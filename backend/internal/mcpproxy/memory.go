package mcpproxy

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/dunkeltech/botbox/backend/internal/memory"
	"github.com/google/uuid"
)

// MemoryHandler exposes per-bot knowledge base tools on the bot's private egress proxy.
// A turn ID selects work for the pinned identity and never changes that identity.
func MemoryHandler(svc memory.Service, workspaceID, botID uuid.UUID) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16384))
		if err != nil {
			writeProxyRPCError(w, http.StatusBadRequest, nil, -32600, "invalid memory request body")
			return
		}
		var req rpcRequest
		if json.Unmarshal(raw, &req) != nil || req.JSONRPC != "2.0" || req.Method == "" {
			writeProxyRPCError(w, http.StatusOK, nil, -32600, "invalid JSON-RPC request")
			return
		}
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		switch req.Method {
		case "initialize":
			writeProxyRPCOK(w, req.ID, map[string]any{
				"protocolVersion": mcpProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]string{"name": "botbox-memory", "version": "0.1.0"},
			})
		case "tools/list":
			writeProxyRPCOK(w, req.ID, map[string]any{"tools": memory.ToolDefinitions(true)})
		case "tools/call":
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			var args struct {
				TurnID uuid.UUID `json:"turn_id"`
			}
			if json.Unmarshal(req.Params, &params) != nil || json.Unmarshal(params.Arguments, &args) != nil || args.TurnID == uuid.Nil {
				writeProxyRPCError(w, http.StatusOK, req.ID, -32602, "a valid turn_id is required")
				return
			}
			scope := memory.ToolScope{WorkspaceID: workspaceID, BotID: botID, TurnID: args.TurnID}
			result, callErr := svc.Call(r.Context(), scope, params.Name, params.Arguments)
			writeProxyRPCOK(w, req.ID, map[string]any{
				"isError": callErr != nil,
				"content": []map[string]string{{"type": "text", "text": memory.ToolResult(result, callErr)}},
			})
		default:
			writeProxyRPCError(w, http.StatusOK, req.ID, -32601, "method not found")
		}
	})
}
