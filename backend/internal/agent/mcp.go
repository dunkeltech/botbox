// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/authbroker"
	"github.com/dunkeltech/botbox/backend/internal/desk"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"github.com/google/uuid"
)

const maxMCPBodyBytes = 1 << 20

// MCPHandler exposes DesktopService over JSON-RPC 2.0 (Stdio and Streamable HTTP /mcp)
// so both claude and agy CLIs can call desktop tools directly inside the sandbox.
type MCPHandler struct {
	desktop         *DesktopService
	BrokerEnabled   bool
	MCPProxyEnabled bool
	BrokerProxyURL  string
	BrokerClient    *http.Client
}

// NewMCPHandler constructs an MCP server backed by desktop.
func NewMCPHandler(desktop *DesktopService) *MCPHandler {
	if desktop == nil {
		desktop = NewDesktopService(nil)
	}
	return &MCPHandler{
		desktop:        desktop,
		BrokerProxyURL: "http://egress:8081",
		BrokerClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{Proxy: nil},
		},
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ServeHTTP handles Streamable HTTP JSON-RPC POST requests on /mcp.
func (m *MCPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	limited := io.LimitReader(r.Body, maxMCPBodyBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		writeRPCErrorHTTP(w, http.StatusBadRequest, nil, -32700, "failed to read request body")
		return
	}
	if len(raw) > maxMCPBodyBytes {
		writeRPCErrorHTTP(w, http.StatusRequestEntityTooLarge, nil, -32600, "request body exceeds limit")
		return
	}

	resp, hasResp := m.HandleMessage(r.Context(), raw)
	if !hasResp {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// ServeStream processes line-delimited JSON-RPC 2.0 requests over stdio.
func (m *MCPHandler) ServeStream(ctx context.Context, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxMCPBodyBytes)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		resp, hasResp := m.HandleMessage(ctx, line)
		if !hasResp {
			continue
		}
		encoded, err := json.Marshal(resp)
		if err != nil {
			return fmt.Errorf("marshal MCP response: %w", err)
		}
		if _, err := fmt.Fprintf(w, "%s\n", encoded); err != nil {
			return fmt.Errorf("write MCP response: %w", err)
		}
	}
	return scanner.Err()
}

// HandleMessage dispatches one JSON-RPC 2.0 message and returns whether a response is required.
func (m *MCPHandler) HandleMessage(ctx context.Context, raw []byte) (rpcResponse, bool) {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      json.RawMessage("null"),
			Error:   &rpcError{Code: -32700, Message: "parse error"},
		}, true
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      normID(req.ID),
			Error:   &rpcError{Code: -32600, Message: "invalid JSON-RPC request"},
		}, true
	}

	// JSON-RPC notifications omit id and do not produce responses.
	if len(req.ID) == 0 {
		return rpcResponse{}, false
	}

	id := normID(req.ID)
	switch req.Method {
	case "initialize":
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      id,
			Result: map[string]any{
				"protocolVersion": "2025-03-26",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]string{
					"name":    "botbox-agent-mcp",
					"version": "0.1.0",
				},
			},
		}, true
	case "tools/list":
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      id,
			Result:  map[string]any{"tools": m.mcpToolsList(ctx)},
		}, true
	case "tools/call":
		res, rpcErr := m.callTool(ctx, req.Params)
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      id,
			Result:  res,
			Error:   rpcErr,
		}, true
	default:
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      id,
			Error:   &rpcError{Code: -32601, Message: "method not found"},
		}, true
	}
}

func (m *MCPHandler) callTool(ctx context.Context, rawParams json.RawMessage) (any, *rpcError) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(rawParams, &params); err != nil || params.Name == "" {
		return nil, &rpcError{Code: -32602, Message: "invalid tools/call params"}
	}

	if params.Name == "request_secret" {
		return m.handleRequestSecretMCP(params.Arguments)
	}
	if isBrokerMCPTool(params.Name) {
		return m.handleBrokerBridgeMCP(ctx, params.Name, params.Arguments)
	}
	if m.MCPProxyEnabled && strings.HasPrefix(params.Name, "mcp__") {
		return m.handleMCPProxyCall(ctx, params.Name, params.Arguments)
	}

	act, err := desk.Parse(params.Name, params.Arguments)
	if err != nil {
		return nil, mapToolError(err)
	}
	result, err := m.desktop.Perform(ctx, act)
	if err != nil {
		return nil, mapToolError(err)
	}
	content := []map[string]any{}
	if len(result.Image) > 0 {
		mime := result.Mime
		if mime == "" {
			mime = "image/png"
		}
		content = append(content, map[string]any{"type": "image", "data": base64.StdEncoding.EncodeToString(result.Image), "mimeType": mime})
	}
	content = append(content, map[string]any{"type": "text", "text": result.Text})
	return map[string]any{"content": content}, nil
}

