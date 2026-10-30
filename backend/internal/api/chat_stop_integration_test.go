package api_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/secretrequests"
	"github.com/google/uuid"
)

func TestStopPrivateChatTurn(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member@example.com", "member", "Member", "CorrectHorse123!")
	admin, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "admin@example.com", "admin", "Admin", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "viewer@example.com", "viewer", "Viewer", "CorrectHorse123!")
	r := h.request(http.MethodPost, base+"/bots", map[string]any{
		"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "api", "llmProvider": "openrouter", "llmModel": "fixture",
	}, owner.AccessToken, nil, true)
	var bot apigen.Bot
	r.decode(t, &bot)
	openDM := func(token string) string {
		t.Helper()
		r := h.request(http.MethodPost, base+"/bots/"+bot.Id.String()+"/dm", nil, token, nil, true)
		var conversation apigen.ChatConversation
		r.decode(t, &conversation)
		return "/conversations/" + conversation.Id.String()
	}
	memberDM, ownerDM := openDM(member.AccessToken), openDM(owner.AccessToken)
	for _, input := range []struct{ dm, token string }{
		{memberDM, member.AccessToken}, {memberDM, member.AccessToken}, {ownerDM, owner.AccessToken},
	} {
		r := h.request(http.MethodPost, base+input.dm+"/messages", map[string]any{"id": uuid.New(), "content": "Run fixture"}, input.token, nil, true)
		if r.status != http.StatusAccepted {
			t.Fatalf("send: %d %s", r.status, r.body)
		}
	}
	stopPath := func(dm string, turn uuid.UUID) string { return dm + "/turns/" + turn.String() + "/stop" }
	first := stopPath(memberDM, queue.jobs[0].TurnID)
	other := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other", "slug": "other"}, owner.AccessToken, nil, true)
	var otherWS apigen.WorkspaceSummary
	other.decode(t, &otherWS)
	for _, tc := range []struct {
		name, path, token string
		status            int
	}{
		{"anonymous", base + first, "", 401},
		{"viewer", base + first, viewer.AccessToken, 403},
		{"private from owner", base + first, owner.AccessToken, 404},
		{"private from admin", base + first, admin.AccessToken, 404},
		{"foreign workspace", "/api/workspaces/" + otherWS.Id.String() + stopPath(ownerDM, queue.jobs[2].TurnID), owner.AccessToken, 404},
		{"foreign turn", base + stopPath(memberDM, queue.jobs[2].TurnID), member.AccessToken, 404},
		{"unknown turn", base + stopPath(memberDM, uuid.New()), member.AccessToken, 404},
		{"nil turn", base + stopPath(memberDM, uuid.Nil), member.AccessToken, 400},
		{"malformed turn", base + memberDM + "/turns/invalid/stop", member.AccessToken, 400},
		{"queued turn", base + stopPath(memberDM, queue.jobs[1].TurnID), member.AccessToken, 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := h.request(http.MethodPost, tc.path, nil, tc.token, nil, true)
			if r.status != tc.status {
				t.Fatalf("stop: %d want %d: %s", r.status, tc.status, r.body)
			}
		})
	}

	testCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	stopped := make(chan error, 1)
	var active engine.Session
	var ran []uuid.UUID
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Execute: func(ctx context.Context, s engine.Session, _ func(engine.Event) error) error {
		ran = append(ran, s.TurnID)
		if s.TurnID != queue.jobs[0].TurnID {
			return nil
		}
		active = s
		close(started)
		<-ctx.Done()
		close(cancelled)
		select {
		case <-release:
		case <-testCtx.Done():
		}
		return ctx.Err()
	}}
	go func() { stopped <- worker.Process(testCtx, queue.jobs[0]) }()
	select {
	case <-started:
	case err := <-stopped:
		t.Fatalf("worker exited before starting: %v", err)
	case <-testCtx.Done():
		t.Fatal("worker did not start")
	}

	// An existing execution can be stopped even if queue transport is unavailable.
	queue.err = errors.New("private transport error")
	for range 2 {
		r := h.request(http.MethodPost, base+first, nil, member.AccessToken, nil, true)
		if r.status != http.StatusNoContent || len(r.body) != 0 {
			t.Fatalf("stop: %d %s", r.status, r.body)
		}
	}
	select {
	case <-cancelled:
		if testCtx.Err() != nil {
			t.Fatal("stop did not cancel the worker")
		}
	case <-testCtx.Done():
		t.Fatal("worker did not receive cancellation")
	}

	r = h.request(http.MethodGet, base+memberDM+"/messages", nil, member.AccessToken, nil, false)
	var pending apigen.ChatHistory
	r.decode(t, &pending)
	if len(pending.Turns) != 2 || pending.Turns[1].Status != "running" ||
		pending.Turns[1].StopRequested == nil || !*pending.Turns[1].StopRequested {
		t.Fatalf("stop was not visible during cleanup: %s", r.body)
	}
	if err := worker.Process(testCtx, queue.jobs[1]); err == nil {
		t.Fatal("queued turn started before cancellation cleanup")
	}
	_, err := secretrequests.CreateRequest(testCtx, h.pool, secretrequests.CreateInput{
		WorkspaceID: active.WorkspaceID, BotID: active.BotID, ConversationID: active.ConversationID, TurnID: active.TurnID,
		Kind: secretrequests.KindCLIEgress, AdapterID: "github_api", Reason: "Fixture request",
		ToolCallID: "fixture-call", EnvPlaceholderName: "GH_TOKEN", Now: h.clock.Now(),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("stop allowed a secret request to suspend the turn: %v", err)
	}
	close(release)
	select {
	case err := <-stopped:
		if err != nil || testCtx.Err() != nil {
			t.Fatalf("stop did not release the worker: %v (context: %v)", err, testCtx.Err())
		}
	case <-testCtx.Done():
		t.Fatal("worker did not stop")
	}

	r = h.request(http.MethodGet, base+memberDM+"/messages", nil, member.AccessToken, nil, false)
	var history apigen.ChatHistory
	r.decode(t, &history)
	if len(history.Turns) != 2 || history.Turns[0].Status != "queued" ||
		history.Turns[1].Id != queue.jobs[0].TurnID || history.Turns[1].Status != "interrupted" ||
		history.Turns[1].ErrorCode != "engine.interrupted" {
		t.Fatalf("unexpected chat history: %s", r.body)
	}
	for _, job := range queue.jobs {
		if err := worker.Process(testCtx, job); err != nil {
			t.Fatal(err)
		}
	}
	if len(ran) != 3 || ran[1] != queue.jobs[1].TurnID || ran[2] != queue.jobs[2].TurnID {
		t.Fatalf("cancelled turn replayed or other work was stopped: %v", ran)
	}
}
