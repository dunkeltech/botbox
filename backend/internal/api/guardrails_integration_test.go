// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/guardrails"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
	"github.com/google/uuid"
)

type guardrailTestTurnQueue struct {
	*fakeTurnQueue
	mu           sync.Mutex
	invalidated  []uuid.UUID
	onInvalidate func(uuid.UUID)
}

func (q *guardrailTestTurnQueue) PublishGuardrailsInvalidate(_ context.Context, ws uuid.UUID) error {
	q.mu.Lock()
	q.invalidated = append(q.invalidated, ws)
	cb := q.onInvalidate
	q.mu.Unlock()
	if cb != nil {
		cb(ws)
	}
	return nil
}

func (q *guardrailTestTurnQueue) invalidationCount(ws uuid.UUID) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, got := range q.invalidated {
		if got == ws {
			n++
		}
	}
	return n
}

func TestWorkspaceGuardrailsIntegration(t *testing.T) {
	queue := &guardrailTestTurnQueue{fakeTurnQueue: &fakeTurnQueue{}}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})

	guardSvc := guardrails.NewService(h.pool, h.keyRing, nil, h.logger)
	queue.onInvalidate = func(wsID uuid.UUID) {
		guardSvc.InvalidateWorkspace(wsID)
	}

	owner, _ := h.completeSetup("gr-owner@example.com", "CorrectHorse123!", "Owner", "Guardrails WS", "gr-ws")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	grBase := base + "/guardrails"

	admin, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "gr-admin@example.com", "admin", "Admin", "CorrectHorse123!")
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "gr-member@example.com", "member", "Member", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "gr-viewer@example.com", "viewer", "Viewer", "CorrectHorse123!")

	otherWSResp := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other GR WS", "slug": "other-gr"}, owner.AccessToken, nil, true)
	var otherWS apigen.WorkspaceSummary
	otherWSResp.decode(t, &otherWS)
	eve, _ := h.inviteAndAcceptNewUser(owner.AccessToken, otherWS.Id, "gr-eve@example.com", "owner", "Eve", "CorrectHorse123!")
	otherGRBase := "/api/workspaces/" + otherWS.Id.String() + "/guardrails"

	validPolicyBody := func(enabled bool) map[string]any {
		return map[string]any{
			"enabled":     enabled,
			"failureMode": "fail_closed",
			"secrets": map[string]any{
				"enabled":   true,
				"direction": "both",
				"action":    "block",
			},
			"pii": map[string]any{
				"enabled":   true,
				"direction": "both",
				"action":    "redact",
				"entities":  []string{"email", "iban", "card", "phone"},
			},
			"promptInjection": map[string]any{
				"enabled":   true,
				"direction": "input",
				"action":    "audit",
			},
		}
	}

	validRuleBody := func(slug string) map[string]any {
		return map[string]any{
			"slug":             slug,
			"name":             "Customer Account",
			"matchMode":        "regex",
			"pattern":          `ACCT-\d{6}`,
			"direction":        "both",
			"action":           "redact",
			"replacementToken": "[REDACTED]",
			"enabled":          true,
		}
	}

	// Create one real rule in WS so cross-workspace rule ID probing is verified too.
	seedRuleResp := h.request(http.MethodPost, grBase+"/rules", validRuleBody("ws1-seed-rule"), owner.AccessToken, nil, true)
	if seedRuleResp.status != http.StatusCreated {
		t.Fatalf("seed rule = %d %s", seedRuleResp.status, seedRuleResp.body)
	}
	var seedRule apigen.WorkspaceGuardrailRule
	seedRuleResp.decode(t, &seedRule)

	// 1. Unauthenticated -> 401 on all 7 endpoints.
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, grBase, nil},
		{http.MethodPut, grBase + "/policy", validPolicyBody(true)},
		{http.MethodPost, grBase + "/rules", validRuleBody("unauth-rule")},
		{http.MethodPatch, grBase + "/rules/" + seedRule.Id.String(), map[string]any{"name": "X"}},
		{http.MethodDelete, grBase + "/rules/" + seedRule.Id.String(), nil},
		{http.MethodPost, grBase + "/test", map[string]any{"matchMode": "regex", "pattern": `a+`, "sampleText": "aaa"}},
		{http.MethodGet, grBase + "/events", nil},
	} {
		if r := h.request(tc.method, tc.path, tc.body, "", nil, tc.method != http.MethodGet); r.status != http.StatusUnauthorized {
			t.Fatalf("unauth %s %s = %d, want 401", tc.method, tc.path, r.status)
		}
	}

	// 2. Cross-workspace -> 404 on all 7 endpoints (and using WS1 rule ID via WS2 returns 404).
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, grBase, nil},
		{http.MethodPut, grBase + "/policy", validPolicyBody(true)},
		{http.MethodPost, grBase + "/rules", validRuleBody("eve-rule")},
		{http.MethodPatch, grBase + "/rules/" + seedRule.Id.String(), map[string]any{"name": "Hijack"}},
		{http.MethodDelete, grBase + "/rules/" + seedRule.Id.String(), nil},
		{http.MethodPost, grBase + "/test", map[string]any{"matchMode": "regex", "pattern": `a+`, "sampleText": "aaa"}},
		{http.MethodGet, grBase + "/events", nil},
		{http.MethodPatch, otherGRBase + "/rules/" + seedRule.Id.String(), map[string]any{"name": "CrossID"}},
		{http.MethodDelete, otherGRBase + "/rules/" + seedRule.Id.String(), nil},
	} {
		if r := h.request(tc.method, tc.path, tc.body, eve.AccessToken, nil, tc.method != http.MethodGet); r.status != http.StatusNotFound {
			t.Fatalf("cross-workspace %s %s = %d, want 404 (%s)", tc.method, tc.path, r.status, r.body)
		}
	}

	// 3. RBAC across all 4 roles: viewer & member can GET, cannot mutate -> 403.
	for _, tok := range []string{viewer.AccessToken, member.AccessToken} {
		for _, readPath := range []string{grBase, grBase + "/events"} {
			if r := h.request(http.MethodGet, readPath, nil, tok, nil, false); r.status != http.StatusOK {
				t.Fatalf("read %s = %d, want 200", readPath, r.status)
			}
		}
		for _, tc := range []struct {
			method, path string
			body         any
		}{
			{http.MethodPut, grBase + "/policy", validPolicyBody(true)},
			{http.MethodPost, grBase + "/rules", validRuleBody("rbac-rule")},
			{http.MethodPatch, grBase + "/rules/" + seedRule.Id.String(), map[string]any{"name": "Denied"}},
			{http.MethodDelete, grBase + "/rules/" + seedRule.Id.String(), nil},
			{http.MethodPost, grBase + "/test", map[string]any{"matchMode": "regex", "pattern": `a+`, "sampleText": "aaa"}},
		} {
			if r := h.request(tc.method, tc.path, tc.body, tok, nil, true); r.status != http.StatusForbidden {
				t.Fatalf("forbidden %s %s = %d, want 403 (%s)", tc.method, tc.path, r.status, r.body)
			}
		}
	}

	// 4. Default policy is opt-in (enabled=false, activeRuleCount=0), admin can update -> revision increments & invalidates.
	initResp := h.request(http.MethodGet, grBase, nil, admin.AccessToken, nil, false)
	if initResp.status != http.StatusOK {
		t.Fatalf("get initial guardrails = %d %s", initResp.status, initResp.body)
	}
	var initState apigen.WorkspaceGuardrailsResponse
	initResp.decode(t, &initState)
	if initState.Policy.Enabled || initState.ActiveRuleCount != 0 || !initState.Policy.Secrets.Enabled {
		t.Fatalf("unexpected initial opt-in state: %+v", initState)
	}

	// Invalid policy inputs -> 400 validation_error.
	badPol := validPolicyBody(true)
	badPol["failureMode"] = "ignore"
	if r := h.request(http.MethodPut, grBase+"/policy", badPol, admin.AccessToken, nil, true); r.status != http.StatusBadRequest {
		t.Fatalf("bad policy failureMode = %d, want 400", r.status)
	}

	// Admin updates policy (enable=false initially for proxy passthrough check later, then update rules).
	updPol := h.request(http.MethodPut, grBase+"/policy", validPolicyBody(false), admin.AccessToken, nil, true)
	if updPol.status != http.StatusOK {
		t.Fatalf("admin update policy = %d %s", updPol.status, updPol.body)
	}
	var polOut apigen.WorkspaceGuardrailPolicy
	updPol.decode(t, &polOut)
	if polOut.Revision < 1 {
		t.Fatalf("expected policy revision >= 1, got %d", polOut.Revision)
	}
	if queue.invalidationCount(ws) < 2 {
		t.Fatalf("expected invalidation broadcast on create/update, got %d", queue.invalidationCount(ws))
	}

	// 5. Custom rule CRUD, duplicate slug 409, invalid pattern 400, missing 404, Quick Test, and 100-rule limit 409.
	dupResp := h.request(http.MethodPost, grBase+"/rules", validRuleBody("ws1-seed-rule"), owner.AccessToken, nil, true)
	if dupResp.status != http.StatusConflict || memErrCode(t, dupResp.body) != "guardrail.rule_conflict" {
		t.Fatalf("duplicate slug = %d %s, want 409 guardrail.rule_conflict", dupResp.status, dupResp.body)
	}
	badRegex := validRuleBody("bad-regex")
	badRegex["pattern"] = "[unclosed"
	if r := h.request(http.MethodPost, grBase+"/rules", badRegex, owner.AccessToken, nil, true); r.status != http.StatusBadRequest || memErrCode(t, r.body) != "guardrail.invalid_pattern" {
		t.Fatalf("invalid regex create = %d %s, want 400 guardrail.invalid_pattern", r.status, r.body)
	}

	patchResp := h.request(http.MethodPatch, grBase+"/rules/"+seedRule.Id.String(), map[string]any{ // #nosec G101 -- synthetic mask label
		"name":             "Account Numbers Masked",
		"replacementToken": "[ACCT-MASK]",
		"enabled":          true,
	}, admin.AccessToken, nil, true)
	if patchResp.status != http.StatusOK {
		t.Fatalf("patch rule = %d %s", patchResp.status, patchResp.body)
	}
	if r := h.request(http.MethodPatch, grBase+"/rules/"+uuid.NewString(), map[string]any{"name": "Missing"}, owner.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("patch missing rule = %d, want 404", r.status)
	}
	if r := h.request(http.MethodPatch, grBase+"/rules/not-a-uuid", map[string]any{"name": "BadID"}, owner.AccessToken, nil, true); r.status != http.StatusBadRequest {
		t.Fatalf("patch bad uuid = %d, want 400", r.status)
	}

	// Quick Test: valid match, invalid regex returns valid=false (no event persisted), bad matchMode returns 400.
	qtOK := h.request(http.MethodPost, grBase+"/test", map[string]any{
		"matchMode":        "regex",
		"pattern":          `ACCT-\d{6}`,
		"replacementToken": "[ACCT]",
		"sampleText":       "Customer ACCT-123456 opened ticket.",
	}, admin.AccessToken, nil, true)
	if qtOK.status != http.StatusOK {
		t.Fatalf("quick test ok = %d %s", qtOK.status, qtOK.body)
	}
	var qtRes apigen.TestGuardrailRuleResponse
	qtOK.decode(t, &qtRes)
	if !qtRes.Valid || qtRes.MatchCount != 1 || qtRes.Preview != "Customer [ACCT] opened ticket." {
		t.Fatalf("unexpected quick test response: %+v", qtRes)
	}
	qtBadRe := h.request(http.MethodPost, grBase+"/test", map[string]any{
		"matchMode":  "regex",
		"pattern":    "(abc",
		"sampleText": "abc",
	}, owner.AccessToken, nil, true)
	if qtBadRe.status != http.StatusOK {
		t.Fatalf("quick test invalid regex status = %d %s", qtBadRe.status, qtBadRe.body)
	}
	var qtBadOut apigen.TestGuardrailRuleResponse
	qtBadRe.decode(t, &qtBadOut)
	if qtBadOut.Valid || qtBadOut.Error == nil {
		t.Fatalf("expected invalid regex diagnostic in quick test, got %+v", qtBadOut)
	}
	if r := h.request(http.MethodPost, grBase+"/test", map[string]any{
		"matchMode":  "fuzzy",
		"pattern":    "abc",
		"sampleText": "abc",
	}, owner.AccessToken, nil, true); r.status != http.StatusBadRequest {
		t.Fatalf("quick test bad matchMode = %d, want 400", r.status)
	}

	// Quick Test must persist 0 guardrail events.
	evBefore := h.request(http.MethodGet, grBase+"/events", nil, owner.AccessToken, nil, false)
	var evListBefore apigen.GuardrailEventListResponse
	evBefore.decode(t, &evListBefore)
	if len(evListBefore.Events) != 0 {
		t.Fatalf("expected 0 events before proxy traffic, got %d", len(evListBefore.Events))
	}

	// Fill up to MaxCustomRules (seedRule = 1, add 99 temp rules, assert 101st -> 409 guardrail.rule_limit), then clean up temp rule to verify DELETE.
	var lastTempID uuid.UUID
	for i := 2; i <= guardrails.MaxCustomRules; i++ {
		slug := fmt.Sprintf("bulk-rule-%03d", i)
		r := h.request(http.MethodPost, grBase+"/rules", validRuleBody(slug), owner.AccessToken, nil, true)
		if r.status != http.StatusCreated {
			t.Fatalf("bulk rule %d = %d %s", i, r.status, r.body)
		}
		var created apigen.WorkspaceGuardrailRule
		r.decode(t, &created)
		lastTempID = created.Id
	}
	overLimit := h.request(http.MethodPost, grBase+"/rules", validRuleBody("rule-101"), owner.AccessToken, nil, true)
	if overLimit.status != http.StatusConflict || memErrCode(t, overLimit.body) != "guardrail.rule_limit" {
		t.Fatalf("101st rule = %d %s, want 409 guardrail.rule_limit", overLimit.status, overLimit.body)
	}
	delResp := h.request(http.MethodDelete, grBase+"/rules/"+lastTempID.String(), nil, admin.AccessToken, nil, true)
	if delResp.status != http.StatusNoContent {
		t.Fatalf("delete rule = %d %s, want 204", delResp.status, delResp.body)
	}

	// 6. End-to-end llmproxy + active DM bot turn + sanitized audit events + chat guardrail/tool cards.
	const (
		rawStoredSecret = "ws-guard-secret-99887766"       // #nosec G101 -- synthetic test secret
		rawStaticSecret = "sk-ant-fixture0123456789abcdef" // #nosec G101 -- synthetic test secret
		rawPIIEmail     = "alice-sensitive@example.com"
		rawCustomAcct   = "ACCT-123456"
	)
	if r := h.request(http.MethodPost, base+"/secrets", map[string]any{
		"name":        "Corp_Guard_Key",
		"domainMatch": "api.example.com",
		"authScheme":  "bearer",
		"value":       rawStoredSecret,
	}, owner.AccessToken, nil, true); r.status != http.StatusCreated {
		t.Fatalf("create ws secret = %d %s", r.status, r.body)
	}

	atlas := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas-gr")
	dmResp := h.request(http.MethodPost, base+"/bots/"+atlas.Id.String()+"/dm", nil, owner.AccessToken, nil, true)
	if dmResp.status != http.StatusOK {
		t.Fatalf("open dm = %d %s", dmResp.status, dmResp.body)
	}
	var conv apigen.ChatConversation
	dmResp.decode(t, &conv)

	msgResp := h.request(http.MethodPost, base+"/conversations/"+conv.Id.String()+"/messages", map[string]any{
		"id":      uuid.New(),
		"content": "Please inspect the ticket",
	}, owner.AccessToken, nil, true)
	if msgResp.status != http.StatusAccepted {
		t.Fatalf("post dm = %d %s", msgResp.status, msgResp.body)
	}
	var turnID uuid.UUID
	ctx := context.Background()
	if err := h.pool.QueryRow(ctx, `UPDATE bot_turns SET status = 'running' WHERE workspace_id = $1 AND bot_id = $2 RETURNING id`, ws, atlas.Id).Scan(&turnID); err != nil {
		t.Fatalf("activate turn: %v", err)
	}

	var seenUpstreams []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seenUpstreams = append(seenUpstreams, string(b))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	defer upstream.Close()

	proxyHandler, err := llmproxy.NewScopedHandler(ws.String(), atlas.Id.String(), func(context.Context, string, string) (*llmproxy.SecretConfig, error) {
		return &llmproxy.SecretConfig{
			UpstreamURL: upstream.URL,
			AuthKind:    llmproxy.AuthKindBearer,
			SecretValue: "upstream-provider-fixture-key",
		}, nil
	}, &llmproxy.Options{
		Guardrails: guardSvc.ForBot(ws, atlas.Id),
		Admit: func(context.Context, string, string, string) (func(llmproxy.Usage) error, error) {
			return func(llmproxy.Usage) error { return nil }, nil
		},
		Logger: h.logger,
	})
	if err != nil {
		t.Fatalf("NewScopedHandler: %v", err)
	}

	// Prime cache while policy.enabled=false -> passes unredacted.
	recOff := httptest.NewRecorder()
	proxyHandler.ServeHTTP(recOff, httptest.NewRequest(http.MethodPost, "/llm/openai/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"`+rawCustomAcct+`"}]}`)))
	if recOff.Code != http.StatusOK || !strings.Contains(seenUpstreams[0], rawCustomAcct) {
		t.Fatalf("disabled policy should passthrough: code=%d seen=%v", recOff.Code, seenUpstreams)
	}

	// Enable policy via REST -> triggers PublishGuardrailsInvalidate -> invalidates guardSvc cache immediately.
	invBefore := queue.invalidationCount(ws)
	enableResp := h.request(http.MethodPut, grBase+"/policy", validPolicyBody(true), owner.AccessToken, nil, true)
	if enableResp.status != http.StatusOK {
		t.Fatalf("enable guardrails = %d %s", enableResp.status, enableResp.body)
	}
	if queue.invalidationCount(ws) != invBefore+1 {
		t.Fatalf("expected cache invalidation broadcast on PUT /policy")
	}

	// Input redact request (PII email + custom ACCT rule): upstream receives masked text.
	recRedact := httptest.NewRecorder()
	proxyHandler.ServeHTTP(recRedact, httptest.NewRequest(http.MethodPost, "/llm/openai/v1/chat/completions", strings.NewReader(
		fmt.Sprintf(`{"model":"gpt-5","messages":[{"role":"user","content":"Email %s and %s"}]}`, rawPIIEmail, rawCustomAcct),
	)))
	if recRedact.Code != http.StatusOK {
		t.Fatalf("redact proxy call = %d %s", recRedact.Code, recRedact.Body.String())
	}
	latestUpstream := seenUpstreams[len(seenUpstreams)-1]
	if strings.Contains(latestUpstream, rawPIIEmail) || strings.Contains(latestUpstream, rawCustomAcct) {
		t.Fatalf("raw PII/custom token reached upstream: %s", latestUpstream)
	}
	if !strings.Contains(latestUpstream, "[REDACTED:EMAIL]") || !strings.Contains(latestUpstream, "[ACCT-MASK]") {
		t.Fatalf("expected masked placeholders at upstream, got: %s", latestUpstream)
	}

	// Input block request using stored workspace secret + static key -> 403, upstream NOT called again.
	callsBeforeBlock := len(seenUpstreams)
	recBlock := httptest.NewRecorder()
	proxyHandler.ServeHTTP(recBlock, httptest.NewRequest(http.MethodPost, "/llm/openai/v1/chat/completions", strings.NewReader(
		fmt.Sprintf(`{"model":"gpt-5","messages":[{"role":"user","content":"Secrets %s and %s"}]}`, rawStoredSecret, rawStaticSecret),
	)))
	if recBlock.Code != http.StatusForbidden {
		t.Fatalf("blocked proxy call = %d, want 403 (%s)", recBlock.Code, recBlock.Body.String())
	}
	if len(seenUpstreams) != callsBeforeBlock {
		t.Fatalf("upstream should not be called on guardrail block")
	}
	if strings.Contains(recBlock.Body.String(), rawStoredSecret) || strings.Contains(recBlock.Body.String(), rawStaticSecret) {
		t.Fatalf("raw secret leaked in 403 response body: %s", recBlock.Body.String())
	}

	// Verify GET /guardrails summary24h and GET /events (unfiltered and filtered) with zero raw secret/PII leakage.
	summaryAfterResp := h.request(http.MethodGet, grBase, nil, owner.AccessToken, nil, false)
	var summaryAfter apigen.WorkspaceGuardrailsResponse
	summaryAfterResp.decode(t, &summaryAfter)
	if summaryAfter.Summary24h.RedactedCount < 1 || summaryAfter.Summary24h.BlockedCount < 1 {
		t.Fatalf("expected redacted>=1 and blocked>=1 in summary24h, got %+v", summaryAfter.Summary24h)
	}

	evAllResp := h.request(http.MethodGet, grBase+"/events?direction=input", nil, member.AccessToken, nil, false)
	if evAllResp.status != http.StatusOK {
		t.Fatalf("list events = %d %s", evAllResp.status, evAllResp.body)
	}
	for _, sensitive := range []string{rawStoredSecret, rawStaticSecret, rawPIIEmail, rawCustomAcct} {
		if strings.Contains(string(evAllResp.body), sensitive) {
			t.Fatalf("sensitive string %q leaked in guardrail events response: %s", sensitive, evAllResp.body)
		}
	}

	evBlockedResp := h.request(http.MethodGet, grBase+"/events?action=blocked&direction=input", nil, viewer.AccessToken, nil, false)
	var blockedEvents apigen.GuardrailEventListResponse
	evBlockedResp.decode(t, &blockedEvents)
	if len(blockedEvents.Events) != 1 || blockedEvents.Events[0].Action != apigen.GuardrailEventActionBlocked {
		t.Fatalf("expected 1 blocked input event, got %+v", blockedEvents.Events)
	}

	// Verify inline kind='guardrail' chat cards AND kind='tool' trace steps in DM transcript without sensitive values.
	histResp := h.request(http.MethodGet, base+"/conversations/"+conv.Id.String()+"/messages", nil, owner.AccessToken, nil, false)
	if histResp.status != http.StatusOK {
		t.Fatalf("get dm messages = %d %s", histResp.status, histResp.body)
	}
	for _, sensitive := range []string{rawStoredSecret, rawStaticSecret, rawPIIEmail, rawCustomAcct} {
		if strings.Contains(string(histResp.body), sensitive) {
			t.Fatalf("sensitive string %q leaked in chat history: %s", sensitive, histResp.body)
		}
	}
	var hist apigen.ChatHistory
	histResp.decode(t, &hist)
	guardCards := 0
	guardTools := 0
	for _, m := range hist.Messages {
		if m.Kind == apigen.ChatMessageKindGuardrail && m.Guardrail != nil {
			guardCards++
			rawMsg, _ := json.Marshal(m)
			for _, sensitive := range []string{rawStoredSecret, rawStaticSecret, rawPIIEmail, rawCustomAcct} {
				if strings.Contains(string(rawMsg), sensitive) {
					t.Fatalf("sensitive string %q leaked in guardrail card: %s", sensitive, rawMsg)
				}
			}
		}
		if m.Kind == apigen.ChatMessageKindTool && m.Tool != nil && m.Tool.Guardrail != nil {
			guardTools++
			rawMsg, _ := json.Marshal(m)
			for _, sensitive := range []string{rawStoredSecret, rawStaticSecret, rawPIIEmail, rawCustomAcct} {
				if strings.Contains(string(rawMsg), sensitive) {
					t.Fatalf("sensitive string %q leaked in guardrail tool trace: %s", sensitive, rawMsg)
				}
			}
		}
	}
	if guardCards < 2 || guardTools < 2 {
		t.Fatalf("expected >=2 inline guardrail cards and >=2 guardrail tool trace messages, got cards=%d tools=%d", guardCards, guardTools)
	}

	// Verify HasTurnGuardrailBlock recognizes the blocked turn (and returns false for unrelated turns).
	hasBlock, err := h.queries.HasTurnGuardrailBlock(ctx, db.HasTurnGuardrailBlockParams{
		WorkspaceID: pgUUID(ws),
		TurnID:      pgUUID(turnID),
	})
	if err != nil || !hasBlock {
		t.Fatalf("HasTurnGuardrailBlock = %v, err=%v; want true", hasBlock, err)
	}
	noBlock, err := h.queries.HasTurnGuardrailBlock(ctx, db.HasTurnGuardrailBlockParams{
		WorkspaceID: pgUUID(ws),
		TurnID:      pgUUID(uuid.New()),
	})
	if err != nil || noBlock {
		t.Fatalf("HasTurnGuardrailBlock for unrelated turn = %v, err=%v; want false", noBlock, err)
	}
}
