// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestBotTurnTimeout(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	create := func(minutes any) apiResponse {
		body := map[string]any{
			"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "cli", "llmProvider": "openrouter", "llmModel": "openrouter/fixture",
		}
		if minutes != nil {
			body["turnTimeoutMinutes"] = minutes
		}
		return h.request(http.MethodPost, base+"/bots", body, owner.AccessToken, nil, true)
	}
	assertTimeout := func(r apiResponse, status int, timeout int32) apigen.Bot {
		t.Helper()
		if r.status != status {
			t.Fatalf("status = %d, want %d: %s", r.status, status, r.body)
		}
		var bot apigen.Bot
		r.decode(t, &bot)
		if bot.TurnTimeoutMinutes != timeout {
			t.Fatalf("turn timeout = %d, want %d", bot.TurnTimeoutMinutes, timeout)
		}
		return bot
	}
	assertTimeout(create(nil), http.StatusCreated, 10)
	bot := assertTimeout(create(120), http.StatusCreated, 120)
	botPath := base + "/bots/" + bot.Id.String()
	patch := func(body map[string]any, token string) apiResponse {
		return h.request(http.MethodPatch, botPath, body, token, nil, true)
	}
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"below minimum", 0}, {"above maximum", 121}, {"out of integer range", int64(1) << 32},
		{"fractional", 12.5}, {"wrong type", "30"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if r := create(tc.value); r.status != http.StatusBadRequest {
				t.Errorf("invalid create status = %d: %s", r.status, r.body)
			}
			if r := patch(map[string]any{"turnTimeoutMinutes": tc.value}, owner.AccessToken); r.status != http.StatusBadRequest {
				t.Errorf("invalid update status = %d: %s", r.status, r.body)
			}
		})
	}
	assertTimeout(patch(map[string]any{"name": "Renamed"}, owner.AccessToken), http.StatusOK, 120)
	assertTimeout(patch(map[string]any{"turnTimeoutMinutes": 1}, owner.AccessToken), http.StatusOK, 1)

	if r := patch(map[string]any{"turnTimeoutMinutes": 60}, ""); r.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated update = %d: %s", r.status, r.body)
	}
	r := h.request(http.MethodPost, botPath+"/dm", nil, owner.AccessToken, nil, true)
	var dm apigen.ChatConversation
	r.decode(t, &dm)
	messagePath := base + "/conversations/" + dm.Id.String() + "/messages"
	queueTurn := func() uuid.UUID {
		t.Helper()
		r := h.request(http.MethodPost, messagePath, map[string]any{"id": uuid.New(), "content": "Fake turn"}, owner.AccessToken, nil, true)
		if r.status != http.StatusAccepted {
			t.Fatalf("queue turn = %d: %s", r.status, r.body)
		}
		return queue.jobs[len(queue.jobs)-1].TurnID
	}
	first := queueTurn()
	assertTimeout(patch(map[string]any{"turnTimeoutMinutes": 30}, owner.AccessToken), http.StatusOK, 30)
	assertTimeout(h.request(http.MethodGet, botPath, nil, owner.AccessToken, nil, false), http.StatusOK, 30)
	claimTime := h.clock.Now()
	workspaceID := pgtype.UUID{Bytes: ws, Valid: true}
	botID := pgtype.UUID{Bytes: bot.Id, Valid: true}
	getTurn := func(ctx context.Context, id uuid.UUID) (db.BotTurn, error) {
		return h.queries.GetBotTurn(ctx, db.GetBotTurnParams{WorkspaceID: workspaceID, ID: pgtype.UUID{Bytes: id, Valid: true}})
	}
	expire := func(ctx context.Context, elapsed time.Duration) error {
		return h.queries.ExpireStaleBotTurns(ctx, db.ExpireStaleBotTurnsParams{
			WorkspaceID: workspaceID, BotID: botID, AsOf: pgtype.Timestamptz{Time: claimTime.Add(elapsed), Valid: true},
		})
	}

	// A deadline between the old cutoff and the configured limit must reach the runner.
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Minute)
	defer cancel()
	parentDeadline, _ := ctx.Deadline()
	var runnerDeadline time.Time
	var running db.BotTurn
	var runnerErr error
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Execute: func(ctx context.Context, _ engine.Session, _ func(engine.Event) error) error {
		runnerDeadline, _ = ctx.Deadline()
		if runnerErr = expire(ctx, 21*time.Minute); runnerErr != nil {
			return runnerErr
		}
		running, runnerErr = getTurn(ctx, first)
		if runnerErr != nil {
			return runnerErr
		}
		return context.DeadlineExceeded
	}}
	if err := worker.Process(ctx, queue.jobs[0]); err != nil {
		t.Fatal(err)
	}
	if runnerErr != nil || !runnerDeadline.Equal(parentDeadline) || running.Status != "running" ||
		!running.RunDeadlineAt.Valid || !running.RunDeadlineAt.Time.Equal(claimTime.Add(30*time.Minute)) {
		t.Fatalf("runner deadline = %s, parent = %s, running turn = %+v, error = %v", runnerDeadline, parentDeadline, running, runnerErr)
	}
	if turn, err := getTurn(ctx, first); err != nil || turn.Status != "interrupted" || turn.ErrorCode != "engine.interrupted" {
		t.Fatalf("expired execution = %+v, error = %v", turn, err)
	}

	second := queueTurn()
	if err := h.queries.StartBotTurn(ctx, db.StartBotTurnParams{
		WorkspaceID: workspaceID, ID: pgtype.UUID{Bytes: second, Valid: true}, AsOf: pgtype.Timestamptz{Time: claimTime, Valid: true},
		RunDeadlineAt: running.RunDeadlineAt,
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		elapsed      time.Duration
		status, code string
	}{{34 * time.Minute, "running", ""}, {36 * time.Minute, "interrupted", "engine.worker_lost"}} {
		if err := expire(ctx, tc.elapsed); err != nil {
			t.Fatal(err)
		}
		if turn, err := getTurn(ctx, second); err != nil || turn.Status != tc.status || turn.ErrorCode != tc.code {
			t.Fatalf("stale turn at %s = %+v, error = %v", tc.elapsed, turn, err)
		}
	}
}
