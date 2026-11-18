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

	"github.com/dunkeltech/botbox/backend/internal/budgets"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestPhase4ProxySettlementAndKillSwitch(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		cost       int64
	}{
		{"priced", "data: {\"usage\":{\"prompt_tokens\":70,\"completion_tokens\":40}}\n\ndata: [DONE]\n\n", 429, 110},
		{"provider cost", "data: {\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"cost\":0.00012}}\n\ndata: [DONE]\n\n", 429, 120},
		{"truncated", "data: {\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n", 503, -1},
		{"missing usage", "data: [DONE]\n\n", 503, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queue := &fakeTurnQueue{}
			h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
			owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
			ws := owner.ActiveWorkspace.Id
			base := "/api/workspaces/" + ws.String()
			r := h.request(http.MethodPost, base+"/bots", map[string]any{"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "cli", "llmProvider": "codex", "llmModel": "fixture"}, owner.AccessToken, nil, true)
			var bot apigen.Bot
			r.decode(t, &bot)
			for path, body := range map[string]any{
				"/budgets":      map[string]any{"botId": bot.Id, "period": "day", "limitMicros": 100},
				"/model-prices": map[string]any{"provider": "openai", "model": "fixture", "inputMicrosPerMillion": 1000000, "outputMicrosPerMillion": 1000000, "cacheMicrosPerMillion": 1000000},
			} {
				if r = h.request(http.MethodPut, base+path, body, owner.AccessToken, nil, true); r.status != 200 {
					t.Fatal(string(r.body))
				}
			}
			r = h.request(http.MethodPost, base+"/bots/"+bot.Id.String()+"/dm", nil, owner.AccessToken, nil, true)
			var conv apigen.ChatConversation
			r.decode(t, &conv)
			r = h.request(http.MethodPost, base+"/conversations/"+conv.Id.String()+"/messages", map[string]any{"id": uuid.New(), "content": "Meter each request"}, owner.AccessToken, nil, true)
			if r.status != 202 {
				t.Fatal(string(r.body))
			}
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "Bearer fixture-outside-key" {
					t.Error("credential injection missing")
				}
				if r.Header.Get("X-Botbox-Bot-ID") != "" {
					t.Error("untrusted identity forwarded")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			proxy, err := llmproxy.NewScopedHandler(ws.String(), bot.Id.String(), func(context.Context, string, string) (*llmproxy.SecretConfig, error) {
				return &llmproxy.SecretConfig{UpstreamURL: upstream.URL, AuthKind: llmproxy.AuthKindBearer, SecretValue: "fixture-outside-key"}, nil
			}, &llmproxy.Options{Admit: budgets.ProxyAdmission(h.pool, ws, bot.Id, h.clock.Now)})
			if err != nil {
				t.Fatal(err)
			}
			worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Execute: func(ctx context.Context, _ engine.Session, emit func(engine.Event) error) error {
				for i, want := range []int{200, tc.status} {
					req := httptest.NewRequest("POST", "/llm/openai/v1/chat/completions", strings.NewReader(`{"model":"fixture","stream":true}`)).WithContext(ctx)
					req.Header.Set("X-Botbox-Bot-ID", uuid.NewString())
					rec := httptest.NewRecorder()
					proxy.ServeHTTP(rec, req)
					if rec.Code != want {
						t.Errorf("request %d=%d want=%d body=%s", i, rec.Code, want, rec.Body.String())
					}
				}
				// A CLI aggregate must not double charge already metered provider requests.
				return emit(engine.Event{Kind: engine.EventKindUsage, InputTokens: 999, OutputTokens: 999})
			}}
			if err = worker.Process(context.Background(), queue.jobs[0]); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("upstream calls=%d", calls)
			}
			var count int
			var cost pgtype.Int8
			if err = h.pool.QueryRow(context.Background(), "SELECT count(*),sum(cost_micros)::bigint FROM usage_records WHERE workspace_id=$1 AND bot_id=$2", ws, bot.Id).Scan(&count, &cost); err != nil {
				t.Fatal(err)
			}
			if count != 1 || tc.cost >= 0 && (!cost.Valid || cost.Int64 != tc.cost) || tc.cost < 0 && cost.Valid {
				t.Fatalf("records=%d cost=%+v", count, cost)
			}
			approvals, err := h.queries.ListBudgetApprovals(context.Background(), pgtype.UUID{Bytes: ws, Valid: true})
			if err != nil {
				t.Fatal(err)
			}
			if tc.status == 429 && len(approvals) != 1 {
				t.Fatalf("approvals=%d", len(approvals))
			}
			// Monetary limits fail closed on unfinished proxy accounting, including
			// workspace-wide limits that cover another bot's usage.
			if tc.cost < 0 {
				err = budgets.CheckSerialized(context.Background(), h.pool, ws, bot.Id, h.clock.Now())
				if err == nil || errors.Is(err, llmproxy.ErrBudgetExceeded) {
					t.Fatalf("unknown usage admitted: %v", err)
				}
			}
			foreign := budgets.ProxyAdmission(h.pool, uuid.New(), bot.Id, h.clock.Now)
			if settle, e := foreign(context.Background(), "openai", "fixture", "/v1/chat/completions"); e == nil {
				_ = settle(llmproxy.Usage{Known: true})
				t.Fatal("cross-workspace admission")
			}
		})
	}
}

