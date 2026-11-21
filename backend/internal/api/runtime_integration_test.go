// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/engine"
	"github.com/dunkeltech/botbox/backend/internal/engine/enginetest"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/google/uuid"
)

func (q *fakeTurnQueue) Interrupt(_ context.Context, ws, bot uuid.UUID) error {
	if q.interrupt != nil {
		q.interrupt(ws, bot)
	}
	return q.err
}

func TestPhase4PauseResumeAndTakeover(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	r := h.request(http.MethodPost, base+"/bots", map[string]any{"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "cli", "llmProvider": "openrouter", "llmModel": "openrouter/fixture"}, owner.AccessToken, nil, true)
	var bot apigen.Bot
	r.decode(t, &bot)
	path := base + "/bots/" + bot.Id.String() + "/runtime"
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member@example.com", "member", "Member", "CorrectHorse123!")
	admin, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "admin@example.com", "admin", "Admin", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "viewer@example.com", "viewer", "Viewer", "CorrectHorse123!")
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {member.AccessToken, 403}, {viewer.AccessToken, 403}, {admin.AccessToken, 200}, {owner.AccessToken, 200}} {
		r = h.request(http.MethodPut, path, map[string]any{"paused": true}, tc.token, nil, true)
		if r.status != tc.status {
			t.Fatalf("pause=%d want=%d %s", r.status, tc.status, r.body)
		}
	}
	for _, body := range []any{nil, map[string]any{}, map[string]any{"paused": nil}, map[string]any{"paused": "true"}, map[string]any{"paused": 1}, map[string]any{"paused": true, "unknown": true}} {
		if r = h.request(http.MethodPut, path, body, owner.AccessToken, nil, true); r.status != 400 {
			t.Fatalf("invalid=%d %s", r.status, r.body)
		}
	}
	for _, token := range []string{owner.AccessToken, admin.AccessToken, member.AccessToken, viewer.AccessToken} {
		r = h.request(http.MethodGet, path, nil, token, nil, false)
		var state apigen.BotRuntime
		r.decode(t, &state)
		if r.status != 200 || !state.Paused {
			t.Fatalf("state=%d %s", r.status, r.body)
		}
	}
	r = h.request(http.MethodPost, base+"/bots/"+bot.Id.String()+"/dm", nil, owner.AccessToken, nil, true)
	var conv apigen.ChatConversation
	r.decode(t, &conv)
	r = h.request(http.MethodPost, base+"/conversations/"+conv.Id.String()+"/messages", map[string]any{"id": uuid.New(), "content": "Keep this queued"}, owner.AccessToken, nil, true)
	if r.status != 202 {
		t.Fatalf("send=%d %s", r.status, r.body)
	}
	calls := 0
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Execute: func(_ context.Context, _ engine.Session, _ func(engine.Event) error) error { calls++; return nil }}
	if err := worker.Process(context.Background(), queue.jobs[0]); err == nil || calls != 0 {
		t.Fatal("paused turn executed")
	}
	r = h.request(http.MethodPut, path, map[string]any{"paused": false}, owner.AccessToken, nil, true)
	if r.status != 200 {
		t.Fatalf("resume=%d %s", r.status, r.body)
	}
	if err := worker.Process(context.Background(), queue.jobs[0]); err != nil || calls != 1 {
		t.Fatalf("resume: calls=%d err=%v", calls, err)
	}
	// Takeover keeps later input queued until the human returns control.
	control := base + "/bots/" + bot.Id.String() + "/control/"
	if r = h.request(http.MethodPost, control+"take", nil, owner.AccessToken, nil, true); r.status != 200 {
		t.Fatalf("take=%d %s", r.status, r.body)
	}
	r = h.request(http.MethodPost, base+"/conversations/"+conv.Id.String()+"/messages", map[string]any{"id": uuid.New(), "content": "After takeover"}, owner.AccessToken, nil, true)
	if r.status != 202 {
		t.Fatal(string(r.body))
	}
	if err := worker.Process(context.Background(), queue.jobs[1]); err == nil || calls != 1 {
		t.Fatal("turn ran during takeover")
	}
	if r = h.request(http.MethodPost, control+"release", nil, owner.AccessToken, nil, true); r.status != 200 {
		t.Fatal(string(r.body))
	}
	// Pause cancels an active turn, without replaying its external effects.
	started := make(chan struct{})
	worker.Execute = func(ctx context.Context, _ engine.Session, _ func(engine.Event) error) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	queue.interrupt = worker.Interrupt
	done := make(chan error, 1)
	go func() { done <- worker.Process(context.Background(), queue.jobs[1]) }()
	<-started
	r = h.request(http.MethodPut, path, map[string]any{"paused": true}, owner.AccessToken, nil, true)
	if r.status != 200 {
		t.Fatal(string(r.body))
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	worker.Execute = func(context.Context, engine.Session, func(engine.Event) error) error {
		t.Error("interrupted turn replayed")
		return nil
	}
	h.request(http.MethodPut, path, map[string]any{"paused": false}, owner.AccessToken, nil, true)
	if err := worker.Process(context.Background(), queue.jobs[1]); err != nil {
		t.Fatal(err)
	}
	queue.err = errors.New("queue down")
	if r = h.request(http.MethodPut, path, map[string]any{"paused": true}, owner.AccessToken, nil, true); r.status != 503 {
		t.Fatalf("queue failure=%d", r.status)
	}
	queue.err = nil
	other := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other", "slug": "other"}, owner.AccessToken, nil, true)
	var otherWS apigen.WorkspaceSummary
	other.decode(t, &otherWS)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		r = h.request(method, "/api/workspaces/"+otherWS.Id.String()+"/bots/"+bot.Id.String()+"/runtime", map[string]any{"paused": true}, owner.AccessToken, nil, true)
		if r.status != 404 {
			t.Fatalf("cross workspace=%d", r.status)
		}
	}
}

