package authbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

func TestCustomGitLabCDPUsesGuardedContextAcrossConnections(t *testing.T) {
	var endpoint string
	var attaches atomic.Int32
	var guarded atomic.Bool
	var disposed atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/version" {
			_ = json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": strings.Replace(endpoint, "http://", "ws://", 1) + "/devtools/browser"})
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
		session := ""
		for {
			_, raw, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var msg struct {
				ID        int64          `json:"id"`
				Method    string         `json:"method"`
				SessionID string         `json:"sessionId"`
				Params    map[string]any `json:"params"`
			}
			if err = json.Unmarshal(raw, &msg); err != nil {
				return
			}
			result := map[string]any{}
			if msg.SessionID != "" && msg.SessionID != session {
				reply, _ := json.Marshal(map[string]any{"id": msg.ID, "error": map[string]string{"message": "session belongs to another websocket"}})
				_ = conn.Write(r.Context(), websocket.MessageText, reply)
				continue
			}
			switch msg.Method {
			case "Target.createBrowserContext":
				proxy, _ := msg.Params["proxyServer"].(string)
				guarded.Store(strings.HasPrefix(proxy, "http://127.0.0.1:") && msg.Params["proxyBypassList"] == "<-loopback>" && msg.Params["disposeOnDetach"] == false)
				result["browserContextId"] = "context-1"
			case "Target.createTarget":
				result["targetId"] = "target-1"
			case "Target.attachToTarget":
				session = fmt.Sprintf("session-%d", attaches.Add(1))
				result["sessionId"] = session
			case "Page.getFrameTree":
				result["frameTree"] = map[string]any{"frame": map[string]string{"id": "frame-1", "loaderId": "loader-1", "url": "https://gitlab.example.org/users/sign_in"}}
			case "Runtime.evaluate":
				result["result"] = map[string]string{"value": `{"token":"document-1","formAction":"https://gitlab.example.org/users/sign_in"}`}
			case "Target.disposeBrowserContext":
				disposed.Store(true)
			}
			reply, _ := json.Marshal(map[string]any{"id": msg.ID, "result": result})
			if conn.Write(r.Context(), websocket.MessageText, reply) != nil {
				return
			}
		}
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	endpoint = srv.URL
	engine := NewCDPBrowsingEngine(endpoint)
	adapter, err := ResolveLoginAdapter("gitlab_web", "https://gitlab.example.org")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id := uuid.New()
	binding, err := engine.OpenTarget(ctx, uuid.New(), uuid.New(), id, adapter)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = engine.CloseSession(context.Background(), id) }()
	if !guarded.Load() {
		t.Fatal("custom browser context bypasses guarded proxy")
	}
	again, err := engine.InspectBinding(ctx, id)
	if err != nil || again != binding {
		t.Fatalf("binding lost across connections: %v %+v", err, again)
	}
	if attaches.Load() != 2 {
		t.Fatalf("target not reattached: %d", attaches.Load())
	}
	session, err := engine.getSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if session.PublicProxy == nil {
		t.Fatal("missing proxy lifetime binding")
	}
	if err = engine.CloseSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	if !disposed.Load() {
		t.Fatal("browser context not disposed")
	}
	session.PublicProxy.mu.Lock()
	closed := session.PublicProxy.closed
	session.PublicProxy.mu.Unlock()
	if !closed {
		t.Fatal("network tunnel remains after revocation")
	}
}
