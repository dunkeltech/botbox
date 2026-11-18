// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/budgets"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestParallelRequestsReserveGroupBudgetAndSettle(t *testing.T) {
	for _, scope := range []string{"group", "workspace", "bot"} {
		t.Run(scope, func(t *testing.T) { testParallelReservation(t, scope) })
	}
}

func testParallelReservation(t *testing.T, scope string) {
	t.Helper()
	h := newTestHarness(t, nil, harnessOpts{turnQueue: &fakeTurnQueue{}})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	a := createPiBot(t, h, base, owner.AccessToken, "Yoda", "yoda-seed")
	b := createPiBot(t, h, base, owner.AccessToken, "Nova", "nova-seed")
	c := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas-seed")
	limit := int64(10000)
	group := createGroup(t, h, base, owner.AccessToken, groupSettings{name: "Budget", lead: a.Id, turnLimit: 20, budget: 0, members: []any{groupBot(a.Id, "lead"), groupBot(b.Id, "reviewer"), groupBot(c.Id, "reviewer")}})
	if scope == "group" {
		if _, err := h.pool.Exec(context.Background(), "UPDATE conversations SET budget_limit_micros=$3 WHERE workspace_id=$1 AND id=$2", ws, group.Id, limit); err != nil {
			t.Fatal(err)
		}
	} else {
		body := map[string]any{"period": "month", "limitMicros": limit}
		if scope == "bot" {
			body["botId"] = a.Id
		}
		if r := h.request(http.MethodPut, base+"/budgets", body, owner.AccessToken, nil, true); r.status != 200 {
			t.Fatal(string(r.body))
		}
	}
	postGroup(t, h, base, owner.AccessToken, group.Id, "@Yoda @Nova @Atlas go")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := h.pool.Exec(ctx, "UPDATE bot_turns SET status='running' WHERE workspace_id=$1 AND conversation_id=$2", ws, group.Id); err != nil {
		t.Fatal(err)
	}
	response := h.request(http.MethodPut, base+"/model-prices", map[string]any{"provider": "openrouter", "model": "openrouter/fixture", "inputMicrosPerMillion": 1000000, "outputMicrosPerMillion": 1000000, "cacheMicrosPerMillion": 1000000}, owner.AccessToken, nil, true)
	if response.status != 200 {
		t.Fatal(string(response.body))
	}
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	defer close(release)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":10,"completion_tokens":10}}`)
	}))
	// Cancellation must release the upstream before Server.Close waits for it.
	defer func() { cancel(); upstream.Close() }()
	call := func(bot string) int {
		handler, err := llmproxy.NewScopedHandler(ws.String(), bot, func(context.Context, string, string) (*llmproxy.SecretConfig, error) {
			return &llmproxy.SecretConfig{UpstreamURL: upstream.URL, AuthKind: llmproxy.AuthKindBearer, SecretValue: "fixture-key"}, nil
		}, &llmproxy.Options{Admit: budgets.ProxyAdmission(h.pool, ws, uuid.MustParse(bot), h.clock.Now)})
		if err != nil {
			t.Error(err)
			return 0
		}
		req := httptest.NewRequest("POST", "/llm/openrouter/v1/chat/completions", strings.NewReader(`{"model":"openrouter/fixture","messages":[{"role":"user","content":"hi"}],"max_tokens":100}`)).WithContext(ctx)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	done := make(chan int, 2)
	go func() { done <- call(a.Id.String()) }()
	second, third := b.Id, c.Id
	if scope == "bot" {
		second, third = a.Id, a.Id
	}
	go func() { done <- call(second.String()) }()
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("budget lock serialized bounded requests")
		}
	}
	if got := call(third.String()); got != 429 {
		t.Fatalf("third request exceeded shared group budget: %d", got)
	}
	// Finishing requests releases their unused reservations.
	release <- struct{}{}
	release <- struct{}{}
	for range 2 {
		select {
		case got := <-done:
			if got != 200 {
				t.Errorf("request=%d", got)
			}
		case <-ctx.Done():
			t.Fatal("request did not finish")
		}
	}
	var cost, reservations int64
	if err := h.pool.QueryRow(ctx, "SELECT COALESCE(sum(cost_micros),0)::bigint,count(reserved_micros) FROM usage_records WHERE workspace_id=$1", ws).Scan(&cost, &reservations); err != nil {
		t.Fatal(err)
	}
	if cost != 40 || reservations != 0 {
		t.Fatalf("cost=%d reservations=%d", cost, reservations)
	}
}

