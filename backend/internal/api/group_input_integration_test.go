package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
)

func TestGroupInputCollectsRepliesAndResumesOnce(t *testing.T) {
	ctx := context.Background()
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue, chatLimit: 50})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	yoda := createAPIBot(t, h, base, owner.AccessToken, "Yoda", "yoda")
	mace := createAPIBot(t, h, base, owner.AccessToken, "Mace", "mace")
	babu := createAPIBot(t, h, base, owner.AccessToken, "Babu", "babu")
	group := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name: "Holiday discussion", lead: mace.Id, turnLimit: 20, botTalk: true,
		members: []any{groupBot(yoda.Id, ""), groupBot(mace.Id, ""), groupBot(babu.Id, "")},
	})
	path := base + "/groups/" + group.Id.String() + "/messages"
	const question = "What destinations are easy to reach by train?"
	var continued, unavailableCalls int
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Enqueue: queue.Enqueue, Execute: func(_ context.Context, session engine.Session, emit func(engine.Event) error) error {
		var reply string
		switch session.BotID {
		case yoda.Id:
			if session.ContinuationPrompt == "" {
				if strings.Contains(session.Messages[0].Content, "prefer a direct connection") &&
					(!strings.Contains(session.Preface, "Mace: replied") || !strings.Contains(session.Preface, "Babu: waiting")) {
					t.Error("requester did not see current input progress")
				}
				reply = question + "\nRequest required input: [\"Mace\", \"Babu\", \"Mace\"]"
			} else {
				continued++
				for _, want := range []string{question, "Reply from Mace:", "A direct train is preferable.", "Babu: No reply received.", "Required input is missing."} {
					if !strings.Contains(session.ContinuationPrompt, want) {
						t.Errorf("continuation missing %q", want)
					}
				}
				reply = "My shortlist incorporates @Mace's feedback."
			}
		case mace.Id:
			if !strings.Contains(session.ContinuationPrompt, question) {
				t.Error("recipient did not receive the question")
			}
			reply = "A direct train is preferable.\nRequest input: [\"Yoda\"]"
		case babu.Id:
			unavailableCalls++
		}
		return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: reply})
	}}
	process := func(index int) {
		t.Helper()
		job := queue.jobs[index]
		queue.jobs = append(queue.jobs[:index], queue.jobs[index+1:]...)
		if err := worker.Process(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	collect := func() {
		t.Helper()
		if err := worker.CollectGroupInputs(ctx); err != nil {
			t.Fatal(err)
		}
	}
	progress := func() *apigen.GroupInputRequest {
		t.Helper()
		history := groupHistory(t, h, base, owner.AccessToken, group.Id)
		for _, message := range history.Messages {
			if message.Content == question && message.InputRequest != nil {
				return message.InputRequest
			}
		}
		t.Fatal("input request missing from history")
		return nil
	}

	postGroup(t, h, base, owner.AccessToken, group.Id, "@Yoda discuss holiday destinations")
	process(0)
	if len(queue.jobs) != 2 || queue.jobs[0].BotID != mace.Id || queue.jobs[1].BotID != babu.Id {
		t.Fatalf("fan-out jobs=%+v", queue.jobs)
	}
	got := progress()
	if got.State != "waiting" || !got.Required || len(got.Recipients) != 2 || got.Recipients[0].State != "waiting" {
		t.Fatalf("initial progress=%+v", got)
	}

	process(0)
	collect()
	if len(queue.jobs) != 1 || queue.jobs[0].BotID != babu.Id {
		t.Fatalf("first reply queued a new turn: %+v", queue.jobs)
	}
	got = progress()
	if got.Recipients[0].Name != "Babu" || got.Recipients[0].State != "waiting" || got.Recipients[1].State != "replied" {
		t.Fatalf("intermediate progress=%+v", got)
	}
	// A new human message can steer the conversation. Another request by
	// the same owner while this one is outstanding must reuse the round.
	postGroup(t, h, base, owner.AccessToken, group.Id, "@Yoda prefer a direct connection")
	process(1)
	if len(queue.jobs) != 1 || queue.jobs[0].BotID != babu.Id {
		t.Fatalf("duplicate round queued work: %+v", queue.jobs)
	}

	if r := h.request(http.MethodGet, path, nil, "", nil, true); r.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated history=%d", r.status)
	}
	other, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "other@example.com", "member", "Other", "CorrectHorse123!")
	if r := h.request(http.MethodGet, path, nil, other.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("nonmember history=%d", r.status)
	}
	var otherWS apigen.WorkspaceSummary
	created := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other", "slug": "other"}, owner.AccessToken, nil, true)
	created.decode(t, &otherWS)
	cross := "/api/workspaces/" + otherWS.Id.String() + "/groups/" + group.Id.String() + "/messages"
	if r := h.request(http.MethodGet, cross, nil, owner.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("cross-workspace history=%d", r.status)
	}

	h.clock.Advance(5 * time.Minute)
	queue.err = errors.New("queue unavailable")
	if err := worker.CollectGroupInputs(ctx); !errors.Is(err, queue.err) {
		t.Fatalf("collect during outage: %v", err)
	}
	if got = progress(); got.State != "waiting" || len(queue.jobs) != 1 {
		t.Fatalf("outage persisted a continuation: progress=%+v jobs=%+v", got, queue.jobs)
	}
	queue.err = nil
	errorsOut := make(chan error, 2)
	for range 2 {
		go func() { errorsOut <- worker.CollectGroupInputs(ctx) }()
	}
	for range 2 {
		if err := <-errorsOut; err != nil {
			t.Fatal(err)
		}
	}
	if len(queue.jobs) != 2 || queue.jobs[1].BotID != yoda.Id {
		t.Fatalf("deadline did not queue exactly one continuation: %+v", queue.jobs)
	}
	process(0)
	process(0)
	collect()
	if continued != 1 || unavailableCalls != 0 || len(queue.jobs) != 0 {
		t.Fatalf("continued=%d expired recipient calls=%d jobs=%+v", continued, unavailableCalls, queue.jobs)
	}
	got = progress()
	if got.State != "collected" || got.Recipients[0].State != "unavailable" || got.Recipients[1].State != "replied" {
		t.Fatalf("final progress=%+v", got)
	}
}

