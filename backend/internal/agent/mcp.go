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

	switch params.Name {
	case "screenshot":
		pngBytes, w, h, err := m.desktop.Screenshot(ctx)
		if err != nil {
			return nil, mapToolError(err)
		}
		return map[string]any{
			"content": []map[string]any{
				{
					"type":     "image",
					"data":     base64.StdEncoding.EncodeToString(pngBytes),
					"mimeType": "image/png",
				},
				{
					"type": "text",
					"text": fmt.Sprintf("Captured %dx%d screenshot", w, h),
				},
			},
		}, nil

	case "click":
		var args struct {
			X      int32  `json:"x"`
			Y      int32  `json:"y"`
			Button string `json:"button"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid click arguments"}
		}
		if err := m.desktop.Click(ctx, args.X, args.Y, args.Button); err != nil {
			return nil, mapToolError(err)
		}
		return textContent(fmt.Sprintf("Clicked %s at (%d, %d)", args.Button, args.X, args.Y)), nil

	case "type":
		var args struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid type arguments"}
		}
		if err := m.desktop.Type(ctx, args.Text); err != nil {
			return nil, mapToolError(err)
		}
		return textContent("Typed text"), nil

	case "key":
		var args struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid key arguments"}
		}
		if err := m.desktop.Key(ctx, args.Key); err != nil {
			return nil, mapToolError(err)
		}
		return textContent("Pressed key " + args.Key), nil

	default:
		return nil, &rpcError{Code: -32602, Message: "unknown tool: " + params.Name}
	}
}

func mapToolError(err error) *rpcError {
	if errors.Is(err, ErrInvalidDesktopInput) {
		return &rpcError{Code: -32602, Message: err.Error()}
	}
	return &rpcError{Code: -32603, Message: err.Error()}
}

func textContent(msg string) map[string]any {
	return map[string]any{
		"content": []map[string]string{
			{"type": "text", "text": msg},
		},
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
	return []map[string]any{
		{
			"name":        "screenshot",
			"description": "Capture the 1280x800 X11 desktop as PNG.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "click",
			"description": "Click mouse button at (x, y) on the 1280x800 desktop.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"x", "y"},
				"properties": map[string]any{
					"x":      map[string]any{"type": "integer", "minimum": 0, "maximum": DesktopWidth - 1},
					"y":      map[string]any{"type": "integer", "minimum": 0, "maximum": DesktopHeight - 1},
					"button": map[string]any{"type": "string", "enum": []string{"left", "right", "middle", "double"}},
				},
			},
		},
		{
			"name":        "type",
			"description": "Type text into the focused window.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"text"},
				"properties": map[string]any{
					"text": map[string]any{"type": "string", "minLength": 1},
				},
			},
		},
		{
			"name":        "key",
			"description": "Press a key or chord such as Return or ctrl+l.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"key"},
				"properties": map[string]any{
					"key": map[string]any{"type": "string", "minLength": 1},
				},
			},
		},
	}
}