func TestPhase4SoftDenialSurvivesHistoryReload(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	base := "/api/workspaces/" + owner.ActiveWorkspace.Id.String()
	r := h.request(http.MethodPost, base+"/bots", map[string]any{"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "cli", "llmProvider": "agy", "llmModel": "fixture"}, owner.AccessToken, nil, true)
	var bot apigen.Bot
	r.decode(t, &bot)
	r = h.request(http.MethodPost, base+"/bots/"+bot.Id.String()+"/dm", nil, owner.AccessToken, nil, true)
	var conv apigen.ChatConversation
	r.decode(t, &conv)
	path := base + "/conversations/" + conv.Id.String() + "/messages"
	r = h.request(http.MethodPost, path, map[string]any{"id": uuid.New(), "content": "Run a denied fixture tool"}, owner.AccessToken, nil, true)
	if r.status != 202 {
		t.Fatal(string(r.body))
	}
	// agy 1.5.9 exits 0 and lists auto-denied tools only in its result record.
	fake := enginetest.CLI{Name: "agy", Stdout: `{"event":"result","result":{"status":"SUCCESS","denied_actions":[{"action":"command","display_name":"Bash"}]}}` + "\n", Stderr: "jetski: a tool was auto-denied\n"}
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Execute: fake.Run}
	if err := worker.Process(context.Background(), queue.jobs[0]); err != nil {
		t.Fatal(err)
	}
	r = h.request(http.MethodGet, path, nil, owner.AccessToken, nil, false)
	var history apigen.ChatHistory
	r.decode(t, &history)
	found := false
	for _, message := range history.Messages {
		if message.Kind == "system" && message.Content == "agy denied tools without asking: Bash" {
			found = true
		}
	}
	if !found {
		t.Fatalf("soft denial lost on reload: %s", r.body)
	}
}

func TestPhase4QueueOrderAcrossPrivateConversations(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	r := h.request(http.MethodPost, base+"/bots", map[string]any{"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "cli", "llmProvider": "openrouter", "llmModel": "openrouter/fixture"}, owner.AccessToken, nil, true)
	var bot apigen.Bot
	r.decode(t, &bot)
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member@example.com", "member", "Member", "CorrectHorse123!")
	var conversations []uuid.UUID
	for i, token := range []string{owner.AccessToken, member.AccessToken} {
		r = h.request(http.MethodPost, base+"/bots/"+bot.Id.String()+"/dm", nil, token, nil, true)
		var conversation apigen.ChatConversation
		r.decode(t, &conversation)
		conversations = append(conversations, conversation.Id)
		r = h.request(http.MethodPost, base+"/conversations/"+conversation.Id.String()+"/messages", map[string]any{"id": uuid.New(), "content": fmt.Sprintf("input %d", i)}, token, nil, true)
		if r.status != 202 {
			t.Fatal(string(r.body))
		}
	}
	started, release := make(chan struct{}), make(chan struct{})
	var active atomic.Int32
	var mu sync.Mutex
	var order []uuid.UUID
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Execute: func(_ context.Context, s engine.Session, emit func(engine.Event) error) error {
		if !active.CompareAndSwap(0, 1) {
			t.Error("two turns used one computer concurrently")
		}
		defer active.Store(0)
		mu.Lock()
		index := len(order)
		order = append(order, s.ConversationID)
		mu.Unlock()
		if len(s.Messages) != 1 || s.Messages[0].Content != fmt.Sprintf("input %d", index) {
			t.Error("private conversation context mixed")
		}
		if index == 0 {
			close(started)
			<-release
		}
		return emit(engine.Event{Kind: engine.EventKindMessage, Text: "fixture reply"})
	}}
	// Out-of-order notifications must wait for the first persisted input.
	if err := worker.Process(context.Background(), queue.jobs[1]); err == nil {
		t.Fatal("later notification skipped queued input")
	}
	first := make(chan error, 1)
	go func() { first <- worker.Process(context.Background(), queue.jobs[0]) }()
	<-started
	// A busy bot leaves the later turn queued for redelivery. It must not
	// occupy a dispatcher slot waiting for another turn to release its computer.
	busyErr := worker.Process(context.Background(), queue.jobs[1])
	close(release)
	if busyErr == nil {
		t.Fatal("busy bot admitted a second turn")
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(context.Background(), queue.jobs[1]); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != conversations[0] || order[1] != conversations[1] {
		t.Fatalf("order=%v", order)
	}
}

func TestPhase4ActionableFailureDoesNotExposePrivateCause(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	base := "/api/workspaces/" + owner.ActiveWorkspace.Id.String()
	r := h.request(http.MethodPost, base+"/bots", map[string]any{"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "cli", "llmProvider": "anthropic", "llmModel": "sonnet"}, owner.AccessToken, nil, true)
	var bot apigen.Bot
	r.decode(t, &bot)
	r = h.request(http.MethodPost, base+"/bots/"+bot.Id.String()+"/dm", nil, owner.AccessToken, nil, true)
	var conv apigen.ChatConversation
	r.decode(t, &conv)
	path := base + "/conversations/" + conv.Id.String() + "/messages"
	r = h.request(http.MethodPost, path, map[string]any{"id": uuid.New(), "content": "hi"}, owner.AccessToken, nil, true)
	if r.status != 202 {
		t.Fatal(string(r.body))
	}
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Execute: func(context.Context, engine.Session, func(engine.Event) error) error {
		return engine.WithFailureCode(engine.FailureCredentialsMissing, errors.New("fixture-private-credential"))
	}}
	if err := worker.Process(context.Background(), queue.jobs[0]); err != nil {
		t.Fatal(err)
	}
	r = h.request(http.MethodGet, path, nil, owner.AccessToken, nil, false)
	var history apigen.ChatHistory
	r.decode(t, &history)
	if len(history.Turns) != 1 || history.Turns[0].ErrorCode != engine.FailureCredentialsMissing {
		t.Fatalf("failure lost: %s", r.body)
	}
	if strings.Contains(string(r.body), "fixture-private-credential") || strings.Contains(h.logs.String(), "fixture-private-credential") {
		t.Fatal("private error exposed")
	}
}
