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

	"github.com/dunkeltech/botbox/backend/internal/desk"
)

const maxMCPBodyBytes = 1 << 20

// MCPHandler exposes DesktopService over JSON-RPC 2.0 (Stdio and Streamable HTTP /mcp)
// so both claude and agy CLIs can call desktop tools directly inside the sandbox.
type MCPHandler struct {
	desktop *DesktopService
}

// NewMCPHandler constructs an MCP server backed by desktop.
func NewMCPHandler(desktop *DesktopService) *MCPHandler {
	if desktop == nil {
		desktop = NewDesktopService(nil)
	}
	return &MCPHandler{desktop: desktop}
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
			Result:  map[string]any{"tools": mcpToolsList()},
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

func mcpToolsList() []map[string]any {
	point := map[string]any{
		"x":            map[string]any{"type": "integer"},
		"y":            map[string]any{"type": "integer"},
		"image_width":  map[string]any{"type": "integer"},
		"image_height": map[string]any{"type": "integer"},
	}
	return []map[string]any{
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
	}
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
