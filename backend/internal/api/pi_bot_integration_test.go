// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/budgets"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
)

func TestPiBotConfiguration(t *testing.T) {
	h := newTestHarness(t, nil, harnessOpts{})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	path := "/api/workspaces/" + ws.String() + "/bots"
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "viewer@example.com", "viewer", "Viewer", "CorrectHorse123!")
	body := func(mode, provider, model any) map[string]any {
		return map[string]any{"name": "Pi", "avatarSeed": "pi", "llmMode": mode, "llmProvider": provider, "llmModel": model}
	}

	for _, tc := range []struct {
		name, token string
		input       any
		status      int
	}{
		{"anonymous", "", body("cli", "openrouter", "openrouter/auto"), http.StatusUnauthorized},
		{"viewer", viewer.AccessToken, body("cli", "openrouter", "openrouter/auto"), http.StatusForbidden},
		{"legacy API mode", owner.AccessToken, body("api", "openrouter", "openrouter/auto"), http.StatusBadRequest},
		{"unknown provider", owner.AccessToken, body("cli", "pi", "openrouter/auto"), http.StatusBadRequest},
		{"missing model", owner.AccessToken, body("cli", "openrouter", nil), http.StatusBadRequest},
		{"wrong model type", owner.AccessToken, body("cli", "openrouter", 42), http.StatusBadRequest},
		{"invalid model", owner.AccessToken, body("cli", "openrouter", "--invalid"), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if r := h.request(http.MethodPost, path, tc.input, tc.token, nil, true); r.status != tc.status {
				t.Fatalf("create=%d want=%d %s", r.status, tc.status, r.body)
			}
		})
	}
	created := h.request(http.MethodPost, path, body("cli", "openrouter", "openrouter/auto"), owner.AccessToken, nil, true)
	if created.status != http.StatusCreated {
		t.Fatalf("create Pi bot=%d %s", created.status, created.body)
	}
	var bot apigen.Bot
	created.decode(t, &bot)
	if bot.LlmMode != apigen.Cli || bot.LlmProvider != "openrouter" || bot.LlmModel != "openrouter/auto" {
		t.Fatalf("stored bot=%+v", bot)
	}
	if r := h.request(http.MethodPatch, path+"/"+bot.Id.String(), map[string]string{"llmMode": "api"}, owner.AccessToken, nil, true); r.status != http.StatusBadRequest {
		t.Fatalf("restoring API mode=%d", r.status)
	}
	if r := h.request(http.MethodPatch, path+"/"+bot.Id.String(), map[string]string{"llmModel": "invalid"}, owner.AccessToken, nil, true); r.status != http.StatusBadRequest {
		t.Fatalf("invalid model update=%d", r.status)
	}
	change := map[string]string{"llmModel": "openrouter/auto:free"}
	if r := h.request(http.MethodPatch, path+"/"+bot.Id.String(), change, viewer.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("viewer model update=%d", r.status)
	}
	other := h.request(http.MethodPost, "/api/workspaces", map[string]string{"name": "Other", "slug": "other"}, owner.AccessToken, nil, true)
	if other.status != http.StatusCreated {
		t.Fatalf("create other workspace=%d %s", other.status, other.body)
	}
	var otherWS apigen.WorkspaceSummary
	other.decode(t, &otherWS)
	foreignPath := "/api/workspaces/" + otherWS.Id.String() + "/bots/" + bot.Id.String()
	if r := h.request(http.MethodPatch, foreignPath, change, owner.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("cross-workspace model update=%d", r.status)
	}
}

func TestPiOpenRouterProxyUsesWorkspaceKeyAndSettlesMessages(t *testing.T) {
	h := newTestHarness(t, nil, harnessOpts{})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	bot := createPiBot(t, h, base, owner.AccessToken, "Pi", "pi")
	const credential = "private-openrouter-fixture-value"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/messages" || r.Header.Get("Authorization") != "Bearer "+credential || r.Header.Get("X-API-Key") != "" {
			t.Errorf("unexpected upstream path or authorization headers")
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n"+
			"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3,\"cost\":0.00012}}\n\n"+
			"data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer upstream.Close()

	resolve := secrets.CLIProviderResolver(h.queries, h.keyRing, ws, bot.Id)
	proxy, err := llmproxy.NewScopedHandler(ws.String(), bot.Id.String(), func(ctx context.Context, id, provider string) (*llmproxy.SecretConfig, error) {
		config, err := resolve(ctx, id, provider)
		if err != nil {
			return nil, err
		}
		config.UpstreamURL = upstream.URL + "/api"
		return config, nil
	}, &llmproxy.Options{Admit: budgets.ProxyAdmission(h.pool, ws, bot.Id, h.clock.Now)})
	if err != nil {
		t.Fatal(err)
	}
	call := func(path, model string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"`+model+`","stream":true}`))
		req.Header.Set("X-API-Key", "botbox-placeholder")
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, req)
		if rec.Code != want || strings.Contains(rec.Body.String(), credential) {
			t.Fatalf("proxy status=%d want=%d or unsafe response", rec.Code, want)
		}
	}
	const path = "/llm/openrouter/v1/v1/messages?beta=true"
	call(path, bot.LlmModel, http.StatusForbidden)
	secret := map[string]any{"name": "llm-openrouter", "domainMatch": "openrouter.ai", "pathPrefix": "/api/v1", "authScheme": "bearer", "value": credential}
	if r := h.request(http.MethodPost, base+"/secrets", secret, owner.AccessToken, nil, true); r.status != http.StatusCreated || strings.Contains(string(r.body), credential) {
		t.Fatalf("workspace secret status=%d or unsafe response", r.status)
	}
	if r := h.request(http.MethodPut, base+"/budgets", map[string]any{"botId": bot.Id, "period": "day", "limitMicros": 100}, owner.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("budget status=%d %s", r.status, r.body)
	}
	price := map[string]any{"provider": "openrouter", "model": bot.LlmModel, "inputMicrosPerMillion": 1000000, "outputMicrosPerMillion": 1000000, "cacheMicrosPerMillion": 0}
	if r := h.request(http.MethodPut, base+"/model-prices", price, owner.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("price status=%d %s", r.status, r.body)
	}
	call("/llm/openrouter/v1/v1/messages/count_tokens", bot.LlmModel, http.StatusForbidden)
	call(path, "openrouter/other", http.StatusServiceUnavailable)
	call(path, bot.LlmModel, http.StatusOK)
	call(path, bot.LlmModel, http.StatusTooManyRequests)

	var count, cost int64
	err = h.pool.QueryRow(context.Background(), `SELECT count(*), sum(cost_micros)::bigint FROM usage_records WHERE workspace_id=$1 AND bot_id=$2`, ws, bot.Id).Scan(&count, &cost)
	if err != nil || count != 1 || cost != 120 {
		t.Fatalf("usage records=%d cost=%d err=%v", count, cost, err)
	}
}
