// SPDX-License-Identifier: AGPL-3.0-only

package agent_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/agent"
	"github.com/google/uuid"
)

func TestMCPRequestSecretGates(t *testing.T) {
	t.Parallel()
	h := agent.NewMCPHandler(nil)

	// 1. tools/list advertises request_secret.
	listReq := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	resp, ok := h.HandleMessage(context.Background(), []byte(listReq))
	if !ok || resp.Error != nil {
		t.Fatalf("tools/list failed: %+v", resp.Error)
	}
	rawList, _ := json.Marshal(resp.Result)
	if !strings.Contains(string(rawList), `"request_secret"`) {
		t.Fatalf("request_secret missing from tools/list: %s", rawList)
	}

	// 2. browser_login must fail with unsupported_capability before accepting any secret.
	browserReq := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"request_secret","arguments":{"kind":"browser_login","adapter_id":"odoo_staging","reason":"Log into staging"}}}`
	resp, ok = h.HandleMessage(context.Background(), []byte(browserReq))
	if !ok || resp.Error != nil {
		t.Fatalf("expected tool error result for browser_login, got rpcErr=%+v", resp.Error)
	}
	rawBrowser, _ := json.Marshal(resp.Result)
	if !strings.Contains(string(rawBrowser), `"isError":true`) || !strings.Contains(string(rawBrowser), "secret_request.browser_unsupported") {
		t.Fatalf("expected browser_unsupported tool error, got %s", rawBrowser)
	}

	// 3. Reserved env var name or unsupported adapter is rejected.
	badEnvReq := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"request_secret","arguments":{"kind":"cli_egress","adapter_id":"github_api","reason":"Need token","env_placeholder_name":"PATH"}}}`
	resp, _ = h.HandleMessage(context.Background(), []byte(badEnvReq))
	if resp.Error == nil {
		t.Fatalf("expected RPC invalid params error for reserved env var, got %+v", resp.Result)
	}

	badAdapterReq := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"request_secret","arguments":{"kind":"cli_egress","adapter_id":"arbitrary_host","reason":"Need token"}}}`
	resp, _ = h.HandleMessage(context.Background(), []byte(badAdapterReq))
	if resp.Error == nil {
		t.Fatalf("expected RPC invalid params error for unknown adapter, got %+v", resp.Result)
	}

	// 4. Valid cli_egress request succeeds without secret material.
	goodReq := `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"request_secret","arguments":{"kind":"cli_egress","adapter_id":"github_api","reason":"List releases","env_placeholder_name":"GH_TOKEN"}}}`
	resp, _ = h.HandleMessage(context.Background(), []byte(goodReq))
	if resp.Error != nil {
		t.Fatalf("expected valid cli_egress request to succeed, got %+v", resp.Error)
	}

	// 5. broker_open also fails closed when broker is disabled.
	brokerOffReq := `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"broker_open","arguments":{"broker_session_id":"11111111-1111-1111-1111-111111111111","url":"https://github.com/"}}}`
	resp, ok = h.HandleMessage(context.Background(), []byte(brokerOffReq))
	if !ok || resp.Error != nil {
		t.Fatalf("expected tool error for disabled broker_open, got %+v", resp.Error)
	}
	rawOff, _ := json.Marshal(resp.Result)
	if !strings.Contains(string(rawOff), "secret_request.browser_unsupported") {
		t.Fatalf("expected browser_unsupported for broker_open when disabled, got %s", rawOff)
	}
}

func TestMCPBrokerEnabledAdvertisesAdaptersAndBridgesCalls(t *testing.T) {
	t.Parallel()
	var gotPath string
	var gotBody string
	bridgeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/broker/navigate":
			_, _ = w.Write([]byte(`{"origin":"https://github.com:443","status":"authenticated","page_url":"https://github.com/issues"}`))
		case "/broker/read":
			_, _ = w.Write([]byte(`{"selector":"main","text":"3 open issues"}`))
		case "/broker/screenshot":
			_, _ = w.Write([]byte(`{"mime":"image/png","image":"iVBORw0KGgo=","page_url":"https://github.com/issues","origin":"https://github.com:443","sanitized":true}`))
		default:
			http.Error(w, "unknown", http.StatusNotFound)
		}
	}))
	defer bridgeSrv.Close()

	h := agent.NewMCPHandler(nil)
	h.BrokerEnabled = true
	h.BrokerProxyURL = bridgeSrv.URL

	// 1. tools/list advertises reviewed browser adapters + broker_* tools and drops stale warning.
	resp, ok := h.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if !ok || resp.Error != nil {
		t.Fatalf("tools/list failed: %+v", resp.Error)
	}
	rawList, _ := json.Marshal(resp.Result)
	for _, want := range []string{`"odoo_staging"`, `"gitlab_web"`, `"github_web"`, `"broker_open"`, `"broker_read"`, `"broker_click"`, `"broker_screenshot"`} {
		if !strings.Contains(string(rawList), want) {
			t.Fatalf("tools/list missing %s: %s", want, rawList)
		}
	}
	if strings.Contains(string(rawList), "browser_login returns unsupported_capability") {
		t.Fatalf("stale unsupported warning remained in tools/list: %s", rawList)
	}

	// 2. request_secret accepts valid browser_login and rejects env_placeholder_name on browser_login.
	resp, _ = h.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"request_secret","arguments":{"kind":"browser_login","adapter_id":"odoo_staging","reason":"Check sales orders"}}}`))
	if resp.Error != nil {
		t.Fatalf("expected browser_login acceptance, got %+v", resp.Error)
	}
	resp, _ = h.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"request_secret","arguments":{"kind":"browser_login","adapter_id":"odoo_staging","reason":"Bad env","env_placeholder_name":"ODOO_PW"}}}`))
	if resp.Error == nil {
		t.Fatal("expected env_placeholder_name rejection for browser_login")
	}

	// 3. broker_open / broker_read / broker_screenshot bridge cleanly.
	sid := uuid.New().String()
	resp, _ = h.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"broker_open","arguments":{"broker_session_id":"`+sid+`","url":"https://github.com/issues"}}}`))
	if resp.Error != nil || gotPath != "/broker/navigate" || !strings.Contains(gotBody, sid) {
		t.Fatalf("broker_open bridge failed: err=%+v path=%s body=%s", resp.Error, gotPath, gotBody)
	}
	resp, _ = h.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"broker_read","arguments":{"broker_session_id":"`+sid+`","selector":"main"}}}`))
	if resp.Error != nil || gotPath != "/broker/read" {
		t.Fatalf("broker_read bridge failed: err=%+v path=%s", resp.Error, gotPath)
	}
	resp, _ = h.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"broker_screenshot","arguments":{"broker_session_id":"`+sid+`"}}}`))
	if resp.Error != nil || gotPath != "/broker/screenshot" {
		t.Fatalf("broker_screenshot bridge failed: err=%+v path=%s", resp.Error, gotPath)
	}
	rawShot, _ := json.Marshal(resp.Result)
	if !strings.Contains(string(rawShot), `"type":"image"`) {
		t.Fatalf("expected image content in broker_screenshot result, got %s", rawShot)
	}
}

func TestMCPRequestSecretCustomGitLabOrigin(t *testing.T) {
	h := agent.NewMCPHandler(nil)
	h.BrokerEnabled = true
	for _, tt := range []struct {
		adapter, origin string
		valid           bool
	}{
		{"gitlab_web", "https://gitlab.example.org", true},
		{"gitlab_web", "https://gitlab.example.org:8443", false},
		{"gitlab_web", "https://gitlab.example.org/path", false},
		{"github_web", "https://gitlab.example.org", false},
	} {
		t.Run(tt.adapter+tt.origin, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "request_secret", "arguments": map[string]any{"kind": "browser_login", "adapter_id": tt.adapter, "target_origin": tt.origin, "reason": "Review project"}}})
			resp, ok := h.HandleMessage(context.Background(), raw)
			if !ok || (resp.Error == nil) != tt.valid {
				t.Fatalf("valid=%v rpc=%+v", tt.valid, resp.Error)
			}
		})
	}
}