func mapToolError(err error) *rpcError {
	switch {
	case errors.Is(err, ErrControlLocked):
		return &rpcError{Code: -32001, Message: err.Error()}
	case errors.Is(err, ErrInvalidDesktopInput), errors.Is(err, desk.ErrInvalid):
		return &rpcError{Code: -32602, Message: err.Error()}
	default:
		return &rpcError{Code: -32603, Message: err.Error()}
	}
}

func normID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

func writeRPCErrorHTTP(w http.ResponseWriter, status int, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(rpcResponse{
		JSONRPC: "2.0",
		ID:      normID(id),
		Error:   &rpcError{Code: code, Message: msg},
	})
}

func (m *MCPHandler) mcpToolsList(ctx context.Context) []map[string]any {
	point := map[string]any{
		"x":            map[string]any{"type": "integer"},
		"y":            map[string]any{"type": "integer"},
		"image_width":  map[string]any{"type": "integer"},
		"image_height": map[string]any{"type": "integer"},
	}
	adapterIDs := []string{"github_api", "gitlab_api", "gitlab_git", "npm_registry", "pypi_upload", "pypi_simple"}
	requestSecretDesc := "Request a missing credential securely in chat without placing secrets into AI context. Only cli_egress with a reviewed adapter is currently supported; browser_login returns unsupported_capability."
	if m.BrokerEnabled {
		requestSecretDesc = "Request a missing credential securely in chat without placing secrets into AI context. Supports cli_egress (reviewed HTTPS adapters with env_placeholder_name) and browser_login (isolated external browser login adapters, without env_placeholder_name)."
		for _, la := range authbroker.ListLoginAdapters() {
			adapterIDs = append(adapterIDs, la.ID)
		}
	}

	tools := []map[string]any{
		{"name": "screenshot", "description": "Capture the 1280x800 desktop. Optional format png or jpeg and max_width. Coordinates on a downscaled shot are scaled back to the desktop.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"format": map[string]any{"type": "string", "enum": []string{"png", "jpeg"}}, "max_width": map[string]any{"type": "integer"}, "quality": map[string]any{"type": "integer"}}}},
		{"name": "cursor_position", "description": "Read the pointer position on the 1280x800 desktop.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
		{"name": "mouse_move", "description": "Move the pointer. Prefer shell or browser tools when a page has selectors.", "inputSchema": map[string]any{"type": "object", "required": []string{"x", "y"}, "properties": point}},
		{"name": "click", "description": "Click a mouse button at x, y.", "inputSchema": map[string]any{"type": "object", "required": []string{"x", "y"}, "properties": merge(point, map[string]any{"button": map[string]any{"type": "string", "enum": []string{"left", "right", "middle", "double", "triple"}}})}},
		{"name": "drag", "description": "Drag the left button from x, y to to_x, to_y.", "inputSchema": map[string]any{"type": "object", "required": []string{"x", "y", "to_x", "to_y"}, "properties": merge(point, map[string]any{"to_x": map[string]any{"type": "integer"}, "to_y": map[string]any{"type": "integer"}})}},
		{"name": "scroll", "description": "Scroll by dx and dy ticks, at most 20. Positive dy scrolls down.", "inputSchema": map[string]any{"type": "object", "properties": merge(point, map[string]any{"dx": map[string]any{"type": "integer"}, "dy": map[string]any{"type": "integer"}})}},
		{"name": "type", "description": "Type text into the focused window with a short per-character delay.", "inputSchema": map[string]any{"type": "object", "required": []string{"text"}, "properties": map[string]any{"text": map[string]any{"type": "string"}}}},
		{"name": "key", "description": "Press a key or chord such as Return or ctrl+l.", "inputSchema": map[string]any{"type": "object", "required": []string{"key"}, "properties": map[string]any{"key": map[string]any{"type": "string"}}}},
		{"name": "wait", "description": "Wait up to 10 seconds. A human takeover cancels the wait.", "inputSchema": map[string]any{"type": "object", "required": []string{"ms"}, "properties": map[string]any{"ms": map[string]any{"type": "integer"}}}},
		{"name": "browser_navigate", "description": "Open an http(s) URL in the sandbox browser. More reliable than pixel clicks on web pages.", "inputSchema": map[string]any{"type": "object", "required": []string{"url"}, "properties": map[string]any{"url": map[string]any{"type": "string"}}}},
		{"name": "browser_click", "description": "Click a CSS selector in the sandbox browser.", "inputSchema": map[string]any{"type": "object", "required": []string{"selector"}, "properties": map[string]any{"selector": map[string]any{"type": "string"}}}},
		{"name": "browser_fill", "description": "Fill a form control selected by CSS selector.", "inputSchema": map[string]any{"type": "object", "required": []string{"selector", "text"}, "properties": map[string]any{"selector": map[string]any{"type": "string"}, "text": map[string]any{"type": "string"}}}},
		{"name": "browser_read", "description": "Read visible text from a CSS selector, or the page when selector is omitted.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"selector": map[string]any{"type": "string"}}}},
		{"name": "browser_screenshot", "description": "Capture a browser element, or the viewport when selector is omitted.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"selector": map[string]any{"type": "string"}}}},
		{"name": "request_secret", "description": requestSecretDesc, "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"kind", "adapter_id", "reason"},
			"properties": map[string]any{
				"kind":                 map[string]any{"type": "string", "enum": []string{"cli_egress", "browser_login"}},
				"adapter_id":           map[string]any{"type": "string", "enum": adapterIDs},
				"reason":               map[string]any{"type": "string"},
				"target_origin":        map[string]any{"type": "string", "description": "For self-hosted GitLab browser_login or cli_egress, use the exact workspace-approved HTTPS origin assigned to this bot. Other providers retain fixed origins."},
				"env_placeholder_name": map[string]any{"type": "string"},
				"username_hint":        map[string]any{"type": "string"},
			},
		}},
	}
	if m.BrokerEnabled {
		tools = append(tools,
			map[string]any{
				"name":        "broker_open",
				"description": "Navigate an authenticated isolated broker browser session to an adapter-permitted HTTPS URL.",
				"inputSchema": map[string]any{
					"type":     "object",
					"required": []string{"broker_session_id", "url"},
					"properties": map[string]any{
						"broker_session_id": map[string]any{"type": "string"},
						"url":               map[string]any{"type": "string"},
					},
				},
			},
			map[string]any{
				"name":        "broker_read",
				"description": "Read sanitized visible text for a non-credential CSS selector in an authenticated broker session.",
				"inputSchema": map[string]any{
					"type":     "object",
					"required": []string{"broker_session_id", "selector"},
					"properties": map[string]any{
						"broker_session_id": map[string]any{"type": "string"},
						"selector":          map[string]any{"type": "string"},
					},
				},
			},
			map[string]any{
				"name":        "broker_click",
				"description": "Click a non-credential CSS selector inside an authenticated broker session.",
				"inputSchema": map[string]any{
					"type":     "object",
					"required": []string{"broker_session_id", "selector"},
					"properties": map[string]any{
						"broker_session_id": map[string]any{"type": "string"},
						"selector":          map[string]any{"type": "string"},
					},
				},
			},
			map[string]any{
				"name":        "broker_screenshot",
				"description": "Capture a sanitized PNG screenshot from an authenticated broker session when not in takeover or credential entry.",
				"inputSchema": map[string]any{
					"type":     "object",
					"required": []string{"broker_session_id"},
					"properties": map[string]any{
						"broker_session_id": map[string]any{"type": "string"},
					},
				},
			},
		)
	}
	if m.MCPProxyEnabled {
		tools = append(tools, m.fetchMCPProxyTools(ctx)...)
	}
	return tools
}

func (m *MCPHandler) fetchMCPProxyTools(ctx context.Context) []map[string]any {
	baseURL := strings.TrimRight(strings.TrimSpace(m.BrokerProxyURL), "/")
	if baseURL == "" {
		baseURL = "http://egress:8081"
	}
	client := m.BrokerClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	}
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, baseURL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)) //nolint:gosec // controlled per-bot egress proxy MCP endpoint
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req) //nolint:gosec // controlled per-bot egress proxy MCP endpoint
	if err != nil {
		return nil
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort close
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var rpc struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&rpc) != nil {
		return nil
	}
	return rpc.Result.Tools
}