func TestGroupInputHonorsGroupSettingsAndTurnLimit(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue, chatLimit: 50})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	base := "/api/workspaces/" + owner.ActiveWorkspace.Id.String()
	yoda := createAPIBot(t, h, base, owner.AccessToken, "Yoda", "yoda")
	mace := createAPIBot(t, h, base, owner.AccessToken, "Mace", "mace")
	members := []any{groupBot(yoda.Id, ""), groupBot(mace.Id, "")}
	var calls int
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Enqueue: queue.Enqueue, Execute: func(_ context.Context, _ engine.Session, emit func(engine.Event) error) error {
		calls++
		return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "Any ideas?\nRequest input: [\"Mace\"]"})
	}}
	for _, tc := range []struct {
		name     string
		limit    int
		botTalk  bool
		turnsOut int
	}{
		{name: "quiet", limit: 20},
		{name: "no room to collect", limit: 2, botTalk: true},
		{name: "disable after scheduling", limit: 20, botTalk: true, turnsOut: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := createGroup(t, h, base, owner.AccessToken, groupSettings{
				name: tc.name, lead: yoda.Id, turnLimit: tc.limit, botTalk: tc.botTalk, members: members,
			})
			postGroup(t, h, base, owner.AccessToken, group.Id, "Ask Mace where to go")
			before := calls
			job := queue.jobs[0]
			queue.jobs = nil
			if err := worker.Process(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			if len(queue.jobs) != tc.turnsOut {
				t.Fatalf("queued jobs=%+v, want %d", queue.jobs, tc.turnsOut)
			}
			if tc.turnsOut > 0 {
				update := newGroupBody(group.Id, tc.name, yoda.Id, members)
				delete(update, "id")
				for _, enabled := range []bool{false, true} {
					update["botTalk"] = enabled
					if r := h.request(http.MethodPatch, base+"/groups/"+group.Id.String(), update, owner.AccessToken, nil, true); r.status != http.StatusOK {
						t.Fatalf("update bot talk=%d %s", r.status, r.body)
					}
				}
			}
			drainTurns(t, worker, queue, 2)
			if err := worker.CollectGroupInputs(context.Background()); err != nil {
				t.Fatal(err)
			}
			if calls != before+1 || len(queue.jobs) != 0 {
				t.Fatalf("model calls=%d jobs=%+v", calls-before, queue.jobs)
			}
		})
	}
}
