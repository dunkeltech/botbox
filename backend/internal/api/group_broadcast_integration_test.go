package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/google/uuid"
)

func TestGroupBroadcastRoutesOnlyMembersAndRefreshesRoles(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue, chatLimit: 50})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	lead := createPiBot(t, h, base, owner.AccessToken, "Yoda", "yoda")
	other := createPiBot(t, h, base, owner.AccessToken, "Mace Windu", "mace")
	createPiBot(t, h, base, owner.AccessToken, "Outsider", "outsider")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "viewer@example.com", "viewer", "Viewer", "CorrectHorse123!")
	group := createGroup(t, h, base, owner.AccessToken, groupSettings{name: "Discussion", lead: lead.Id, botTalk: true, turnLimit: 20, members: []any{groupBot(lead.Id, ""), groupBot(other.Id, ""), groupUser(viewer.User.Id, "")}})
	path := base + "/groups/" + group.Id.String() + "/messages"
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {viewer.AccessToken, 403}} {
		if r := h.request(http.MethodPost, path, map[string]any{"id": uuid.New(), "content": "@all discuss"}, tc.token, nil, true); r.status != tc.status {
			t.Fatalf("broadcast authorization=%d want=%d", r.status, tc.status)
		}
	}
	res := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other", "slug": "other"}, owner.AccessToken, nil, true)
	var otherWS apigen.WorkspaceSummary
	res.decode(t, &otherWS)
	crossPath := "/api/workspaces/" + otherWS.Id.String() + "/groups/" + group.Id.String() + "/messages"
	if r := h.request(http.MethodPost, crossPath, map[string]any{"id": uuid.New(), "content": "@all"}, owner.AccessToken, nil, true); r.status != 404 {
		t.Fatalf("cross-workspace broadcast=%d", r.status)
	}
	if len(queue.jobs) != 0 {
		t.Fatal("unauthorized broadcast queued a bot")
	}
	role := "Review proposals for GDPR compliance and suggest privacy-preserving alternatives."
	for _, bot := range []apigen.Bot{lead, other} {
		if r := h.request(http.MethodPatch, base+"/bots/"+bot.Id.String(), map[string]any{"description": role}, owner.AccessToken, nil, true); r.status != 200 {
			t.Fatalf("set global role=%d %s", r.status, r.body)
		}
	}
	postGroup(t, h, base, owner.AccessToken, group.Id, "Please discuss the issue")
	if len(queue.jobs) != 1 || queue.jobs[0].BotID != lead.Id {
		t.Fatalf("initial jobs=%v", queue.jobs)
	}
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Enqueue: queue.Enqueue, Execute: func(_ context.Context, s engine.Session, emit func(engine.Event) error) error {
		if s.Bot.Description != role || !strings.Contains(s.Preface, role) || !strings.Contains(s.Preface, "Group lead: Yoda") || !strings.Contains(s.Preface, "You are "+s.Bot.Name) {
			t.Errorf("missing role or identity: bot=%+v preface=%s", s.Bot.Name, s.Preface)
		}
		return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "Please review the issue.\nRequest input: [\"@all\"]"})
	}}
	job := queue.jobs[0]
	queue.jobs = nil
	if err := worker.Process(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if len(queue.jobs) != 1 || queue.jobs[0].BotID != other.Id {
		t.Fatalf("lead broadcast jobs=%v", queue.jobs)
	}
	// Role edits must be visible when the queued turn is claimed.
	role = "Keep the system architecture understandable."
	if r := h.request(http.MethodPatch, base+"/bots/"+other.Id.String(), map[string]any{"description": role}, owner.AccessToken, nil, true); r.status != 200 {
		t.Fatalf("edit role=%d", r.status)
	}
	worker.Execute = func(_ context.Context, s engine.Session, _ func(engine.Event) error) error {
		if (s.BotID == other.Id && s.Bot.Description != role) || !strings.Contains(s.Preface, role) || !strings.Contains(s.Preface, "You are "+s.Bot.Name) {
			t.Errorf("stale role: %q preface=%s", s.Bot.Description, s.Preface)
		}
		return nil
	}
	drainTurns(t, worker, queue, 2)
	if err := worker.CollectGroupInputs(context.Background()); err != nil {
		t.Fatal(err)
	}
	drainTurns(t, worker, queue, 2)
	postGroup(t, h, base, owner.AccessToken, group.Id, "@all @Yoda @all contribute")
	if len(queue.jobs) != 2 || queue.jobs[0].BotID == queue.jobs[1].BotID {
		t.Fatalf("human broadcast jobs=%v", queue.jobs)
	}
	drainTurns(t, worker, queue, 3)
}
