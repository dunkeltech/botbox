package mcpproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/netguard"
)

const (
	// MaxResponseBytes caps upstream MCP JSON-RPC payloads to prevent memory exhaustion.
	MaxResponseBytes = 2 << 20
	// DefaultRequestTimeout bounds upstream MCP discovery and tool execution calls.
	DefaultRequestTimeout = 30 * time.Second
	maxDiscoveryPages     = 10
)

// UpstreamClient sends SSRF-safe Streamable HTTP JSON-RPC 2.0 requests to registered MCP servers.
type UpstreamClient struct {
	HTTPClient *http.Client
	Timeout    time.Duration
}

// UpstreamCallResult contains normalized content returned by an upstream tools/call invocation.
type UpstreamCallResult struct {
	Result  json.RawMessage
	IsError bool
	Text    string
}

// NewUpstreamClient builds an UpstreamClient that validates public DNS answers and dials
// literal public IP addresses via netguard without following redirects.
func NewUpstreamClient() *UpstreamClient {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return netguard.DialPublic(ctx, net.DefaultResolver, dialer.DialContext, network, addr)
		},
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &UpstreamClient{
		Timeout: DefaultRequestTimeout,
		HTTPClient: &http.Client{
			Timeout:   DefaultRequestTimeout,
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("upstream redirects are disallowed")
			},
		},
	}
}

// BuildAuthHeader formats the outbound authentication header from a server auth scheme and decrypted secret.
func BuildAuthHeader(scheme, headerName, username, secretValue string) (name, value string, err error) {
	switch scheme {
	case "", "none":
		return "", "", nil
	case "bearer":
		if strings.TrimSpace(secretValue) == "" {
			return "", "", errors.New("missing bearer token")
		}
		if strings.TrimSpace(headerName) == "" {
			headerName = "Authorization"
		}
		return headerName, "Bearer " + secretValue, nil
	case "header":
		if strings.TrimSpace(headerName) == "" || strings.TrimSpace(secretValue) == "" {
			return "", "", errors.New("missing header name or secret value")
		}
		return headerName, secretValue, nil
	case "basic":
		if strings.TrimSpace(username) == "" || strings.TrimSpace(secretValue) == "" {
			return "", "", errors.New("missing basic username or secret value")
		}
		if strings.TrimSpace(headerName) == "" {
			headerName = "Authorization"
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + secretValue))
		return headerName, "Basic " + encoded, nil
	default:
		return "", "", fmt.Errorf("unsupported auth scheme %q", scheme)
	}
}

type jsonRPCEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// DiscoverTools performs MCP initialize + paginated tools/list against endpointURL.
func (c *UpstreamClient) DiscoverTools(ctx context.Context, endpointURL, authHeaderName, authHeaderValue, secretRaw string) ([]ToolDef, error) {
	initParams := map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]string{
			"name":    "botbox-mcp-proxy",
			"version": "0.1.0",
		},
	}
	sessionID, _, err := c.postRPC(ctx, endpointURL, authHeaderName, authHeaderValue, "", jsonRPCEnvelope{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params:  initParams,
	})
	if err != nil {
		return nil, scrubError(err, authHeaderValue, secretRaw)
	}

	// Best-effort initialized notification per MCP Streamable HTTP lifecycle.
	_, _, _ = c.postRPC(ctx, endpointURL, authHeaderName, authHeaderValue, sessionID, jsonRPCEnvelope{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})

	collected := make([]ToolDef, 0)
	cursor := ""
	for page := 0; page < maxDiscoveryPages; page++ {
		var params any
		if cursor != "" {
			params = map[string]string{"cursor": cursor}
		} else {
			params = map[string]any{}
		}
		nextSession, rawResult, listErr := c.postRPC(ctx, endpointURL, authHeaderName, authHeaderValue, sessionID, jsonRPCEnvelope{
			JSONRPC: "2.0",
			ID:      page + 2,
			Method:  "tools/list",
			Params:  params,
		})
		if listErr != nil {
			return nil, scrubError(listErr, authHeaderValue, secretRaw)
		}
		if nextSession != "" {
			sessionID = nextSession
		}
		var pageOut struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(rawResult, &pageOut); err != nil {
			return nil, errors.New("upstream tools/list returned malformed JSON")
		}
		for _, item := range pageOut.Tools {
			collected = append(collected, ToolDef{
				ToolName:    item.Name,
				Description: item.Description,
				InputSchema: item.InputSchema,
			})
			if len(collected) > MaxToolsPerServer {
				return nil, fmt.Errorf("upstream server exceeds %d tool limit", MaxToolsPerServer)
			}
		}
		if pageOut.NextCursor == "" || pageOut.NextCursor == cursor {
			break
		}
		cursor = pageOut.NextCursor
	}

	return NormalizeToolDefinitions(collected)
}