func TestUncertainReservedRequestBlocksFurtherSpend(t *testing.T) {
	h := newTestHarness(t, nil, harnessOpts{})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	bot := createPiBot(t, h, base, owner.AccessToken, "Yoda", "yoda-seed")
	for path, body := range map[string]any{
		"/budgets":      map[string]any{"period": "month", "limitMicros": 10000},
		"/model-prices": map[string]any{"provider": "openrouter", "model": "openrouter/fixture", "inputMicrosPerMillion": 1000000, "outputMicrosPerMillion": 1000000, "cacheMicrosPerMillion": 1000000},
	} {
		if r := h.request(http.MethodPut, base+path, body, owner.AccessToken, nil, true); r.status != 200 {
			t.Fatal(string(r.body))
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	admit := budgets.ProxyAdmission(h.pool, ws, bot.Id, h.clock.Now)
	handler, err := llmproxy.NewScopedHandler(ws.String(), bot.Id.String(), func(context.Context, string, string) (*llmproxy.SecretConfig, error) {
		return &llmproxy.SecretConfig{UpstreamURL: "http://127.0.0.1:1", AuthKind: llmproxy.AuthKindBearer, SecretValue: "fixture-key"}, nil
	}, &llmproxy.Options{Admit: func(ctx context.Context, provider, model, path string) (func(llmproxy.Usage) error, error) {
		settle, err := admit(ctx, provider, model, path)
		cancel()
		return settle, err
	}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/llm/openrouter/v1/chat/completions", strings.NewReader(`{"model":"openrouter/fixture","max_tokens":100}`)).WithContext(ctx)
	handler.ServeHTTP(httptest.NewRecorder(), req)
	var reserved, unknown int
	if err := h.pool.QueryRow(context.Background(), "SELECT count(reserved_micros),count(*) FILTER(WHERE cost_micros IS NULL) FROM usage_records WHERE workspace_id=$1", ws).Scan(&reserved, &unknown); err != nil {
		t.Fatal(err)
	}
	if reserved != 0 || unknown != 1 {
		t.Fatalf("reserved=%d unknown=%d", reserved, unknown)
	}
	if err := budgets.CheckSerialized(context.Background(), h.pool, ws, bot.Id, h.clock.Now()); err == nil {
		t.Fatal("unknown cost reopened the workspace budget")
	}
}

func TestGroupBudgetCheckWaitsForSettlementAndCleansUpCanceledClaim(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	bot := createPiBot(t, h, base, owner.AccessToken, "Yoda", "yoda-seed")
	g := createGroup(t, h, base, owner.AccessToken, groupSettings{name: "Pending", lead: bot.Id, turnLimit: 20, budget: 10000, members: []any{groupBot(bot.Id, "lead")}})
	postGroup(t, h, base, owner.AccessToken, g.Id, "@Yoda go")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := h.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	q := db.New(conn)
	if err = q.LockWorkspaceSpend(ctx, ws.String()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.UnlockWorkspaceSpend(context.Background(), ws.String()) }()
	if err = q.RecordTurnUsage(ctx, db.RecordTurnUsageParams{WorkspaceID: pgtype.UUID{Bytes: ws, Valid: true}, BotID: pgtype.UUID{Bytes: bot.Id, Valid: true}, TurnID: pgtype.UUID{Bytes: queue.jobs[0].TurnID, Valid: true}, RequestID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, Source: "proxy_meter", Model: "openrouter/fixture", CreatedAt: pgtype.Timestamptz{Time: h.clock.Now(), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	worker := &engine.Worker{Pool: h.pool, Execute: func(context.Context, engine.Session, func(engine.Event) error) error {
		return errors.New("model must not execute")
	}}
	done := make(chan error, 1)
	go func() { done <- worker.Process(ctx, queue.jobs[0]) }()
	// Observe PostgreSQL's lock wait, rather than guessing with a sleep. Without
	// the lock the provisional row would incorrectly pause the group as unknown.
	for {
		select {
		case err := <-done:
			t.Fatalf("did not wait for pending settlement: %v", err)
		default:
		}
		var waiting bool
		if err = h.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted)").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	cancel()
	if err = <-done; err == nil {
		t.Fatal("canceled claim succeeded")
	}
	turn, err := h.queries.GetBotTurn(context.Background(), db.GetBotTurnParams{WorkspaceID: pgtype.UUID{Bytes: ws, Valid: true}, ID: pgtype.UUID{Bytes: queue.jobs[0].TurnID, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Status != "failed" {
		t.Fatalf("canceled setup remained %s", turn.Status)
	}
	if current := getGroup(t, h, base, owner.AccessToken, g.Id); current.Paused {
		t.Fatal("pending request falsely paused the group")
	}
}
