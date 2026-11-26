// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func doRawRequest(t *testing.T, handler http.Handler, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-BotBox-CSRF", "1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestExhaustiveWorkspaceMultiTenancyAndRoleIsolation(t *testing.T) {
	events := &fakeChatEvents{}
	h := newTestHarness(t, nil, harnessOpts{chatEvents: events})

	// Workspace A: owner, member (who owns a separate bot not owned by viewer), and viewer.
	ownerA, _ := h.completeSetup("ownera@example.com", "CorrectHorse123!", "OwnerA", "Workspace A", "ws-a")
	wsA := ownerA.ActiveWorkspace.Id
	baseA := "/api/workspaces/" + wsA.String()

	memberA, _ := h.inviteAndAcceptNewUser(ownerA.AccessToken, wsA, "membera@example.com", "member", "MemberA", "CorrectHorse123!")
	viewerA, _ := h.inviteAndAcceptNewUser(ownerA.AccessToken, wsA, "viewera@example.com", "viewer", "ViewerA", "CorrectHorse123!")

	// Workspace B: ownerB belongs ONLY to Workspace B.
	r := h.request(http.MethodPost, "/api/workspaces", map[string]any{
		"name": "Workspace B", "slug": "ws-b",
	}, ownerA.AccessToken, nil, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create wsB: %d %s", r.status, r.body)
	}
	var wsBResp apigen.WorkspaceSummary
	r.decode(t, &wsBResp)
	wsB := wsBResp.Id
	outsiderB, _ := h.inviteAndAcceptNewUser(ownerA.AccessToken, wsB, "outsiderb@example.com", "owner", "OutsiderB", "CorrectHorse123!")

	// Seed real resources inside Workspace A so cross-workspace requests hit genuine IDs.
	r = h.request(http.MethodPost, baseA+"/bots", map[string]any{
		"name": "BotA", "avatarSeed": "bota-seed", "llmMode": "cli", "llmProvider": "openrouter", "llmModel": "openrouter/fixture",
	}, ownerA.AccessToken, nil, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create botA: %d %s", r.status, r.body)
	}
	var botA apigen.Bot
	r.decode(t, &botA)

	r = h.request(http.MethodPost, baseA+"/bots/"+botA.Id.String()+"/dm", nil, ownerA.AccessToken, nil, true)
	if r.status != http.StatusOK && r.status != http.StatusCreated {
		t.Fatalf("open DM: %d %s", r.status, r.body)
	}
	var convA apigen.ChatConversation
	r.decode(t, &convA)

	r = h.request(http.MethodPost, baseA+"/groups", newGroupBody(uuid.New(), "GroupA", botA.Id, []any{
		groupBot(botA.Id, "lead"),
	}), ownerA.AccessToken, nil, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create groupA: %d %s", r.status, r.body)
	}
	var groupA apigen.Group
	r.decode(t, &groupA)

	r = h.request(http.MethodPost, baseA+"/secrets", map[string]any{
		"name": "WS_TEST", "domainMatch": "api.example.com", "authScheme": "bearer", "value": "secret-val-1234",
	}, ownerA.AccessToken, nil, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create ws secret: %d %s", r.status, r.body)
	}
	var secretA apigen.SecretMetadata
	r.decode(t, &secretA)

	router, ok := h.handler.(chi.Routes)
	if !ok {
		t.Fatal("expected chi.Routes from NewHandlerWithConfig")
	}

	resolvePath := func(pattern string) string {
		repl := strings.NewReplacer(
			"{workspaceID}", wsA.String(),
			"{botID}", botA.Id.String(),
			"{conversationID}", convA.Id.String(),
			"{groupID}", groupA.Id.String(),
			"{userID}", ownerA.User.Id.String(),
			"{invitationID}", uuid.NewString(),
			"{secretID}", secretA.Id.String(),
			"{serverID}", uuid.NewString(),
			"{ruleID}", uuid.NewString(),
			"{memoryID}", uuid.NewString(),
			"{approvalID}", uuid.NewString(),
			"{requestID}", uuid.NewString(),
			"{notificationID}", uuid.NewString(),
			"{messageID}", uuid.NewString(),
			"{turnID}", uuid.NewString(),
			"{actionID}", uuid.NewString(),
		)
		return repl.Replace(pattern)
	}

	isMemberAdminForbidden := func(method, pattern string) bool {
		if pattern == "/api/workspaces/{workspaceID}/" && (method == http.MethodPatch || method == http.MethodDelete) {
			return true
		}
		if strings.HasPrefix(pattern, "/api/workspaces/{workspaceID}/members") && method != http.MethodGet {
			return true
		}
		if strings.HasPrefix(pattern, "/api/workspaces/{workspaceID}/invitations") {
			return true
		}
		if strings.HasPrefix(pattern, "/api/workspaces/{workspaceID}/gitlab-instances") && method != http.MethodGet {
			return true
		}
		if strings.HasPrefix(pattern, "/api/workspaces/{workspaceID}/mcp-servers") && method != http.MethodGet {
			return true
		}
		if strings.HasPrefix(pattern, "/api/workspaces/{workspaceID}/guardrails") && method != http.MethodGet {
			return true
		}
		if strings.HasPrefix(pattern, "/api/workspaces/{workspaceID}/audit-logs") {
			return true
		}
		if strings.HasPrefix(pattern, "/api/workspaces/{workspaceID}/secrets") && method != http.MethodGet {
			return true
		}
		if strings.HasPrefix(pattern, "/api/workspaces/{workspaceID}/budgets") && method != http.MethodGet {
			return true
		}
		if strings.HasPrefix(pattern, "/api/workspaces/{workspaceID}/model-prices") && method != http.MethodGet {
			return true
		}
		if strings.HasPrefix(pattern, "/api/workspaces/{workspaceID}/subscription-") {
			return true
		}
		return false
	}

	// Read-only routes (plus view-only VNC ticket and user's own read markers) permitted for viewer.
	isViewerAllowed := func(method, pattern string) bool {
		if pattern == "/api/workspaces/{workspaceID}/bots/{botID}/vnc-ticket" && method == http.MethodPost {
			return true
		}
		if method == http.MethodPost && (strings.HasPrefix(pattern, "/api/workspaces/{workspaceID}/notifications") || strings.HasSuffix(pattern, "/read")) {
			return true
		}
		if method != http.MethodGet {
			return false
		}
		// Invitations are admin/owner only.
		if strings.HasPrefix(pattern, "/api/workspaces/{workspaceID}/invitations") {
			return false
		}
		// Unresolved subscription usage is admin/owner only.
		if strings.HasPrefix(pattern, "/api/workspaces/{workspaceID}/subscription-usage") {
			return false
		}
		// Audit logs are admin/owner only.
		if strings.HasPrefix(pattern, "/api/workspaces/{workspaceID}/audit-logs") {
			return false
		}
		return true
	}

	visited := 0
	walkErr := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/workspaces/{workspaceID}") {
			return nil
		}
		visited++
		target := resolvePath(route)
		var reqBody any
		if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
			reqBody = map[string]any{}
		}

		// 1. Unauthenticated must return 401.
		unauth := h.request(method, target, reqBody, "", nil, true)
		if unauth.status != http.StatusUnauthorized {
			t.Errorf("unauth %s %s -> %d, want 401 (%s)", method, route, unauth.status, unauth.body)
		}

		// 2. Missing CSRF header on mutating routes must return 403.
		if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete {
			noCSRF := h.request(method, target, reqBody, ownerA.AccessToken, nil, false)
			if noCSRF.status != http.StatusForbidden {
				t.Errorf("no-csrf %s %s -> %d, want 403 (%s)", method, route, noCSRF.status, noCSRF.body)
			}
		}

		// 3. Cross-workspace user (outsiderB) must ALWAYS return 404.
		cross := h.request(method, target, reqBody, outsiderB.AccessToken, nil, true)
		if cross.status != http.StatusNotFound {
			t.Errorf("cross-workspace %s %s -> %d, want 404 (%s)", method, route, cross.status, cross.body)
		}

		// 4. Viewer in Workspace A cannot mutate or read admin-only routes (403, or 404 on ownerA's private DM / non-member group).
		if !isViewerAllowed(method, route) {
			vRes := h.request(method, target, reqBody, viewerA.AccessToken, nil, true)
			if vRes.status != http.StatusForbidden && vRes.status != http.StatusNotFound {
				t.Errorf("viewer %s %s -> %d, want 403/404 (%s)", method, route, vRes.status, vRes.body)
			}
		}

		// 5. Member in Workspace A cannot invoke admin/owner routes (403).
		if isMemberAdminForbidden(method, route) {
			mRes := h.request(method, target, reqBody, memberA.AccessToken, nil, true)
			if mRes.status != http.StatusForbidden {
				t.Errorf("member %s %s -> %d, want 403 (%s)", method, route, mRes.status, mRes.body)
			}
		}

		return nil
	})
	if walkErr != nil {
		t.Fatalf("chi.Walk failed: %v", walkErr)
	}
	if visited < 50 {
		t.Fatalf("expected at least 50 workspace routes walked, got %d", visited)
	}

	// Explicit checks for terminal exec and WebSocket channels (VNC + Chat).
	termRes := h.request(http.MethodPost, baseA+"/bots/"+botA.Id.String()+"/terminal/exec", map[string]any{"command": "id"}, outsiderB.AccessToken, nil, true)
	if termRes.status != http.StatusNotFound {
		t.Fatalf("cross-workspace terminal exec = %d, want 404", termRes.status)
	}
	termMember := h.request(http.MethodPost, baseA+"/bots/"+botA.Id.String()+"/terminal/exec", map[string]any{"command": "id"}, memberA.AccessToken, nil, true)
	if termMember.status != http.StatusForbidden {
		t.Fatalf("non-owner member terminal exec = %d, want 403", termMember.status)
	}

	// Seed model price so BotA environment can start, then test VNC ticket & relay paths.
	priceRes := h.request(http.MethodPut, baseA+"/model-prices", map[string]any{
		"provider":               "openrouter",
		"model":                  "openrouter/fixture",
		"inputMicrosPerMillion":  1000000,
		"outputMicrosPerMillion": 1000000,
		"cacheMicrosPerMillion":  1000000,
	}, ownerA.AccessToken, nil, true)
	if priceRes.status != http.StatusOK {
		t.Fatalf("seed model price: %d %s", priceRes.status, priceRes.body)
	}
	startRes := h.request(http.MethodPost, baseA+"/bots/"+botA.Id.String()+"/environment/start", nil, ownerA.AccessToken, nil, true)
	if startRes.status != http.StatusOK {
		t.Fatalf("start env: %d %s", startRes.status, startRes.body)
	}
	crossTicket := h.request(http.MethodPost, baseA+"/bots/"+botA.Id.String()+"/vnc-ticket", nil, outsiderB.AccessToken, nil, true)
	if crossTicket.status != http.StatusNotFound {
		t.Fatalf("cross-workspace VNC ticket = %d, want 404", crossTicket.status)
	}

	ticketRes := h.request(http.MethodPost, baseA+"/bots/"+botA.Id.String()+"/vnc-ticket", nil, ownerA.AccessToken, nil, true)
	if ticketRes.status != http.StatusOK {
		t.Fatalf("owner VNC ticket = %d %s", ticketRes.status, ticketRes.body)
	}
	var ticket apigen.StreamTicketResponse
	ticketRes.decode(t, &ticket)

	srv := httptest.NewServer(h.handler)
	defer srv.Close()
	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Cross-workspace VNC path with valid ticket for wsA must fail (404).
	_, vncCrossResp, err := websocket.Dial(ctx, wsBase+"/ws/bots/"+wsB.String()+"/"+botA.Id.String()+"/vnc?ticket="+ticket.Ticket, nil)
	if err == nil || vncCrossResp == nil || vncCrossResp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-workspace VNC ws = %v err=%v, want 404", vncCrossResp, err)
	}
	if vncCrossResp.Body != nil {
		_ = vncCrossResp.Body.Close()
	}

	// Disallowed Origin on VNC WebSocket must fail (403).
	_, vncOriginResp, err := websocket.Dial(ctx, wsBase+"/ws/bots/"+wsA.String()+"/"+botA.Id.String()+"/vnc?ticket="+ticket.Ticket, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {"https://evil.example"}},
	})
	if err == nil || vncOriginResp == nil || vncOriginResp.StatusCode != http.StatusForbidden {
		t.Fatalf("evil origin VNC ws = %v err=%v, want 403", vncOriginResp, err)
	}
	if vncOriginResp.Body != nil {
		_ = vncOriginResp.Body.Close()
	}

	// Cross-workspace Chat WebSocket must fail (404), and disallowed Origin must fail (403).
	_, chatCrossResp, err := websocket.Dial(ctx, wsBase+"/ws/chat/"+wsA.String()+"/"+convA.Id.String(), &websocket.DialOptions{
		Subprotocols: []string{"botbox.events", "botbox.auth." + outsiderB.AccessToken},
	})
	if err == nil || chatCrossResp == nil || chatCrossResp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-workspace chat ws = %v err=%v, want 404", chatCrossResp, err)
	}
	if chatCrossResp.Body != nil {
		_ = chatCrossResp.Body.Close()
	}
}