// CallUpstreamTool executes one JSON-RPC tools/call request and scrubs any credential reflections.
func (c *UpstreamClient) CallUpstreamTool(
	ctx context.Context,
	endpointURL, authHeaderName, authHeaderValue, secretRaw, toolName string,
	arguments json.RawMessage,
) (*UpstreamCallResult, error) {
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	_, rawResult, err := c.postRPC(ctx, endpointURL, authHeaderName, authHeaderValue, "", jsonRPCEnvelope{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params: map[string]any{
			"name":      toolName,
			"arguments": json.RawMessage(arguments),
		},
	})
	if err != nil {
		return nil, scrubError(err, authHeaderValue, secretRaw)
	}

	scrubbed := scrubString(string(rawResult), authHeaderValue, secretRaw)
	var parsed struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	text := scrubbed
	isErr := false
	if json.Unmarshal([]byte(scrubbed), &parsed) == nil {
		isErr = parsed.IsError
		parts := make([]string, 0, len(parsed.Content))
		for _, item := range parsed.Content {
			if item.Text != "" {
				parts = append(parts, item.Text)
			}
		}
		if len(parts) > 0 {
			text = strings.Join(parts, "\n")
		}
	}
	return &UpstreamCallResult{
		Result:  json.RawMessage(scrubbed),
		IsError: isErr,
		Text:    text,
	}, nil
}

func (c *UpstreamClient) httpClient() *http.Client {
	if c != nil && c.HTTPClient != nil {
		return c.HTTPClient
	}
	return NewUpstreamClient().HTTPClient
}

func (c *UpstreamClient) postRPC(
	ctx context.Context,
	endpointURL, authHeaderName, authHeaderValue, sessionID string,
	payload jsonRPCEnvelope,
) (string, json.RawMessage, error) {
	timeout := DefaultRequestTimeout
	if c != nil && c.Timeout > 0 {
		timeout = c.Timeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(payload)
	if err != nil {
		return "", nil, err
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpointURL, bytes.NewReader(body)) //nolint:gosec // validated canonical HTTPS endpoint dialed via netguard
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	if authHeaderName != "" && authHeaderValue != "" {
		req.Header.Set(authHeaderName, authHeaderValue)
	}

	resp, err := c.httpClient().Do(req) //nolint:gosec // validated canonical HTTPS endpoint dialed via netguard
	if err != nil {
		return "", nil, fmt.Errorf("upstream MCP request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort close

	nextSession := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id"))
	limited := io.LimitReader(resp.Body, MaxResponseBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return "", nil, fmt.Errorf("read upstream MCP response: %w", err)
	}
	if len(raw) > MaxResponseBytes {
		return "", nil, fmt.Errorf("upstream MCP response exceeds %d byte limit", MaxResponseBytes)
	}
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		return nextSession, nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("upstream MCP server returned HTTP %d", resp.StatusCode)
	}
	if payload.ID == nil {
		return nextSession, nil, nil
	}

	msgBytes := raw
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		extracted, sseErr := extractJSONRPCFromSSE(raw)
		if sseErr != nil {
			return "", nil, sseErr
		}
		msgBytes = extracted
	}

	var rpcResp jsonRPCEnvelope
	if err := json.Unmarshal(msgBytes, &rpcResp); err != nil {
		return "", nil, errors.New("upstream MCP server returned invalid JSON-RPC")
	}
	if rpcResp.Error != nil {
		return "", nil, fmt.Errorf("upstream MCP error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return nextSession, rpcResp.Result, nil
}

func extractJSONRPCFromSSE(raw []byte) ([]byte, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), MaxResponseBytes)
	var dataLines []string
	var lastJSON []byte
	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		candidate := bytes.TrimSpace([]byte(strings.Join(dataLines, "\n")))
		dataLines = nil
		if len(candidate) > 0 && json.Valid(candidate) {
			lastJSON = candidate
		}
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if payload, ok := strings.CutPrefix(line, "data:"); ok {
			dataLines = append(dataLines, strings.TrimSpace(payload))
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(lastJSON) == 0 {
		return nil, errors.New("upstream SSE stream contained no JSON-RPC payload")
	}
	return lastJSON, nil
}

func scrubString(s string, secretsToMask ...string) string {
	out := s
	for _, sec := range secretsToMask {
		trimmed := strings.TrimSpace(sec)
		if len(trimmed) >= 4 {
			out = strings.ReplaceAll(out, trimmed, "[REDACTED]")
		}
	}
	return out
}

func scrubError(err error, secretsToMask ...string) error {
	if err == nil {
		return nil
	}
	return errors.New(scrubString(err.Error(), secretsToMask...))
}
