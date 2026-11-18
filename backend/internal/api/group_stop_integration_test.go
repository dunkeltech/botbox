// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/google/uuid"
)

func TestStopGroupTurnLeavesOtherBotsRunning(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member@example.com", "member", "Member", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "viewer@example.com", "viewer", "Viewer", "CorrectHorse123!")
	admin, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "admin@example.com", "admin", "Admin", "CorrectHorse123!")
	atlas := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas-seed")
	nova := createPiBot(t, h, base, owner.AccessToken, "Nova", "nova-seed")
	group := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name: "Shared", lead: atlas.Id, turnLimit: 20,
		members: []any{groupBot(atlas.Id, "lead"), groupBot(nova.Id, "reviewer"), groupUser(member.User.Id, "writer"), groupUser(viewer.User.Id, "reader")},
	})
	other := createGroup(t, h, base, owner.AccessToken, groupSettings{name: "Other group", lead: atlas.Id, turnLimit: 20, members: []any{groupBot(atlas.Id, "lead")}})
	postGroup(t, h, base, owner.AccessToken, group.Id, "@Atlas @Nova work")
	postGroup(t, h, base, owner.AccessToken, group.Id, "@Atlas follow up")
	if len(queue.jobs) != 3 {
		t.Fatalf("expected two initial turns and a follow-up, got %d jobs", len(queue.jobs))
	}
	atlasJob, novaJob := queue.jobs[0], queue.jobs[1]
	if atlasJob.BotID != atlas.Id {
		atlasJob, novaJob = novaJob, atlasJob
	}
	path := "/groups/" + group.Id.String() + "/turns/"
	stop := path + atlasJob.TurnID.String() + "/stop"
	r := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other", "slug": "other"}, owner.AccessToken, nil, true)
	var otherWS apigen.WorkspaceSummary
	r.decode(t, &otherWS)
	for _, tc := range []struct {
		name, url, token string
		status           int
	}{
		{"anonymous", base + stop, "", 401},
		{"viewer", base + stop, viewer.AccessToken, 403},
		{"admin outside group", base + stop, admin.AccessToken, 404},
		{"different workspace", "/api/workspaces/" + otherWS.Id.String() + stop, owner.AccessToken, 404},
		{"different group", base + "/groups/" + other.Id.String() + "/turns/" + atlasJob.TurnID.String() + "/stop", owner.AccessToken, 404},
		{"invalid turn", base + path + "invalid/stop", member.AccessToken, 400},
		{"unknown turn", base + path + uuid.NewString() + "/stop", member.AccessToken, 404},
		{"queued turn", base + path + queue.jobs[2].TurnID.String() + "/stop", owner.AccessToken, 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := h.request(http.MethodPost, tc.url, nil, tc.token, nil, true)
			if r.status != tc.status {
				t.Fatalf("stop=%d want=%d %s", r.status, tc.status, r.body)
			}
		})
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	started, finishNova := make(chan struct{}, 2), make(chan struct{})
	worker := &engine.Worker{Pool: h.pool, Execute: func(ctx context.Context, s engine.Session, _ func(engine.Event) error) error {
		if s.TurnID == queue.jobs[2].TurnID {
			return nil
		}
		started <- struct{}{}
		if s.BotID == nova.Id {
			select {
			case <-finishNova:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	atlasDone, novaDone := make(chan error, 1), make(chan error, 1)
	go func() { atlasDone <- worker.Process(ctx, atlasJob) }()
	go func() { novaDone <- worker.Process(ctx, novaJob) }()
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("both bots did not start")
		}
	}
	for range 2 {
		r = h.request(http.MethodPost, base+stop, nil, member.AccessToken, nil, true)
		if r.status != http.StatusNoContent || len(r.body) != 0 {
			t.Fatalf("stop=%d %s", r.status, r.body)
		}
	}
	waitForGroupTurn(t, ctx, atlasDone)
	history := groupHistory(t, h, base, member.AccessToken, group.Id)
	states := map[uuid.UUID]apigen.ChatTurn{}
	for _, turn := range history.Turns {
		states[turn.Id] = turn
	}
	if states[atlasJob.TurnID].Status != "interrupted" || states[atlasJob.TurnID].ErrorCode != "engine.interrupted" ||
		states[novaJob.TurnID].Status != "running" || states[queue.jobs[2].TurnID].Status != "queued" {
		t.Fatalf("stop affected the wrong work: %+v", history.Turns)
	}
	if err := worker.Process(ctx, queue.jobs[2]); err != nil {
		t.Fatalf("follow-up could not run: %v", err)
	}
	close(finishNova)
	waitForGroupTurn(t, ctx, novaDone)
}

func TestGroupReplyFinalization(t *testing.T) {
	t.Run("stop prevents handoff", func(t *testing.T) {
		checkGroupReplyFinalization(t, nil, "interrupted", 0)
	})
	t.Run("failed handoff keeps mentions", func(t *testing.T) {
		checkGroupReplyFinalization(t, errors.New("queue unavailable"), "failed", 1)
	})
}

func checkGroupReplyFinalization(t *testing.T, queueErr error, status apigen.ChatTurnStatus, notices int) {
	t.Helper()
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	base := "/api/workspaces/" + owner.ActiveWorkspace.Id.String()
	atlas := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas-seed")
	nova := createPiBot(t, h, base, owner.AccessToken, "Nova", "nova-seed")
	group := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name: "Handoff", lead: atlas.Id, botTalk: true, turnLimit: 20, members: []any{groupBot(atlas.Id, "lead"), groupBot(nova.Id, "reviewer")},
	})
	postGroup(t, h, base, owner.AccessToken, group.Id, "@Atlas prepare")
	if len(queue.jobs) != 1 {
		t.Fatalf("expected one initial turn, got %d jobs", len(queue.jobs))
	}
	job := queue.jobs[0]
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	finalizing, release := make(chan struct{}), make(chan struct{})
	modelFinished := false
	worker := &engine.Worker{
		Pool: h.pool, Enqueue: queue.Enqueue,
		Now: func() time.Time {
			if modelFinished {
				// Pause cleanup after the runner has returned a successful reply.
				modelFinished = false
				close(finalizing)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			return h.clock.Now()
		},
		Execute: func(_ context.Context, _ engine.Session, emit func(engine.Event) error) error {
			modelFinished = true
			return emit(engine.Event{Kind: engine.EventKindMessage, Text: "@Owner, Nova can review this.\nRequest input: [\"Nova\"]"})
		},
	}
	done := make(chan error, 1)
	go func() { done <- worker.Process(ctx, job) }()
	select {
	case <-finalizing:
	case <-ctx.Done():
		t.Fatal("worker did not reach cleanup")
	}
	if queueErr == nil {
		r := h.request(http.MethodPost, base+"/groups/"+group.Id.String()+"/turns/"+job.TurnID.String()+"/stop", nil, owner.AccessToken, nil, true)
		if r.status != http.StatusNoContent {
			t.Fatalf("stop=%d %s", r.status, r.body)
		}
	}
	queue.err = queueErr
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, queueErr) {
			t.Fatalf("turn error = %v, want %v", err, queueErr)
		}
	case <-ctx.Done():
		t.Fatal("turn did not finish")
	}
	history := groupHistory(t, h, base, owner.AccessToken, group.Id)
	if len(queue.jobs) != 1 || len(history.Turns) != 1 || history.Turns[0].Status != status {
		t.Fatalf("unexpected handoff state: %d jobs, %+v", len(queue.jobs), history.Turns)
	}
	inbox := listNotes(t, h, base+"/notifications", owner.AccessToken)
	if len(inbox.Notifications) != notices {
		t.Fatalf("got %d notices, want %d", len(inbox.Notifications), notices)
	}
}

func waitForGroupTurn(t *testing.T, ctx context.Context, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil || ctx.Err() != nil {
			t.Fatalf("turn did not finish: %v (context: %v)", err, ctx.Err())
		}
	case <-ctx.Done():
		t.Fatal("turn did not finish")
	}
}
