package agent_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/dunkeltech/botbox/backend/internal/agent"
	"github.com/dunkeltech/botbox/backend/internal/desk"
)

func TestCDPBrowserNavigateAndRejectsSchemes(t *testing.T) {
	t.Parallel()
	var methods []string
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]string{{
			"type":                 "page",
			"webSocketDebuggerUrl": "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools",
		}})
	})
	mux.HandleFunc("/devtools", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(data, &req) != nil {
			return
		}
		methods = append(methods, req.Method)
		payload := []byte(`{"id":1,"result":{}}`)
		if req.Method == "Runtime.evaluate" {
			payload = []byte(`{"id":1,"result":{"result":{"value":"clicked"}}}`)
		}
		_ = conn.Write(r.Context(), websocket.MessageText, payload)
	})
	server = httptest.NewServer(mux)
	defer server.Close()
	browser := agent.NewCDPBrowser(server.URL)
	if err := browser.Navigate(context.Background(), "file:///etc/passwd"); err == nil {
		t.Fatal("file url was accepted")
	}
	if err := browser.Navigate(context.Background(), "https://example.com/form"); err != nil {
		t.Fatal(err)
	}
	if err := browser.Click(context.Background(), "#submit"); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 2 || methods[0] != "Page.navigate" || methods[1] != "Runtime.evaluate" {
		t.Fatalf("methods = %v", methods)
	}
	desktop := agent.NewDesktopService(nil)
	if _, err := desktop.Perform(context.Background(), desk.Action{Kind: desk.KindBrowserNavigate, URL: desk.FormURL}); err == nil {
		t.Fatal("missing browser should fail")
	}
}
