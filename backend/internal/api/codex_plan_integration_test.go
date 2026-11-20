// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/budgets"
	"github.com/dunkeltech/botbox/backend/internal/codexplan"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	planAccess     = "fixture-secret-access-value"
	planRefresh    = "fixture-secret-renew-value"
	rotatedRefresh = "new-fixture-refresh"
	planClient     = "oaiapp_fixture"
	planNonce      = "nonce_fixture_nonce_fixture_nonce_fixture_01"
)

type planTransport func(*http.Request) (*http.Response, error)

func (f planTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestCodexPlanDesktopAuthorizationAndAccounting(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var refreshes atomic.Int32
	transport := planTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "auth.openai.com" {
			return nil, errors.New("unexpected OAuth origin")
		}
		var response any
		switch r.URL.Path {
		case "/.well-known/jwks.json":
			response = map[string]any{"keys": []any{map[string]string{"kid": "fixture", "kty": "RSA",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}}
		case "/api/accounts/oauth/token":
			if err := r.ParseForm(); err != nil {
				return nil, err
			}
			if r.Form.Get("client_id") != planClient || r.Form.Get("resource") != "https://api.openai.com/v1" {
				return nil, errors.New("wrong OAuth client or resource")
			}
			switch r.Form.Get("grant_type") {
			case "authorization_code":
				scope := "resource.invoke chatgpt.tokens.use.direct"
				if r.Form.Get("code") == "declined" {
					scope = "openid"
				}
				response = map[string]any{"access_token": planAccess, "refresh_token": planRefresh, "id_token": signedPlanID(t, key, now),
					"expires_in": 3600, "token_type": "Bearer", "scope": scope}
			case "refresh_token":
				refreshes.Add(1)
				response = map[string]any{"access_token": planAccess, "refresh_token": rotatedRefresh, "expires_in": 3600, "token_type": "Bearer"}
			default:
				return nil, errors.New("unknown OAuth grant")
			}
		case "/api/accounts/oauth/revoke":
			if r.ParseForm() != nil || r.Form.Get("token") != rotatedRefresh {
				return nil, errors.New("incorrect revocation")
			}
			response = map[string]any{}
		default:
			return nil, errors.New("unexpected OAuth endpoint")
		}
		body, err := json.Marshal(response)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	oauthClock := now
	oauth := codexplan.NewOAuth(transport, func() time.Time { return oauthClock })
	h := newTestHarness(t, nil, harnessOpts{codexPlanOAuth: oauth})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	token := owner.AccessToken
	do := func(method, path string, body any, bearer string, want int) apiResponse {
		t.Helper()
		r := h.request(method, path, body, bearer, nil, true)
		if r.status != want {
			t.Fatalf("%s %s returned %d, want %d: %s", method, path, r.status, want, r.body)
		}
		if bytes.Contains(r.body, []byte(planAccess)) || bytes.Contains(r.body, []byte(planRefresh)) {
			t.Fatal("credential reflected to client")
		}
		return r
	}
	var bot apigen.Bot
	do("POST", base+"/bots", map[string]any{"name": "Desktop", "avatarSeed": "desktop", "llmMode": "cli", "llmProvider": "anthropic", "llmModel": "fixture"}, token, 201).decode(t, &bot)
	botPath := base + "/bots/" + bot.Id.String()
	pairRoute := botPath + "/codex-plan/pairing"
	do("POST", pairRoute, nil, "", 401)
	viewer, _ := h.inviteAndAcceptNewUser(token, ws, "viewer@example.com", "viewer", "Viewer", "CorrectHorse123!")
	do("POST", pairRoute, nil, viewer.AccessToken, 403)
	do("POST", base+"/bots/"+uuid.NewString()+"/codex-plan/pairing", nil, token, 404)
	var other apigen.WorkspaceSummary
	do("POST", "/api/workspaces", map[string]string{"name": "Other workspace", "slug": "other"}, token, 201).decode(t, &other)
	do("POST", "/api/workspaces/"+other.Id.String()+"/bots/"+bot.Id.String()+"/codex-plan/pairing", nil, token, 404)
	complete := func(pair, code string, want int) {
		t.Helper()
		do("POST", "/api/codex-plan/pairing/complete", apigen.CompleteCodexPlanRequest{Pairing: pair, Code: code,
			ClientId: planClient, Verifier: strings.Repeat("v", 43), Nonce: planNonce, Redirect: "http://127.0.0.1:38331/auth/callback"}, "", want)
	}
	newCode := func() string {
		t.Helper()
		var pair apigen.CodexPlanPairing
		do("POST", pairRoute, nil, token, 200).decode(t, &pair)
		return pair.Code
	}
	do("PUT", base+"/subscription-budget-policy", map[string]any{}, token, 400)
	do("PUT", base+"/subscription-budget-policy", map[string]any{"enabled": true}, viewer.AccessToken, 403)
	complete("invalid", "fixture", 400)
	expired := newCode()
	h.clock.Advance(10 * time.Minute)
	complete(expired, "fixture", 409)
	complete(newCode(), "declined", 403)
	code := newCode()
	complete(code, "fixture", 204)
	complete(code, "fixture", 409)
	var status apigen.CodexPlanStatus
	do("GET", botPath+"/codex-plan", nil, token, 200).decode(t, &status)
	if status.Status != "connected" || !status.OwnedByYou || status.SecretScope == nil || *status.SecretScope != apigen.SecretScopeBot {
		t.Fatalf("connection unavailable: %+v", status)
	}
	var botSecretsResp apigen.SecretListResponse
	do("GET", botPath+"/secrets", nil, token, 200).decode(t, &botSecretsResp)
	if len(botSecretsResp.Secrets) != 1 || botSecretsResp.Secrets[0].Name != "llm-openai" ||
		botSecretsResp.Secrets[0].Scope != apigen.SecretScopeBot || botSecretsResp.Secrets[0].DomainMatch != "api.openai.com" {
		t.Fatalf("expected synchronized llm-openai bot secret, got %+v", botSecretsResp.Secrets)
	}

	botpg := pgtype.UUID{Bytes: bot.Id, Valid: true}
	wspg := pgtype.UUID{Bytes: ws, Valid: true}
	holder := pgtype.UUID{Bytes: owner.User.Id, Valid: true}
	svc := codexplan.NewService(h.pool, h.keyRing, oauth, func() time.Time { return oauthClock })
	if _, _, err := svc.Access(ctx, ws, bot.Id); !errors.Is(err, codexplan.ErrForbidden) {
		t.Fatalf("desktop without control: %v", err)
	}
	if _, err := h.queries.UpsertEnvironment(ctx, db.UpsertEnvironmentParams{BotID: botpg, Driver: "docker", EnvID: "fixture-desktop",
		Status: "running", Image: "fixture", CpuMillis: 1000, MemoryMib: 1024, ControlHolderUserID: holder}); err != nil {
		t.Fatal(err)
	}
	oauthClock = now.Add(2 * time.Hour)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if access, _, err := svc.Access(ctx, ws, bot.Id); err != nil || access != planAccess {
				t.Error("desktop grant unavailable after refresh")
			}
		})
	}
	wg.Wait()
	if refreshes.Load() != 1 {
		t.Fatalf("refresh count %d", refreshes.Load())
	}

	do("PUT", base+"/budgets", map[string]any{"botId": bot.Id, "period": "day", "limitMicros": 20}, token, 200)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+planAccess || r.Header.Get("X-Api-Key") != "" || r.URL.Path != "/v1/responses" {
			t.Error("incorrect upstream credential or path")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["store"] != false || body["stream"] != true || body["metadata"] != nil {
			t.Error("incorrect plan payload")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"input_tokens_details\":{\"cached_tokens\":2},\"cost\":1}}}\n\n")
	}))
	defer upstream.Close()
	proxy, err := llmproxy.NewScopedHandler(ws.String(), bot.Id.String(), func(ctx context.Context, _, provider string) (*llmproxy.SecretConfig, error) {
		if provider != llmproxy.ProviderOpenAIPlan {
			return nil, llmproxy.ErrSecretNotFound
		}
		access, rev, err := svc.Access(ctx, ws, bot.Id)
		if errors.Is(err, codexplan.ErrForbidden) {
			return nil, llmproxy.ErrSecretNotFound
		}
		if err != nil {
			return nil, err
		}
		return &llmproxy.SecretConfig{UpstreamURL: upstream.URL, AuthKind: llmproxy.AuthKindOpenAIPlan, SecretValue: access, PlanRevision: rev}, nil
	}, &llmproxy.Options{Admit: budgets.ProxyAdmission(h.pool, ws, bot.Id, h.clock.Now)})
	if err != nil {
		t.Fatal(err)
	}
	infer := func(want int, code string) {
		t.Helper()
		req := httptest.NewRequest("POST", "/llm/openai-plan/v1/responses",
			strings.NewReader(`{"model":"fixture","input":[{"role":"user","content":"hello"}],"metadata":{},"stream":false}`))
		req.Header.Set("Authorization", "Bearer botbox-placeholder")
		req.Header.Set("X-Api-Key", "not-forwarded")
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, req)
		if rec.Code != want || !strings.Contains(rec.Body.String(), code) {
			t.Fatalf("inference status %d: %s", rec.Code, rec.Body.String())
		}
	}
	rule := db.UpsertEgressRuleParams{WorkspaceID: wspg, BotID: botpg, Policy: "open", AllowedHosts: []string{}, BlockedHosts: []string{"*.openai.com"}, Revision: 1}
	if _, err := h.queries.UpsertEgressRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	infer(403, "forbidden")
	rule.BlockedHosts, rule.Revision = []string{}, 2
	if _, err := h.queries.UpsertEgressRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	infer(200, "response.completed")
	do("PUT", base+"/subscription-budget-policy", map[string]any{"enabled": true}, token, 204)
	infer(503, "model_price_required")
	do("PUT", base+"/model-prices", map[string]any{"provider": "openai", "model": "fixture", "inputMicrosPerMillion": 2000000, "outputMicrosPerMillion": 1000000, "cacheMicrosPerMillion": 1000000}, token, 200)
	infer(200, "response.completed")
	infer(429, "budget_exceeded")
	if calls.Load() != 2 {
		t.Fatalf("upstream calls %d", calls.Load())
	}
	var count, input, output, cached, charge int64
	if err := h.pool.QueryRow(ctx, `SELECT count(*),sum(input_tokens)::bigint,sum(output_tokens)::bigint,sum(cache_tokens)::bigint,sum(internal_charge_micros)::bigint
FROM usage_records WHERE workspace_id=$1 AND bot_id=$2 AND turn_id IS NULL AND cost_micros IS NULL`, ws, bot.Id).Scan(&count, &input, &output, &cached, &charge); err != nil {
		t.Fatal(err)
	}
	if count != 2 || input != 20 || output != 10 || cached != 4 || charge != 23 {
		t.Fatalf("usage count=%d in=%d out=%d cached=%d charge=%d", count, input, output, cached, charge)
	}
	var summary apigen.BudgetSummary
	do("GET", base+"/budgets?botId="+url.QueryEscape(bot.Id.String()), nil, token, 200).decode(t, &summary)
	if summary.SpendTodayMicros != 0 || summary.SpendThisWeekMicros != 0 ||
		summary.TotalSpendTodayMicros != 23 || summary.TotalSpendThisWeekMicros != 23 ||
		summary.SubscriptionUsage == nil || len(*summary.SubscriptionUsage) != 4 ||
		len(summary.Budgets) != 1 || summary.Budgets[0].SpentMicros != 23 || !summary.Budgets[0].Exceeded {
		t.Fatalf("summary %+v", summary)
	}
	var wsSummary apigen.BudgetSummary
	do("GET", base+"/budgets", nil, token, 200).decode(t, &wsSummary)
	if wsSummary.SpendTodayMicros != 0 || wsSummary.SpendThisWeekMicros != 0 ||
		wsSummary.TotalSpendTodayMicros != 23 || wsSummary.TotalSpendThisWeekMicros != 23 {
		t.Fatalf("workspace summary %+v", wsSummary)
	}
	do("POST", botPath+"/control/release", nil, token, 200)
	infer(403, "forbidden")
	var removed apigen.CodexPlanDisconnect
	do("DELETE", botPath+"/codex-plan", nil, token, 200).decode(t, &removed)
	if !removed.RevocationConfirmed {
		t.Fatal("refresh token revocation was not confirmed")
	}
	do("GET", botPath+"/secrets", nil, token, 200).decode(t, &botSecretsResp)
	if len(botSecretsResp.Secrets) != 0 {
		t.Fatalf("expected llm-openai bot secret removed on disconnect, got %+v", botSecretsResp.Secrets)
	}
	if bytes.Contains(h.logs.Bytes(), []byte(planAccess)) || bytes.Contains(h.logs.Bytes(), []byte(planRefresh)) {
		t.Fatal("credential entered log")
	}
}

