package agent_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/dunkeltech/botbox/backend/internal/agent"
	"github.com/dunkeltech/botbox/backend/internal/desk"
)

func newMockCDPServer(t *testing.T, respond func(method string, params json.RawMessage) []byte) *httptest.Server {
	t.Helper()
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
		_ = conn.Write(r.Context(), websocket.MessageText, respond(req.Method, req.Params))
	})
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestCDPBrowserNavigateAndRejectsSchemes(t *testing.T) {
	t.Parallel()
	var methods []string
	server := newMockCDPServer(t, func(method string, _ json.RawMessage) []byte {
		methods = append(methods, method)
		if method == "Runtime.evaluate" {
			return []byte(`{"id":1,"result":{"result":{"value":"clicked"}}}`)
		}
		return []byte(`{"id":1,"result":{}}`)
	})
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

func TestCDPBrowserLargeResponsesAndLimitEnforcement(t *testing.T) {
	t.Parallel()
	largeText := strings.Repeat("x", 64<<10)
	largeImage := bytes.Repeat([]byte{0x89, 0x50, 0x4e, 0x47}, 16<<10)
	largeEncoded := base64.StdEncoding.EncodeToString(largeImage)

	server := newMockCDPServer(t, func(method string, _ json.RawMessage) []byte {
		if method == "Page.captureScreenshot" {
			payload, _ := json.Marshal(map[string]any{
				"id":     1,
				"result": map[string]any{"data": largeEncoded},
			})
			return payload
		}
		payload, _ := json.Marshal(map[string]any{
			"id":     1,
			"result": map[string]any{"result": map[string]any{"value": largeText}},
		})
		return payload
	})
	browser := agent.NewCDPBrowser(server.URL)

	shot, err := browser.Shot(context.Background(), "")
	if err != nil {
		t.Fatalf("Shot failed for >32 KiB response: %v", err)
	}
	if !bytes.Equal(shot, largeImage) {
		t.Fatalf("shot len = %d, want %d", len(shot), len(largeImage))
	}

	readText, err := browser.Read(context.Background(), "")
	if err != nil {
		t.Fatalf("Read failed for >32 KiB response: %v", err)
	}
	if readText != largeText {
		t.Fatalf("readText len = %d, want %d", len(readText), len(largeText))
	}

	oversized := make([]byte, agent.MaxCDPMessageBytes+1)
	for i := range oversized {
		oversized[i] = 'a'
	}
	oversizedServer := newMockCDPServer(t, func(string, json.RawMessage) []byte {
		return oversized
	})
	oversizedBrowser := agent.NewCDPBrowser(oversizedServer.URL)
	if _, err := oversizedBrowser.Read(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "message too big") {
		t.Fatalf("expected message too big error, got %v", err)
	}
}
