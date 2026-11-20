// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"net/http"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/budgets"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/google/uuid"
)

func TestAdminReconcilesSubscriptionUsage(t *testing.T) {
	t.Parallel()
	h := newTestHarness(t, nil, harnessOpts{})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Alex", "Acme Bots", "acme")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	admin, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "admin@example.com", "admin", "Admin", "CorrectHorse123!")
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member@example.com", "member", "Member", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "viewer@example.com", "viewer", "Viewer", "CorrectHorse123!")
	other := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other", "slug": "other"}, owner.AccessToken, nil, true)
	if other.status != http.StatusCreated {
		t.Fatalf("create other workspace: %d %s", other.status, other.body)
	}
	var otherWS apigen.WorkspaceSummary
	other.decode(t, &otherWS)

	bot := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas-seed")
	requestID := uuid.New()
	ctx := t.Context()
	if _, err := h.pool.Exec(ctx, `INSERT INTO usage_records
		(workspace_id, bot_id, request_id, source, model, billing_kind, subscription_budgeted,
		 usage_known, input_tokens, output_tokens, cache_tokens, created_at)
		VALUES ($1, $2, $3, 'proxy_meter', 'gpt-fixture', 'subscription', true, false, 0, 0, 0, $4)`,
		ws, bot.Id, requestID, h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if r := h.request(http.MethodPut, base+"/budgets",
		map[string]any{"period": "month", "limitMicros": 1000000}, owner.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("set budget: %d %s", r.status, r.body)
	}
	if err := budgets.CheckSerialized(ctx, h.pool, ws, bot.Id, h.clock.Now()); err == nil {
		t.Fatal("unresolved usage did not block budget")
	}

	listPath := base + "/subscription-usage/unresolved"
	settlePath := base + "/subscription-usage/" + requestID.String() + "/reconcile"
	input := apigen.ReconcileSubscriptionUsageRequest{
		Provider: apigen.Openai, Basis: apigen.OperatorEstimate, InputTokens: 1,
	}
	for _, tc := range []struct {
		name, token string
		status      int
	}{
		{"unauthenticated", "", http.StatusUnauthorized},
		{"member", member.AccessToken, http.StatusForbidden},
		{"viewer", viewer.AccessToken, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if r := h.request(http.MethodGet, listPath, nil, tc.token, nil, false); r.status != tc.status {
				t.Fatalf("list: %d %s", r.status, r.body)
			}
			if r := h.request(http.MethodPost, settlePath, input, tc.token, nil, true); r.status != tc.status {
				t.Fatalf("reconcile: %d %s", r.status, r.body)
			}
		})
	}
	crossPath := "/api/workspaces/" + otherWS.Id.String() + "/subscription-usage/" + requestID.String() + "/reconcile"
	if r := h.request(http.MethodPost, crossPath, input, owner.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("cross-workspace reconcile: %d %s", r.status, r.body)
	}
	list := func(token string) []apigen.UnresolvedSubscriptionUsage {
		t.Helper()
		r := h.request(http.MethodGet, listPath, nil, token, nil, false)
		if r.status != http.StatusOK {
			t.Fatalf("list unresolved: %d %s", r.status, r.body)
		}
		var rows []apigen.UnresolvedSubscriptionUsage
		r.decode(t, &rows)
		return rows
	}
	if rows := list(owner.AccessToken); len(rows) != 1 || rows[0].RequestId != requestID || rows[0].BotName != "Atlas" {
		t.Fatalf("unresolved rows: %+v", rows)
	}
	for _, invalid := range []map[string]any{
		{"provider": "openai", "basis": "provider_report", "inputTokens": 1, "cacheTokens": 0},
		{"provider": "openai", "basis": "provider_report", "inputTokens": "1", "cacheTokens": 0, "outputTokens": 0},
		{"provider": "openai", "basis": "provider_report", "inputTokens": -1, "cacheTokens": 0, "outputTokens": 0},
		{"provider": "openai", "basis": "provider_report", "inputTokens": 1, "cacheTokens": 2, "outputTokens": 0},
		{"provider": "openrouter", "basis": "provider_report", "inputTokens": 1, "cacheTokens": 0, "outputTokens": 0},
		{"provider": "openai", "basis": "unspecified", "inputTokens": 1, "cacheTokens": 0, "outputTokens": 0},
	} {
		if r := h.request(http.MethodPost, settlePath, invalid, admin.AccessToken, nil, true); r.status != http.StatusBadRequest {
			t.Fatalf("invalid adjustment: %d %s", r.status, r.body)
		}
	}
	if r := h.request(http.MethodPost, settlePath, input, admin.AccessToken, nil, true); r.status != http.StatusConflict {
		t.Fatalf("price missing: %d %s", r.status, r.body)
	}
	price := apigen.ModelPriceRequest{Provider: "openai", Model: "gpt-fixture", InputMicrosPerMillion: 2_000_000}
	if r := h.request(http.MethodPut, base+"/model-prices", price, admin.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("set price: %d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, settlePath, input, admin.AccessToken, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("reconcile: %d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, settlePath, input, admin.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("repeat reconciliation: %d %s", r.status, r.body)
	}
	if rows := list(admin.AccessToken); len(rows) != 0 {
		t.Fatalf("settled request remains listed: %+v", rows)
	}
	if err := budgets.Check(ctx, db.New(h.pool), ws, bot.Id, h.clock.Now()); err != nil {
		t.Fatalf("budget still blocked after reconciliation: %v", err)
	}
	var inputTokens, charge int64
	var known bool
	if err := h.pool.QueryRow(ctx, `SELECT input_tokens, internal_charge_micros, usage_known
		FROM usage_records WHERE workspace_id=$1 AND request_id=$2`, ws, requestID).Scan(&inputTokens, &charge, &known); err != nil {
		t.Fatal(err)
	}
	if inputTokens != 1 || charge != 2 || !known {
		t.Fatalf("settled usage input=%d charge=%d known=%t", inputTokens, charge, known)
	}
	var basis string
	if err := h.pool.QueryRow(ctx, `SELECT metadata->>'basis' FROM audit_log
		WHERE workspace_id=$1 AND target_id=$2 AND action='subscription_usage.reconciled'`, ws, requestID).Scan(&basis); err != nil || basis != "operator_estimate" {
		t.Fatalf("audit basis=%q err=%v", basis, err)
	}
}