// DiscoverAssignedMCPSlugs queries the egress proxy /mcp tools catalog and returns
// unique sorted MCP server slugs exposed to this sandbox.
func DiscoverAssignedMCPSlugs(ctx context.Context, proxyURL string) []string {
	trimmed := strings.TrimRight(strings.TrimSpace(proxyURL), "/")
	if trimmed == "" {
		return nil
	}
	h := &MCPHandler{
		BrokerProxyURL:  trimmed,
		MCPProxyEnabled: true,
		BrokerClient: &http.Client{
			Timeout:   3 * time.Second,
			Transport: &http.Transport{Proxy: nil},
		},
	}
	tools := h.fetchMCPProxyTools(ctx)
	if len(tools) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var slugs []string
	for _, tool := range tools {
		name, _ := tool["name"].(string)
		rest, ok := strings.CutPrefix(name, "mcp__")
		if !ok {
			continue
		}
		slug, _, hasSep := strings.Cut(rest, "__")
		if !hasSep || slug == "" {
			continue
		}
		if _, exists := seen[slug]; !exists {
			seen[slug] = struct{}{}
			slugs = append(slugs, slug)
		}
	}
	sort.Strings(slugs)
	return slugs
}

func (m *MCPHandler) handleMCPProxyCall(ctx context.Context, name string, raw json.RawMessage) (any, *rpcError) {
	baseURL := strings.TrimRight(strings.TrimSpace(m.BrokerProxyURL), "/")
	if baseURL == "" {
		baseURL = "http://egress:8081"
	}
	client := m.BrokerClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}}
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": json.RawMessage(raw),
		},
	})
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/mcp", strings.NewReader(string(body))) //nolint:gosec // controlled per-bot egress proxy MCP endpoint
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req) //nolint:gosec // controlled per-bot egress proxy MCP endpoint
	if err != nil {
		return map[string]any{
			"isError": true,
			"content": []map[string]any{{"type": "text", "text": "mcp proxy unavailable"}},
		}, nil
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort close
	rawResp, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}
	var rpc rpcResponse
	if err := json.Unmarshal(rawResp, &rpc); err != nil {
		return nil, &rpcError{Code: -32603, Message: "invalid mcp proxy response"}
	}
	if rpc.Error != nil {
		return nil, rpc.Error
	}
	return rpc.Result, nil
}

