// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/authbroker"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/secretrequests"
	"github.com/google/uuid"
)

func TestBrowserBrokerEndToEndTakeoverRBACAndIsolation(t *testing.T) {
	ctx := context.Background()
	eng := authbroker.NewMemoryBrowserEngine()
	svc := authbroker.NewService(eng)
	brokerHandler, err := authbroker.NewHTTPHandler(svc, "integration-broker-token-min16")
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	brokerSrv := httptest.NewServer(brokerHandler)
	defer brokerSrv.Close()

	brokerClient, err := authbroker.NewHTTPClient(brokerSrv.URL, "integration-broker-token-min16")
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}

	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{
		turnQueue:  queue,
		chatLimit:  500,
		authBroker: brokerClient,
	})
	svc.SetNow(h.clock.Now)

	owner, _ := h.completeSetup("owner-bb@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()

	atlas := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas-bb")
	admin, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "admin-bb@example.com", "admin", "Admin", "CorrectHorse123!")
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member-bb@example.com", "member", "Member", "CorrectHorse123!")
	outsider, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "outsider-bb@example.com", "member", "Outsider", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "viewer-bb@example.com", "viewer", "Viewer", "CorrectHorse123!")

	otherResp := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "OtherBB", "slug": "other-bb"}, owner.AccessToken, nil, true)
	var otherWS apigen.WorkspaceSummary
	otherResp.decode(t, &otherWS)
	eve, _ := h.inviteAndAcceptNewUser(owner.AccessToken, otherWS.Id, "eve-bb@other.com", "admin", "Eve", "CorrectHorse123!")

	const webPassword = "web-password-never-leak-5544"

	var execMu sync.Mutex
	var resumed []string
	worker := &engine.Worker{
		Pool:       h.pool,
		Now:        h.clock.Now,
		AuthBroker: brokerClient,
		Enqueue:    queue.Enqueue,
		Execute: func(_ context.Context, s engine.Session, emit func(engine.Event) error) error {
			execMu.Lock()
			if s.ContinuationPrompt != "" {
				resumed = append(resumed, s.ContinuationPrompt)
			}
			execMu.Unlock()

			if s.ContinuationPrompt != "" {
				return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "Logged in cleanly via broker"})
			}
			if len(s.Messages) > 0 && strings.Contains(s.Messages[0].Content, "login-mfa") {
				return emit(engine.Event{
					Kind:      engine.EventKindToolCall,
					ToolID:    "call_bb_1",
					ToolName:  "mcp__botbox-desktop__request_secret",
					ToolInput: []byte(`{"kind":"browser_login","adapter_id":"github_web","reason":"Sign in to GitHub Issues"}`),
				})
			}
			if len(s.Messages) > 0 && strings.Contains(s.Messages[0].Content, "login-race") {
				return emit(engine.Event{
					Kind:      engine.EventKindToolCall,
					ToolID:    "call_bb_2",
					ToolName:  "mcp__botbox-desktop__request_secret",
					ToolInput: []byte(`{"kind":"browser_login","adapter_id":"gitlab_web","reason":"Sign in to GitLab"}`),
				})
			}
			return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "Idle"})
		},
	}

	// Create a group containing member, admin, and viewer (outsider is NOT a member).
	grp := askGroup(t, h, base, owner.AccessToken, "BrokerOps", atlas.Id, false, 20, []any{}, []any{
		groupUser(member.User.Id, "operator"),
		groupUser(admin.User.Id, "reviewer"),
		groupUser(viewer.User.Id, "observer"),
		groupBot(atlas.Id, "lead"),
	})

	postGroup(t, h, base, member.AccessToken, grp.Id, "@Atlas please login-mfa now")
	drainTurns(t, worker, queue, 2)

	var srSummary *apigen.SecretRequestSummary
	histResp := h.request(http.MethodGet, base+"/groups/"+grp.Id.String()+"/messages", nil, member.AccessToken, nil, false)
	var hist apigen.ChatHistory
	histResp.decode(t, &hist)
	for _, m := range hist.Messages {
		if m.Kind == apigen.ChatMessageKindSecretRequest && m.SecretRequest != nil {
			srSummary = m.SecretRequest
		}
	}
	if srSummary == nil || srSummary.Kind != apigen.BrowserLogin {
		t.Fatalf("expected browser_login secret request card, got %+v", srSummary)
	}
	if srSummary.BrokerEnabled == nil || !*srSummary.BrokerEnabled || srSummary.BrokerSessionId == nil {
		t.Fatalf("expected brokerEnabled=true and brokerSessionId on card, got %+v", srSummary)
	}
	reqID := srSummary.SecretRequestId.String()
	sessID := *srSummary.BrokerSessionId

	// Viewer -> 403; outsider / cross-workspace -> 404.
	if r := h.request(http.MethodPost, base+"/secret-requests/"+reqID+"/fulfill", map[string]any{"secretValue": webPassword, "scope": "ephemeral"}, viewer.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer fulfill, got %d", r.status)
	}
	if r := h.request(http.MethodPost, base+"/secret-requests/"+reqID+"/broker-takeover/start", nil, viewer.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer takeover start, got %d", r.status)
	}
	if r := h.request(http.MethodPost, base+"/secret-requests/"+reqID+"/broker-takeover/start", nil, outsider.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("expected 404 for non-member takeover start, got %d", r.status)
	}
	if r := h.request(http.MethodPost, "/api/workspaces/"+otherWS.Id.String()+"/secret-requests/"+reqID+"/broker-takeover/start", nil, eve.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-workspace takeover start, got %d", r.status)
	}
	// Reject proxy header scopes for browser_login.
	if r := h.request(http.MethodPost, base+"/secret-requests/"+reqID+"/fulfill", map[string]any{"secretValue": webPassword, "scope": "bot_secret"}, member.AccessToken, nil, true); r.status != http.StatusBadRequest {
		t.Fatalf("expected 400 when requesting bot_secret proxy rule on browser_login, got %d", r.status)
	}

	// Simulate MFA challenge on submission: request returns 200 mfaRequired=true and turn stays paused.
	eng.SetMFARequired(sessID, true)
	subResp := h.request(http.MethodPost, base+"/secret-requests/"+reqID+"/fulfill", map[string]any{
		"secretValue": webPassword,
		"username":    "alice",
		"scope":       "ephemeral",
	}, member.AccessToken, nil, true)
	if subResp.status != http.StatusOK {
		t.Fatalf("expected 200 fulfilling password step into MFA, got %d (%s)", subResp.status, subResp.body)
	}
	var subRes apigen.SecretRequestResult
	subResp.decode(t, &subRes)
	if subRes.MfaRequired == nil || !*subRes.MfaRequired || subRes.SecretRequest.Status != apigen.SecretRequestStatusPending {
		t.Fatalf("expected mfaRequired=true and pending status, got %+v", subRes)
	}
	if len(queue.jobs) != 0 {
		t.Fatalf("turn should remain paused until MFA takeover finishes, queued=%d", len(queue.jobs))
	}

	// Member starts dedicated human-only broker takeover.
	tkResp := h.request(http.MethodPost, base+"/secret-requests/"+reqID+"/broker-takeover/start", nil, member.AccessToken, nil, true)
	if tkResp.status != http.StatusOK {
		t.Fatalf("expected 200 starting takeover, got %d (%s)", tkResp.status, tkResp.body)
	}
	var tkState apigen.BrokerTakeoverState
	tkResp.decode(t, &tkState)
	if !tkState.TakeoverActive || tkState.TakeoverTicket == nil || *tkState.TakeoverTicket == "" {
		t.Fatalf("expected active takeover and signed ticket, got %+v", tkState)
	}

	// During takeover, ALL bot calls & captures on the broker are blocked with ErrTakeoverActive.
	if _, err := brokerClient.BotRead(ctx, ws, atlas.Id, sessID, "body"); !errors.Is(err, authbroker.ErrTakeoverActive) {
		t.Fatalf("expected ErrTakeoverActive during BotRead, got %v", err)
	}
	if _, _, err := brokerClient.BotScreenshot(ctx, ws, atlas.Id, sessID); !errors.Is(err, authbroker.ErrTakeoverActive) {
		t.Fatalf("expected ErrTakeoverActive during BotScreenshot, got %v", err)
	}

	// Workspace admin (another user!) cannot view frame, send input, start takeover, or complete member's takeover.
	if r := h.request(http.MethodGet, base+"/secret-requests/"+reqID+"/broker-takeover/frame?ticket="+*tkState.TakeoverTicket, nil, admin.AccessToken, nil, false); r.status != http.StatusForbidden {
		t.Fatalf("expected 403 for admin reading member takeover frame, got %d", r.status)
	}
	if r := h.request(http.MethodPost, base+"/secret-requests/"+reqID+"/broker-takeover/input", map[string]any{"kind": "key", "key": "Enter"}, admin.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("expected 403 for admin sending takeover input, got %d", r.status)
	}
	if r := h.request(http.MethodPost, base+"/secret-requests/"+reqID+"/broker-takeover/complete", nil, admin.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("expected 403 for admin completing takeover, got %d", r.status)
	}

	// Cross-domain sandbox VNC ticket (and missing ticket) cannot authorize broker takeover frame.
	if r := h.request(http.MethodPost, base+"/bots/"+atlas.Id.String()+"/environment/start", nil, owner.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("expected 200 starting sandbox environment, got %d (%s)", r.status, r.body)
	}
	vncResp := h.request(http.MethodPost, base+"/bots/"+atlas.Id.String()+"/vnc-ticket", nil, member.AccessToken, nil, true)
	if vncResp.status != http.StatusOK {
		t.Fatalf("expected 200 issuing sandbox vnc ticket, got %d (%s)", vncResp.status, vncResp.body)
	}
	var vncTicket apigen.StreamTicketResponse
	vncResp.decode(t, &vncTicket)
	if r := h.request(http.MethodGet, base+"/secret-requests/"+reqID+"/broker-takeover/frame?ticket="+vncTicket.Ticket, nil, member.AccessToken, nil, false); r.status != http.StatusForbidden {
		t.Fatalf("expected 403 when using sandbox VNC ticket on broker takeover, got %d", r.status)
	}
	if r := h.request(http.MethodGet, base+"/secret-requests/"+reqID+"/broker-takeover/frame", nil, member.AccessToken, nil, false); r.status != http.StatusForbidden {
		t.Fatalf("expected 403 when omitting takeover ticket on frame endpoint, got %d", r.status)
	}

	// Authorized owner fetches frame, completes MFA with Enter key, and hands back.
	frameResp := h.request(http.MethodGet, base+"/secret-requests/"+reqID+"/broker-takeover/frame?ticket="+*tkState.TakeoverTicket, nil, member.AccessToken, nil, false)
	if frameResp.status != http.StatusOK || len(frameResp.body) == 0 {
		t.Fatalf("expected 200 PNG frame, got %d (len=%d)", frameResp.status, len(frameResp.body))
	}
	inResp := h.request(http.MethodPost, base+"/secret-requests/"+reqID+"/broker-takeover/input", map[string]any{"kind": "key", "key": "Enter"}, member.AccessToken, nil, true)
	if inResp.status != http.StatusOK {
		t.Fatalf("expected 200 on takeover input, got %d (%s)", inResp.status, inResp.body)
	}
	compResp := h.request(http.MethodPost, base+"/secret-requests/"+reqID+"/broker-takeover/complete", nil, member.AccessToken, nil, true)
	if compResp.status != http.StatusOK {
		t.Fatalf("expected 200 on complete takeover, got %d (%s)", compResp.status, compResp.body)
	}
	var compRes apigen.SecretRequestResult
	compResp.decode(t, &compRes)
	if compRes.SecretRequest.Status != apigen.SecretRequestStatusFulfilled {
		t.Fatalf("expected fulfilled status, got %s", compRes.SecretRequest.Status)
	}

	// Continuation turn runs and receives only broker session ID, never credentials.
	drainTurns(t, worker, queue, 2)
	if len(resumed) == 0 || strings.Contains(resumed[len(resumed)-1], webPassword) {
		t.Fatalf("unexpected continuation prompt: %v", resumed)
	}

	// Post-login bot sanitized read & screenshot now succeed without secret leaks.
	readRes, err := brokerClient.BotRead(ctx, ws, atlas.Id, sessID, "body")
	if err != nil || strings.Contains(readRes.Text, webPassword) {
		t.Fatalf("unexpected BotRead result: err=%v text=%q", err, readRes.Text)
	}
	if _, _, err := brokerClient.BotScreenshot(ctx, ws, atlas.Id, sessID); err != nil {
		t.Fatalf("expected BotScreenshot to succeed after takeover completion, got %v", err)
	}

	// Tampered origin/tab before fulfill aborts 409 and revokes broker session cleanly.
	postGroup(t, h, base, member.AccessToken, grp.Id, "@Atlas please login-race now")
	drainTurns(t, worker, queue, 2)
	var raceSR *apigen.SecretRequestSummary
	histResp2 := h.request(http.MethodGet, base+"/groups/"+grp.Id.String()+"/messages", nil, member.AccessToken, nil, false)
	var hist2 apigen.ChatHistory
	histResp2.decode(t, &hist2)
	for _, m := range hist2.Messages {
		if m.Kind == apigen.ChatMessageKindSecretRequest && m.SecretRequest != nil && m.SecretRequest.AdapterId == "gitlab_web" {
			raceSR = m.SecretRequest
		}
	}
	if raceSR == nil || raceSR.BrokerSessionId == nil {
		t.Fatalf("expected second browser_login request, got %+v", raceSR)
	}
	eng.SwitchActiveTab(*raceSR.BrokerSessionId, "https://phish.example:443", "https://phish.example/steal")
	racePayload := map[string]any{ //nolint:gosec // synthetic test fixture
		"secretValue": "gitlab-race-secret-never-land",
		"scope":       "ephemeral",
	}
	if r := h.request(http.MethodPost, base+"/secret-requests/"+raceSR.SecretRequestId.String()+"/fulfill", racePayload, member.AccessToken, nil, true); r.status != http.StatusConflict {
		t.Fatalf("expected 409 conflict on switched tab before fill, got %d", r.status)
	}
	if !eng.StoredPasswordEmpty(*raceSR.BrokerSessionId) {
		t.Fatal("secret stored in broker tab despite binding mismatch")
	}

	// Verify zero leakage across messages, audit_log, and broker_sessions.
	for _, query := range []struct {
		name string
		sql  string
	}{
		{"messages", "SELECT count(*) FROM messages WHERE content LIKE $1 OR metadata::text LIKE $1"},
		{"audit_log", "SELECT count(*) FROM audit_log WHERE metadata::text LIKE $1"},
		{"bot_turns", "SELECT count(*) FROM bot_turns WHERE continuation_prompt LIKE $1"},
	} {
		var cnt int64
		if err := h.pool.QueryRow(ctx, query.sql, "%"+webPassword+"%").Scan(&cnt); err != nil {
			t.Fatalf("%s scan error: %v", query.name, err)
		}
		if cnt != 0 {
			t.Fatalf("secret value leaked in %s (%d rows)", query.name, cnt)
		}
	}

	// Deleting a bot revokes its active isolated broker sessions in the external broker.
	beta := createPiBot(t, h, base, owner.AccessToken, "Beta", "beta-bb")
	betaSess, err := brokerClient.OpenSession(ctx, authbroker.OpenSessionInput{
		WorkspaceID: ws,
		BotID:       beta.Id,
		AdapterID:   "odoo_staging",
	})
	if err != nil {
		t.Fatalf("OpenSession for beta failed: %v", err)
	}
	if r := h.request(http.MethodDelete, base+"/bots/"+beta.Id.String(), nil, owner.AccessToken, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("expected 204 deleting bot, got %d (%s)", r.status, r.body)
	}
	if _, err := brokerClient.GetSession(ctx, ws, betaSess.SessionID); !errors.Is(err, authbroker.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound after bot delete, got %v", err)
	}
}