func TestPhase4ProxyUnpricedTokensAndCanceledSettlement(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	r := h.request(http.MethodPost, base+"/bots", map[string]any{"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "cli", "llmProvider": "codex", "llmModel": "unpriced"}, owner.AccessToken, nil, true)
	var bot apigen.Bot
	r.decode(t, &bot)
	r = h.request(http.MethodPost, base+"/bots/"+bot.Id.String()+"/dm", nil, owner.AccessToken, nil, true)
	var conv apigen.ChatConversation
	r.decode(t, &conv)
	r = h.request(http.MethodPost, base+"/conversations/"+conv.Id.String()+"/messages", map[string]any{"id": uuid.New(), "content": "Unpriced fixture"}, owner.AccessToken, nil, true)
	if r.status != 202 {
		t.Fatal(string(r.body))
	}
	admit := budgets.ProxyAdmission(h.pool, ws, bot.Id, h.clock.Now)
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Execute: func(ctx context.Context, _ engine.Session, emit func(engine.Event) error) error {
		if settle, err := admit(ctx, "anthropic", "unpriced", "/v1/messages"); err == nil {
			_ = settle(llmproxy.Usage{Known: true})
			t.Error("wrong provider admitted")
		}
		request, cancel := context.WithCancel(ctx)
		settle, err := admit(request, "openai", "unpriced", "/v1/responses")
		if err != nil {
			cancel()
			return err
		}
		cancel()
		usage := llmproxy.Usage{Known: true, InputTokens: 10, OutputTokens: 3, CacheTokens: 2}
		if err = settle(usage); err != nil {
			return err
		}
		if err = settle(usage); err != nil {
			return err
		}
		// Paused bots cannot start another request even when there is no money cap.
		if err = h.queries.SetBotPaused(ctx, db.SetBotPausedParams{WorkspaceID: pgtype.UUID{Bytes: ws, Valid: true}, BotID: pgtype.UUID{Bytes: bot.Id, Valid: true}, Paused: true}); err != nil {
			return err
		}
		if settle, err := admit(ctx, "openai", "unpriced", "/v1/responses"); err == nil {
			_ = settle(llmproxy.Usage{Known: true})
			t.Error("paused bot admitted")
		}
		return emit(engine.Event{Kind: engine.EventKindUsage, InputTokens: 999})
	}}
	if err := worker.Process(context.Background(), queue.jobs[0]); err != nil {
		t.Fatal(err)
	}
	var count int
	var input, output, cache int64
	var cost pgtype.Int8
	if err := h.pool.QueryRow(context.Background(), "SELECT count(*),sum(input_tokens)::bigint,sum(output_tokens)::bigint,sum(cache_tokens)::bigint,sum(cost_micros)::bigint FROM usage_records WHERE workspace_id=$1 AND bot_id=$2", ws, bot.Id).Scan(&count, &input, &output, &cache, &cost); err != nil {
		t.Fatal(err)
	}
	if count != 1 || input != 10 || output != 3 || cache != 2 || cost.Valid {
		t.Fatalf("records=%d in=%d out=%d cache=%d cost=%+v", count, input, output, cache, cost)
	}
	if r = h.request(http.MethodPut, base+"/budgets", map[string]any{"period": "month", "limitMicros": 1000}, owner.AccessToken, nil, true); r.status != 200 {
		t.Fatal(string(r.body))
	}
	if err := budgets.CheckSerialized(context.Background(), h.pool, ws, bot.Id, h.clock.Now()); err == nil {
		t.Fatal("unpriced request escaped later workspace budget")
	}
}

func TestPhase4ClaudeSubscriptionMetersTokensWithoutAPICost(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	r := h.request(http.MethodPost, base+"/bots", map[string]any{"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "cli", "llmProvider": "anthropic", "llmModel": "sonnet"}, owner.AccessToken, nil, true)
	var bot apigen.Bot
	r.decode(t, &bot)
	r = h.request(http.MethodPut, base+"/budgets", map[string]any{"botId": bot.Id, "period": "day", "limitMicros": 100}, owner.AccessToken, nil, true)
	if r.status != 200 {
		t.Fatal(string(r.body))
	}
	r = h.request(http.MethodPost, base+"/bots/"+bot.Id.String()+"/dm", nil, owner.AccessToken, nil, true)
	var conv apigen.ChatConversation
	r.decode(t, &conv)
	r = h.request(http.MethodPost, base+"/conversations/"+conv.Id.String()+"/messages", map[string]any{"id": uuid.New(), "content": "Subscription fixture"}, owner.AccessToken, nil, true)
	if r.status != 202 {
		t.Fatal(string(r.body))
	}
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer fixture-private-subscription-token" || r.Header.Get("x-api-key") != "" || !strings.Contains(r.Header.Get("anthropic-beta"), "oauth-2025-04-20") {
			t.Error("subscription auth changed or fell back to an API key")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":2,\"output_tokens\":0}}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer upstream.Close()
	handler, err := llmproxy.NewScopedHandler(ws.String(), bot.Id.String(), func(context.Context, string, string) (*llmproxy.SecretConfig, error) {
		return &llmproxy.SecretConfig{UpstreamURL: upstream.URL, AuthKind: llmproxy.AuthKindClaudeOAuth, SecretValue: "fixture-private-subscription-token"}, nil
	}, &llmproxy.Options{Admit: budgets.ProxyAdmission(h.pool, ws, bot.Id, h.clock.Now)})
	if err != nil {
		t.Fatal(err)
	}
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Execute: func(ctx context.Context, _ engine.Session, emit func(engine.Event) error) error {
		for range 2 {
			req := httptest.NewRequest("POST", "/llm/anthropic/v1/messages?beta=true", strings.NewReader(`{"model":"claude-sonnet-fixture","stream":true}`)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer botbox-placeholder")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Errorf("subscription blocked without model price: %d %s", rec.Code, rec.Body.String())
			}
		}
		return emit(engine.Event{Kind: engine.EventKindUsage, InputTokens: 999})
	}}
	if err = worker.Process(context.Background(), queue.jobs[0]); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("provider calls=%d", calls)
	}
	var count, subscriptions int
	var input, output int64
	var cost pgtype.Int8
	if err = h.pool.QueryRow(context.Background(), "SELECT count(*),count(*) FILTER(WHERE billing_kind='subscription'),sum(input_tokens)::bigint,sum(output_tokens)::bigint,sum(cost_micros)::bigint FROM usage_records WHERE workspace_id=$1 AND bot_id=$2", ws, bot.Id).Scan(&count, &subscriptions, &input, &output, &cost); err != nil {
		t.Fatal(err)
	}
	if count != 2 || subscriptions != 2 || input != 24 || output != 6 || cost.Valid {
		t.Fatalf("count=%d subscription=%d input=%d output=%d cost=%+v", count, subscriptions, input, output, cost)
	}
	if err = budgets.CheckSerialized(context.Background(), h.pool, ws, bot.Id, h.clock.Now()); err != nil {
		t.Fatalf("tokens-only usage treated as missing billing: %v", err)
	}
}

func TestPhase4DesktopClaudeMetersWithoutChatTurn(t *testing.T) {
	const secret = "fixture-private-subscription-token"
	h := newTestHarness(t, nil, harnessOpts{})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	r := h.request(http.MethodPost, base+"/bots", map[string]any{"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "cli", "llmProvider": "anthropic", "llmModel": "sonnet"}, owner.AccessToken, nil, true)
	var bot apigen.Bot
	r.decode(t, &bot)
	// Holding the desktop is how a person opens a terminal. That must not block
	// their own metered CLI call.
	if _, err := h.pool.Exec(context.Background(), `INSERT INTO environments (bot_id, driver, status, control_holder_user_id) VALUES ($1, 'docker', 'running', $2)`, bot.Id, owner.User.Id); err != nil {
		t.Fatal(err)
	}
	r = h.request(http.MethodPost, base+"/bots", map[string]any{"name": "Codex", "avatarSeed": "codex-seed", "llmMode": "cli", "llmProvider": "openai", "llmModel": "fixture"}, owner.AccessToken, nil, true)
	var other apigen.Bot
	r.decode(t, &other)
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer "+secret || r.Header.Get("x-api-key") != "" {
			t.Error("desktop request did not receive the subscription credential")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":4,\"output_tokens\":0}}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer upstream.Close()
	handler, err := llmproxy.NewScopedHandler(ws.String(), bot.Id.String(), func(context.Context, string, string) (*llmproxy.SecretConfig, error) {
		return &llmproxy.SecretConfig{UpstreamURL: upstream.URL, AuthKind: llmproxy.AuthKindClaudeOAuth, SecretValue: secret}, nil
	}, &llmproxy.Options{Admit: budgets.ProxyAdmission(h.pool, ws, bot.Id, h.clock.Now)})
	if err != nil {
		t.Fatal(err)
	}
	send := func(header, value string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/llm/anthropic/v1/messages", strings.NewReader(`{"model":"claude-sonnet-fixture","stream":true}`))
		if header != "" {
			req.Header.Set(header, value)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("response leaked credential: %s", rec.Body.String())
		}
		return rec
	}
	if rec := send("Authorization", "Bearer botbox-placeholder"); rec.Code != http.StatusOK {
		t.Fatalf("desktop request status=%d body=%s", rec.Code, rec.Body.String())
	}
	var count, subscriptions int
	var turn pgtype.UUID
	if err = h.pool.QueryRow(context.Background(), "SELECT count(*), count(*) FILTER (WHERE billing_kind='subscription'), turn_id FROM usage_records WHERE workspace_id=$1 AND bot_id=$2 GROUP BY turn_id", ws, bot.Id).Scan(&count, &subscriptions, &turn); err != nil {
		t.Fatal(err)
	}
	if count != 1 || subscriptions != 1 || turn.Valid || calls != 1 {
		t.Fatalf("count=%d subscription=%d turn=%+v calls=%d", count, subscriptions, turn, calls)
	}
	if rec := send("x-api-key", "botbox-placeholder"); rec.Code != http.StatusForbidden || calls != 1 {
		t.Fatalf("api-key fallback status=%d calls=%d body=%s", rec.Code, calls, rec.Body.String())
	}
	if rec := send("", ""); rec.Code != http.StatusForbidden || calls != 1 {
		t.Fatalf("missing placeholder status=%d calls=%d", rec.Code, calls)
	}
	ctx := context.Background()
	if err = h.queries.SetBotPaused(ctx, db.SetBotPausedParams{WorkspaceID: pgtype.UUID{Bytes: ws, Valid: true}, BotID: pgtype.UUID{Bytes: bot.Id, Valid: true}, Paused: true}); err != nil {
		t.Fatal(err)
	}
	if rec := send("Authorization", "Bearer botbox-placeholder"); rec.Code != http.StatusServiceUnavailable || calls != 1 {
		t.Fatalf("paused desktop status=%d calls=%d body=%s", rec.Code, calls, rec.Body.String())
	}
	mismatch, err := llmproxy.NewScopedHandler(ws.String(), other.Id.String(), func(context.Context, string, string) (*llmproxy.SecretConfig, error) {
		return &llmproxy.SecretConfig{UpstreamURL: upstream.URL, AuthKind: llmproxy.AuthKindClaudeOAuth, SecretValue: secret}, nil
	}, &llmproxy.Options{Admit: budgets.ProxyAdmission(h.pool, ws, other.Id, h.clock.Now)})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/llm/anthropic/v1/messages", strings.NewReader(`{"model":"claude-sonnet-fixture"}`))
	req.Header.Set("Authorization", "Bearer botbox-placeholder")
	rec := httptest.NewRecorder()
	mismatch.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || calls != 1 || strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("wrong provider status=%d calls=%d body=%s", rec.Code, calls, rec.Body.String())
	}
}
