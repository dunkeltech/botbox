package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/google/uuid"
)

type fakeChatEvents struct {
	mu      sync.Mutex
	deliver func([]byte)
}

func (f *fakeChatEvents) SubscribeConversation(_, _ uuid.UUID, deliver func([]byte)) (func(), error) {
	f.mu.Lock()
	f.deliver = deliver
	f.mu.Unlock()
	return func() { f.mu.Lock(); f.deliver = nil; f.mu.Unlock() }, nil
}
func (f *fakeChatEvents) send(data string) {
	f.mu.Lock()
	deliver := f.deliver
	f.mu.Unlock()
	if deliver != nil {
		deliver([]byte(data))
	}
}

func TestPrivateChatWebSocketAndRevocation(t *testing.T) {
	events := &fakeChatEvents{}
	h := newTestHarness(t, nil, harnessOpts{chatEvents: events})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member@example.com", "member", "Member", "CorrectHorse123!")
	r := h.request(http.MethodPost, base+"/bots", map[string]any{"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "api", "llmProvider": "openrouter", "llmModel": "fixture"}, owner.AccessToken, nil, true)
	var bot apigen.Bot
	r.decode(t, &bot)
	r = h.request(http.MethodPost, base+"/bots/"+bot.Id.String()+"/dm", nil, member.AccessToken, nil, true)
	var conversation apigen.ChatConversation
	r.decode(t, &conversation)
	server := httptest.NewServer(h.handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/chat/" + ws.String() + "/" + conversation.Id.String()
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {owner.AccessToken, 404}} {
		_, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{"botbox.events", "botbox.auth." + tc.token}})
		if err == nil || response == nil || response.StatusCode != tc.status {
			t.Fatalf("private socket response=%v err=%v", response, err)
		}
		if response.Body != nil {
			_ = response.Body.Close()
		}
	}
	conn, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{"botbox.events", "botbox.auth." + member.AccessToken}})
	if err != nil {
		t.Fatal(err)
	}
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	defer func() { _ = conn.CloseNow() }()
	events.send("private delta")
	_, data, err := conn.Read(ctx)
	if err != nil || string(data) != "private delta" {
		t.Fatalf("event=%s err=%v", data, err)
	}
	removed := h.request(http.MethodDelete, base+"/members/"+member.User.Id.String(), nil, owner.AccessToken, nil, true)
	if removed.status != 204 {
		t.Fatalf("remove=%d %s", removed.status, removed.body)
	}
	events.send("must not be delivered")
	if _, data, err = conn.Read(ctx); err == nil || websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("revoked event=%s err=%v", data, err)
	}
}