func TestBrowserLoginCapabilityDisabledWithoutBroker(t *testing.T) {
	ctx := context.Background()
	h := newTestHarness(t, nil, harnessOpts{turnQueue: &fakeTurnQueue{}, chatLimit: 500})
	owner, _ := h.completeSetup("owner-nobroker@example.com", "CorrectHorse123!", "Owner", "WS", "ws")
	ws := owner.ActiveWorkspace.Id
	atlas := createPiBot(t, h, "/api/workspaces/"+ws.String(), owner.AccessToken, "Atlas", "atlas-nb")

	_, err := secretrequests.CreateRequest(ctx, h.pool, secretrequests.CreateInput{
		WorkspaceID:    ws,
		BotID:          atlas.Id,
		ConversationID: uuid.New(),
		TurnID:         uuid.New(),
		Kind:           secretrequests.KindBrowserLogin,
		AdapterID:      "github_web",
		Reason:         "Attempt login without broker",
	})
	if !errors.Is(err, secretrequests.ErrUnsupportedCapability) {
		t.Fatalf("expected ErrUnsupportedCapability when broker is nil, got %v", err)
	}

	r := h.request(http.MethodPost, "/api/workspaces/"+ws.String()+"/secret-requests/"+uuid.New().String()+"/broker-takeover/start", nil, owner.AccessToken, nil, true)
	if r.status != http.StatusNotFound {
		t.Fatalf("expected 404 on nonexistent request before capability, got %d", r.status)
	}
}