func TestCodexPlanSecretSyncWorkspaceAndChatTurn(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var refreshes atomic.Int32
	var revokeToken string
	oauthClock := now
	transport := planTransport(func(r *http.Request) (*http.Response, error) {
		var response any
		switch r.URL.Path {
		case "/.well-known/jwks.json":
			response = map[string]any{"keys": []any{map[string]string{"kid": "fixture", "kty": "RSA",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}}
		case "/api/accounts/oauth/token":
			if err := r.ParseForm(); err != nil {
				return nil, err
			}
			switch r.Form.Get("grant_type") {
			case "authorization_code":
				response = map[string]any{"access_token": planAccess, "refresh_token": planRefresh, "id_token": signedPlanID(t, key, oauthClock),
					"expires_in": 3600, "token_type": "Bearer", "scope": "resource.invoke chatgpt.tokens.use.direct"}
			case "refresh_token":
				refreshes.Add(1)
				if r.Form.Get("refresh_token") == "invalid-refresh" {
					body, _ := json.Marshal(map[string]string{"error": "refresh_token_reused"})
					return &http.Response{StatusCode: 400, Body: io.NopCloser(bytes.NewReader(body))}, nil
				}
				response = map[string]any{"access_token": "refreshed-access-fixture", "refresh_token": rotatedRefresh, "expires_in": 3600, "token_type": "Bearer"}
			default:
				return nil, errors.New("unknown OAuth grant")
			}
		case "/api/accounts/oauth/revoke":
			if err := r.ParseForm(); err != nil {
				return nil, err
			}
			revokeToken = r.Form.Get("token")
			response = map[string]any{}
		default:
			return nil, errors.New("unexpected OAuth endpoint")
		}
		body, _ := json.Marshal(response)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	oauth := codexplan.NewOAuth(transport, func() time.Time { return oauthClock })
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{codexPlanOAuth: oauth, turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	token := owner.AccessToken
	member, _ := h.inviteAndAcceptNewUser(token, ws, "member@example.com", "member", "Member", "CorrectHorse123!")

	do := func(method, path string, body any, bearer string, want int) apiResponse {
		t.Helper()
		r := h.request(method, path, body, bearer, nil, true)
		if r.status != want {
			t.Fatalf("%s %s returned %d, want %d: %s", method, path, r.status, want, r.body)
		}
		if bytes.Contains(r.body, []byte(planAccess)) || bytes.Contains(r.body, []byte(planRefresh)) || bytes.Contains(r.body, []byte(rotatedRefresh)) {
			t.Fatal("credential reflected to client")
		}
		return r
	}

	var botA, botB apigen.Bot
	do("POST", base+"/bots", map[string]any{"name": "BotA", "avatarSeed": "bota", "llmMode": "cli", "llmProvider": "openai", "llmModel": "gpt-5"}, token, 201).decode(t, &botA)
	do("POST", base+"/bots", map[string]any{"name": "BotB", "avatarSeed": "botb", "llmMode": "cli", "llmProvider": "openai", "llmModel": "gpt-5"}, member.AccessToken, 201).decode(t, &botB)

	// Member cannot create workspace-scoped pairing, and invalid scopes are rejected.
	do("POST", base+"/bots/"+botB.Id.String()+"/codex-plan/pairing", map[string]any{"secretScope": "workspace"}, member.AccessToken, 403)
	do("POST", base+"/bots/"+botA.Id.String()+"/codex-plan/pairing", map[string]any{"secretScope": "invalid"}, token, 400)

	// Pre-existing workspace llm-openai API key is updated/overwritten by owner workspace pairing.
	do("POST", base+"/secrets", map[string]any{"name": "llm-openai", "domainMatch": "api.openai.com", "authScheme": "bearer", "value": "sk-old-manual-key"}, token, 201)

	var pairResp apigen.CodexPlanPairing
	do("POST", base+"/bots/"+botA.Id.String()+"/codex-plan/pairing", map[string]any{"secretScope": "workspace"}, token, 200).decode(t, &pairResp)
	do("POST", "/api/codex-plan/pairing/complete", apigen.CompleteCodexPlanRequest{
		Pairing: pairResp.Code, Code: "fixture", ClientId: planClient,
		Verifier: strings.Repeat("v", 43), Nonce: planNonce, Redirect: "http://127.0.0.1:38331/auth/callback",
	}, "", 204)

	var wsSecrets apigen.SecretListResponse
	do("GET", base+"/secrets", nil, token, 200).decode(t, &wsSecrets)
	if len(wsSecrets.Secrets) != 1 || wsSecrets.Secrets[0].Name != "llm-openai" || wsSecrets.Secrets[0].Scope != apigen.SecretScopeWorkspace {
		t.Fatalf("expected overwritten workspace llm-openai secret, got %+v", wsSecrets.Secrets)
	}
	var status apigen.CodexPlanStatus
	do("GET", base+"/bots/"+botA.Id.String()+"/codex-plan", nil, token, 200).decode(t, &status)
	if status.Status != "connected" || status.SecretScope == nil || *status.SecretScope != apigen.SecretScopeWorkspace {
		t.Fatalf("unexpected workspace status: %+v", status)
	}

	// Bot B inherits workspace llm-openai and runs both chat turns (without desktop control)
	// and interactive desktop calls (requiring desktop control) over /llm/openai/v1/responses.
	svc := codexplan.NewService(h.pool, h.keyRing, oauth, func() time.Time { return oauthClock })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer refreshed-access-fixture" {
			t.Errorf("unexpected upstream auth/path: %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":12,\"output_tokens\":4}}}\n\n")
	}))
	defer upstream.Close()

	proxyHandler, err := llmproxy.NewScopedHandler(ws.String(), botB.Id.String(), func(ctx context.Context, _, provider string) (*llmproxy.SecretConfig, error) {
		if provider != llmproxy.ProviderOpenAI {
			return nil, llmproxy.ErrSecretNotFound
		}
		cfg, resolveErr := svc.ResolveOpenAISecret(ctx, ws, botB.Id)
		if resolveErr != nil {
			return nil, resolveErr
		}
		cfg.UpstreamURL = upstream.URL
		return cfg, nil
	}, &llmproxy.Options{Admit: budgets.ProxyAdmission(h.pool, ws, botB.Id, h.clock.Now)})
	if err != nil {
		t.Fatal(err)
	}

	// Desktop call without desktop control is forbidden.
	noControlReq := httptest.NewRequest(http.MethodPost, "/llm/openai/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[]}`))
	noControlReq.Header.Set("Authorization", "Bearer botbox-placeholder")
	noControlRec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(noControlRec, noControlReq)
	if noControlRec.Code != http.StatusForbidden {
		t.Fatalf("inherited desktop without control: %d %s", noControlRec.Code, noControlRec.Body.String())
	}

	// Advance clock to force refresh, then run concurrent chat-turn requests on Bot B without desktop control.
	oauthClock = now.Add(2 * time.Hour)
	var conv apigen.ChatConversation
	do("POST", base+"/bots/"+botB.Id.String()+"/dm", nil, member.AccessToken, 200).decode(t, &conv)
	do("POST", base+"/conversations/"+conv.Id.String()+"/messages", map[string]any{"id": uuid.New(), "content": "Run codex"}, member.AccessToken, 202)
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Execute: func(turnCtx context.Context, _ engine.Session, emit func(engine.Event) error) error {
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() {
				req := httptest.NewRequest(http.MethodPost, "/llm/openai/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hi"}]}`)).WithContext(turnCtx)
				req.Header.Set("Authorization", "Bearer botbox-placeholder")
				rec := httptest.NewRecorder()
				proxyHandler.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "response.completed") {
					t.Errorf("chat turn openai request failed: %d %s", rec.Code, rec.Body.String())
				}
			})
		}
		wg.Wait()
		return emit(engine.Event{Kind: engine.EventKindMessage, Text: "done"})
	}}
	if err = worker.Process(ctx, queue.jobs[0]); err != nil {
		t.Fatal(err)
	}
	if refreshes.Load() != 1 {
		t.Fatalf("expected 1 serialized refresh across concurrent requests, got %d", refreshes.Load())
	}

	// Chat turn usage must be recorded as subscription tied to turn_id.
	var turnCount, subCount int
	if err = h.pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE billing_kind='subscription' AND turn_id IS NOT NULL) FROM usage_records WHERE workspace_id=$1 AND bot_id=$2`, ws, botB.Id).Scan(&turnCount, &subCount); err != nil {
		t.Fatal(err)
	}
	if turnCount != 2 || subCount != 2 {
		t.Fatalf("expected 2 subscription chat turn usage records, got total=%d sub=%d", turnCount, subCount)
	}

	// Inherited workspace secret also supports interactive desktop on Bot B when Member takes control.
	if _, err = h.queries.UpsertEnvironment(ctx, db.UpsertEnvironmentParams{
		BotID: pgtype.UUID{Bytes: botB.Id, Valid: true}, Driver: "docker", EnvID: "botb-desktop",
		Status: "running", Image: "fixture", CpuMillis: 1000, MemoryMib: 1024,
		ControlHolderUserID: pgtype.UUID{Bytes: member.User.Id, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err = codexplan.CheckDesktopOpenAI(ctx, h.queries, pgtype.UUID{Bytes: ws, Valid: true}, pgtype.UUID{Bytes: botB.Id, Valid: true}, ""); err != nil {
		t.Fatalf("CheckDesktopOpenAI failed for inherited workspace secret: %v", err)
	}

	// Disconnect removes workspace llm-openai and revokes the rotated refresh token.
	var disc apigen.CodexPlanDisconnect
	do("DELETE", base+"/bots/"+botA.Id.String()+"/codex-plan", nil, token, 200).decode(t, &disc)
	if !disc.RevocationConfirmed || revokeToken != rotatedRefresh {
		t.Fatalf("revocation failed: confirmed=%v token=%q", disc.RevocationConfirmed, revokeToken)
	}
	do("GET", base+"/secrets", nil, token, 200).decode(t, &wsSecrets)
	if len(wsSecrets.Secrets) != 0 {
		t.Fatalf("expected workspace llm-openai deleted on disconnect, got %+v", wsSecrets.Secrets)
	}

	// Manual API-key overwrite unlinks codex_plan and preserves the manual key across disconnect.
	do("POST", base+"/bots/"+botA.Id.String()+"/codex-plan/pairing", map[string]any{"secretScope": "bot"}, token, 200).decode(t, &pairResp)
	do("POST", "/api/codex-plan/pairing/complete", apigen.CompleteCodexPlanRequest{
		Pairing: pairResp.Code, Code: "fixture", ClientId: planClient,
		Verifier: strings.Repeat("v", 43), Nonce: planNonce, Redirect: "http://127.0.0.1:38331/auth/callback",
	}, "", 204)
	var botSecrets apigen.SecretListResponse
	do("GET", base+"/bots/"+botA.Id.String()+"/secrets", nil, token, 200).decode(t, &botSecrets)
	if len(botSecrets.Secrets) != 1 {
		t.Fatalf("expected bot llm-openai secret, got %+v", botSecrets.Secrets)
	}
	do("PUT", base+"/bots/"+botA.Id.String()+"/secrets/"+botSecrets.Secrets[0].Id.String(), map[string]any{
		"name": "llm-openai", "domainMatch": "api.openai.com", "authScheme": "bearer", "value": "sk-manual-override",
	}, token, 200)
	cfg, err := svc.ResolveOpenAISecret(ctx, ws, botA.Id)
	if err != nil || cfg.AuthKind != llmproxy.AuthKindBearer || cfg.SecretValue != "sk-manual-override" {
		t.Fatalf("manual override resolution=%+v err=%v", cfg, err)
	}
	do("GET", base+"/bots/"+botA.Id.String()+"/codex-plan", nil, token, 200).decode(t, &status)
	if status.Status != "disconnected" {
		t.Fatalf("expected disconnected after manual overwrite, got %+v", status)
	}
	do("DELETE", base+"/bots/"+botA.Id.String()+"/codex-plan", nil, token, 200)
	do("GET", base+"/bots/"+botA.Id.String()+"/secrets", nil, token, 200).decode(t, &botSecrets)
	if len(botSecrets.Secrets) != 1 {
		t.Fatalf("manual API key must survive codex-plan disconnect, got %+v", botSecrets.Secrets)
	}

	// Guardrail secret extraction covers both access and refresh tokens of openai_plan secrets.
	wsPayload, _ := secrets.MarshalOpenAIPlanTokens(secrets.OpenAIPlanTokens{
		Access: planAccess, Refresh: planRefresh, Expires: now.Add(time.Hour), ClientID: planClient,
	})
	vals := secrets.ExtractSecretMatchValues(secrets.SecretAuthKindOpenAIPlan, wsPayload)
	if len(vals) != 2 || vals[0] != planAccess || vals[1] != planRefresh {
		t.Fatalf("unexpected extracted secret values: %v", vals)
	}
}

func signedPlanID(t *testing.T, key *rsa.PrivateKey, now time.Time) string {
	t.Helper()
	encode := base64.RawURLEncoding.EncodeToString
	header := encode([]byte(`{"alg":"RS256","kid":"fixture"}`))
	data, err := json.Marshal(map[string]any{"iss": "https://auth.openai.com", "sub": "fixture-owner", "nonce": planNonce, "aud": planClient, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	text := header + "." + encode(data)
	digest := sha256.Sum256([]byte(text))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return text + "." + encode(sig)
}
