package mcpproxy_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/mcpproxy"
	"github.com/dunkeltech/botbox/backend/internal/memory"
	"github.com/google/uuid"
)

func TestMemoryHandlerProtocolAndTurnIDValidation(t *testing.T) {
	t.Parallel()

	handler := mcpproxy.MemoryHandler(memory.Service{}, uuid.New(), uuid.New())

	t.Run("rejects non-POST", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/memory-mcp", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET status = %d, want 405", rec.Code)
		}
	})

	t.Run("accepts notification without id", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodPost, "/memory-mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("notification status = %d, want 202", rec.Code)
		}
	})

	t.Run("initialize and tools/list", func(t *testing.T) {
		t.Parallel()
		for _, method := range []string{"initialize", "tools/list"} {
			body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method})
			req := httptest.NewRequest(http.MethodPost, "/memory-mcp", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s status = %d", method, rec.Code)
			}
			var resp struct {
				Result map[string]any `json:"result"`
				Error  any            `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Error != nil {
				t.Fatalf("%s decode = %v / err=%v", method, err, resp.Error)
			}
			if method == "tools/list" {
				tools, _ := resp.Result["tools"].([]any)
				if len(tools) != 4 {
					t.Fatalf("tools/list count = %d, want 4", len(tools))
				}
			}
		}
	})

	t.Run("tools/call rejects missing or zero turn_id and unknown method", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name string
			body string
			code int
		}{
			{
				name: "missing turn_id",
				body: `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"memory_save","arguments":{"title":"t","content":"c"}}}`,
				code: -32602,
			},
			{
				name: "zero turn_id",
				body: `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"memory_save","arguments":{"turn_id":"00000000-0000-0000-0000-000000000000","title":"t","content":"c"}}}`,
				code: -32602,
			},
			{
				name: "unknown method",
				body: `{"jsonrpc":"2.0","id":4,"method":"resources/list"}`,
				code: -32601,
			},
		} {
			req := httptest.NewRequest(http.MethodPost, "/memory-mcp", bytes.NewBufferString(tc.body))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			var resp struct {
				Error struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("%s decode: %v", tc.name, err)
			}
			if resp.Error.Code != tc.code {
				t.Fatalf("%s rpc error code = %d, want %d", tc.name, resp.Error.Code, tc.code)
			}
		}
	})

	t.Run("tools/call returns safe tool error when service unavailable", func(t *testing.T) {
		t.Parallel()
		body, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      5,
			"method":  "tools/call",
			"params": map[string]any{
				"name": "memory_search",
				"arguments": map[string]any{
					"turn_id": uuid.New().String(),
					"query":   "deploy",
				},
			},
		})
		req := httptest.NewRequest(http.MethodPost, "/memory-mcp", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		var resp struct {
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !resp.Result.IsError || len(resp.Result.Content) == 0 || resp.Result.Content[0].Text == "" {
			t.Fatalf("expected sanitized isError tool result, got %+v", resp.Result)
		}
	})
}
