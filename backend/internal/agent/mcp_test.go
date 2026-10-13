package agent_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/agent"
)

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  map[string]any  `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func TestMCPHandlerInitializeToolsAndCall(t *testing.T) {
	t.Parallel()

	pngBytes := []byte("\x89PNG\r\n\x1a\nmcp")
	runner := &fakeCmdRunner{out: pngBytes}
	handler := agent.NewMCPHandler(agent.NewDesktopService(runner))

	t.Run("initialize and tools/list over HTTP", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var env rpcEnvelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if env.Error != nil || env.Result["protocolVersion"] != "2025-03-26" {
			t.Fatalf("unexpected initialize result: %+v", env)
		}

		reqList := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
		recList := httptest.NewRecorder()
		handler.ServeHTTP(recList, reqList)
		if recList.Code != http.StatusOK {
			t.Fatalf("tools/list status = %d, want 200", recList.Code)
		}
		var listEnv rpcEnvelope
		if err := json.Unmarshal(recList.Body.Bytes(), &listEnv); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		tools, ok := listEnv.Result["tools"].([]any)
		if !ok || len(tools) != 14 {
			t.Fatalf("tools count = %v, want 14", listEnv.Result["tools"])
		}
	})

	t.Run("tools/call screenshot returns base64 PNG", func(t *testing.T) {
		t.Parallel()

		body := `{"jsonrpc":"2.0","id":"s1","method":"tools/call","params":{"name":"screenshot","arguments":{}}}`
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var env rpcEnvelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode call: %v", err)
		}
		if env.Error != nil {
			t.Fatalf("unexpected rpc error: %+v", env.Error)
		}
		content := env.Result["content"].([]any)
		img := content[0].(map[string]any)
		if img["data"] != base64.StdEncoding.EncodeToString(pngBytes) {
			t.Fatalf("unexpected screenshot data: %v", img["data"])
		}
	})

	t.Run("stdio ServeStream processes multiple lines and skips notifications", func(t *testing.T) {
		t.Parallel()

		input := strings.Join([]string{
			`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
			`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"click","arguments":{"x":100,"y":200,"button":"left"}}}`,
			`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"type","arguments":{"text":"hello"}}}`,
			`{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"key","arguments":{"key":"Return"}}}`,
		}, "\n") + "\n"

		var out bytes.Buffer
		if err := handler.ServeStream(context.Background(), strings.NewReader(input), &out); err != nil {
			t.Fatalf("ServeStream failed: %v", err)
		}
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		if len(lines) != 3 {
			t.Fatalf("got %d responses, want 3 (notification skipped): %q", len(lines), out.String())
		}
	})
}

func TestMCPHandlerNegativePaths(t *testing.T) {
	t.Parallel()

	t.Run("HTTP rejects non-POST and oversized bodies", func(t *testing.T) {
		t.Parallel()

		h := agent.NewMCPHandler(agent.NewDesktopService(&fakeCmdRunner{}))

		getReq := httptest.NewRequest(http.MethodGet, "/mcp", nil)
		getRec := httptest.NewRecorder()
		h.ServeHTTP(getRec, getReq)
		if getRec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET status = %d, want 405", getRec.Code)
		}

		bigBody := strings.NewReader(strings.Repeat("a", (1<<20)+10))
		bigReq := httptest.NewRequest(http.MethodPost, "/mcp", bigBody)
		bigRec := httptest.NewRecorder()
		h.ServeHTTP(bigRec, bigReq)
		if bigRec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("oversized body status = %d, want 413", bigRec.Code)
		}
	})

	tests := []struct {
		name     string
		payload  string
		runner   *fakeCmdRunner
		wantCode int
	}{
		{
			name:     "malformed JSON returns -32700",
			payload:  `{not json`,
			runner:   &fakeCmdRunner{},
			wantCode: -32700,
		},
		{
			name:     "wrong JSON-RPC version returns -32600",
			payload:  `{"jsonrpc":"1.0","id":1,"method":"tools/list"}`,
			runner:   &fakeCmdRunner{},
			wantCode: -32600,
		},
		{
			name:     "unknown method returns -32601",
			payload:  `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`,
			runner:   &fakeCmdRunner{},
			wantCode: -32601,
		},
		{
			name:     "unknown tool returns -32602",
			payload:  `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"shell","arguments":{}}}`,
			runner:   &fakeCmdRunner{},
			wantCode: -32602,
		},
		{
			name:     "out of bounds click maps ErrInvalidDesktopInput to -32602",
			payload:  `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"click","arguments":{"x":9999,"y":10}}}`,
			runner:   &fakeCmdRunner{},
			wantCode: -32602,
		},
		{
			name:     "runner failure maps to -32603",
			payload:  `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"key","arguments":{"key":"Return"}}}`,
			runner:   &fakeCmdRunner{err: errors.New("X11 down")},
			wantCode: -32603,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := agent.NewMCPHandler(agent.NewDesktopService(tc.runner))
			resp, hasResp := h.HandleMessage(context.Background(), []byte(tc.payload))
			if !hasResp {
				t.Fatal("expected JSON-RPC error response")
			}
			encoded, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal resp: %v", err)
			}
			var env rpcEnvelope
			if err := json.Unmarshal(encoded, &env); err != nil {
				t.Fatalf("decode resp: %v", err)
			}
			if env.Error == nil || env.Error.Code != tc.wantCode {
				t.Fatalf("error = %+v, want code %d", env.Error, tc.wantCode)
			}
		})
	}
}