func (m *MCPHandler) handleRequestSecretMCP(raw json.RawMessage) (any, *rpcError) {
	var args struct {
		Kind               string `json:"kind"`
		AdapterID          string `json:"adapter_id"`
		Reason             string `json:"reason"`
		TargetOrigin       string `json:"target_origin"`
		EnvPlaceholderName string `json:"env_placeholder_name"`
		UsernameHint       string `json:"username_hint"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid request_secret arguments"}
	}
	if args.Kind == "browser_login" {
		if !m.BrokerEnabled {
			return map[string]any{
				"isError": true,
				"content": []map[string]any{{
					"type": "text",
					"text": "secret_request.browser_unsupported: browser_login is disabled until the isolated authentication broker is available",
				}},
			}, nil
		}
		_, resolveErr := authbroker.ResolveLoginAdapter(args.AdapterID, args.TargetOrigin)
		if resolveErr != nil {
			return nil, &rpcError{Code: -32602, Message: "unsupported browser login adapter"}
		}
		if args.Reason == "" || len(args.Reason) > 500 {
			return nil, &rpcError{Code: -32602, Message: "reason must be 1..500 bytes"}
		}
		if args.EnvPlaceholderName != "" {
			return nil, &rpcError{Code: -32602, Message: "env_placeholder_name is not valid for browser_login"}
		}
		return map[string]any{
			"content": []map[string]any{{
				"type": "text",
				"text": "Browser login secret request submitted to chat. Waiting for human input.",
			}},
		}, nil
	}
	if args.Kind != "cli_egress" {
		return nil, &rpcError{Code: -32602, Message: "unsupported request_secret kind"}
	}
	_, resolveErr := mitmproxy.ResolveAdapter(args.AdapterID, args.TargetOrigin)
	if resolveErr != nil {
		return nil, &rpcError{Code: -32602, Message: "unsupported HTTPS credential adapter"}
	}
	if args.Reason == "" || len(args.Reason) > 500 {
		return nil, &rpcError{Code: -32602, Message: "reason must be 1..500 bytes"}
	}
	if err := mitmproxy.ValidateEnvPlaceholderName(args.EnvPlaceholderName); err != nil {
		return nil, &rpcError{Code: -32602, Message: err.Error()}
	}
	return map[string]any{
		"content": []map[string]any{{
			"type": "text",
			"text": "Secret request submitted to chat. Waiting for human input.",
		}},
	}, nil
}

func isBrokerMCPTool(name string) bool {
	switch name {
	case "broker_open", "broker_read", "broker_click", "broker_screenshot":
		return true
	default:
		return false
	}
}

func (m *MCPHandler) handleBrokerBridgeMCP(ctx context.Context, name string, raw json.RawMessage) (any, *rpcError) {
	if !m.BrokerEnabled {
		return map[string]any{
			"isError": true,
			"content": []map[string]any{{
				"type": "text",
				"text": "secret_request.browser_unsupported: isolated authentication broker is disabled",
			}},
		}, nil
	}
	var in struct {
		BrokerSessionID string `json:"broker_session_id"`
		URL             string `json:"url"`
		Selector        string `json:"selector"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid broker tool arguments"}
	}
	if _, err := uuid.Parse(strings.TrimSpace(in.BrokerSessionID)); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid broker_session_id"}
	}

	var route string
	switch name {
	case "broker_open":
		if strings.TrimSpace(in.URL) == "" {
			return nil, &rpcError{Code: -32602, Message: "url is required"}
		}
		route = "/broker/navigate"
	case "broker_read":
		if strings.TrimSpace(in.Selector) == "" {
			return nil, &rpcError{Code: -32602, Message: "selector is required"}
		}
		route = "/broker/read"
	case "broker_click":
		if strings.TrimSpace(in.Selector) == "" {
			return nil, &rpcError{Code: -32602, Message: "selector is required"}
		}
		route = "/broker/click"
	case "broker_screenshot":
		route = "/broker/screenshot"
	default:
		return nil, &rpcError{Code: -32601, Message: "unknown broker tool"}
	}

	baseURL := strings.TrimRight(strings.TrimSpace(m.BrokerProxyURL), "/")
	if baseURL == "" {
		baseURL = "http://egress:8081"
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}
	client := m.BrokerClient
	if client == nil {
		client = &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{Proxy: nil},
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+route, strings.NewReader(string(payload))) //nolint:gosec // controlled per-bot egress proxy bridge
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req) //nolint:gosec // controlled per-bot egress proxy bridge
	if err != nil {
		return map[string]any{
			"isError": true,
			"content": []map[string]any{{"type": "text", "text": "broker bridge unavailable: " + err.Error()}},
		}, nil
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort close
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}
	if resp.StatusCode >= 400 {
		return map[string]any{
			"isError": true,
			"content": []map[string]any{{"type": "text", "text": strings.TrimSpace(string(body))}},
		}, nil
	}
	if name == "broker_screenshot" {
		var shot struct {
			Mime    string `json:"mime"`
			Image   string `json:"image"`
			PageURL string `json:"page_url"`
			Origin  string `json:"origin"`
		}
		if json.Unmarshal(body, &shot) == nil && shot.Image != "" {
			mime := shot.Mime
			if mime == "" {
				mime = "image/png"
			}
			return map[string]any{
				"content": []map[string]any{
					{"type": "image", "data": shot.Image, "mimeType": mime},
					{"type": "text", "text": fmt.Sprintf("Captured sanitized broker screenshot on %s", shot.PageURL)},
				},
			}, nil
		}
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": strings.TrimSpace(string(body))}},
	}, nil
}

func merge(base, extra map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
