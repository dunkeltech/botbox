package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/dunkeltech/botbox/backend/internal/api"
	"github.com/dunkeltech/botbox/backend/internal/compute"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
)

func TestPhase2EnvironmentLifecycleFilesTerminalControlAndRBAC(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t, nil, harnessOpts{})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Alex", "Acme Bots", "acme")
	wsID := owner.ActiveWorkspace.Id.String()

	// Create a bot owned by Alex.
	createBotResp := h.request(http.MethodPost, "/api/workspaces/"+wsID+"/bots", map[string]any{
		"name":        "Atlas",
		"description": "Ops runner",
		"avatarSeed":  "atlas-01",
		"llmMode":     "cli",
		"llmProvider": "anthropic",
		"llmModel":    "claude-sonnet-4",
	}, owner.AccessToken, nil, true)
	if createBotResp.status != http.StatusCreated {
		t.Fatalf("create bot: status=%d body=%s", createBotResp.status, createBotResp.body)
	}
	var bot apigen.Bot
	createBotResp.decode(t, &bot)
	botID := bot.Id.String()
	baseURL := "/api/workspaces/" + wsID + "/bots/" + botID

	// Invite a member (Mia), a viewer (Sam), and provision an isolated second workspace (Other Org) with Eve.
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, owner.ActiveWorkspace.Id, "mia@example.com", "member", "Mia", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, owner.ActiveWorkspace.Id, "sam@example.com", "viewer", "Sam", "CorrectHorse123!")

	otherWsResp := h.request(http.MethodPost, "/api/workspaces", map[string]any{
		"name": "Other Org",
		"slug": "other-org",
	}, owner.AccessToken, nil, true)
	if otherWsResp.status != http.StatusCreated {
		t.Fatalf("create other workspace: status=%d body=%s", otherWsResp.status, otherWsResp.body)
	}
	var otherWs apigen.WorkspaceSummary
	otherWsResp.decode(t, &otherWs)
	eve, _ := h.inviteAndAcceptNewUser(owner.AccessToken, otherWs.Id, "eve@other.com", "owner", "Eve", "CorrectHorse123!")

	// Unauthenticated -> 401
	unauthResp := h.request(http.MethodGet, baseURL+"/environment", nil, "", nil, false)
	if unauthResp.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /environment status = %d, want 401", unauthResp.status)
	}

	// Initial environment state is stopped.
	envResp := h.request(http.MethodGet, baseURL+"/environment", nil, owner.AccessToken, nil, false)
	if envResp.status != http.StatusOK {
		t.Fatalf("GET /environment status=%d body=%s", envResp.status, envResp.body)
	}
	var env apigen.EnvironmentStateResponse
	envResp.decode(t, &env)
	if env.Status != "stopped" || env.ControlHolder != nil {
		t.Fatalf("initial env = %+v, want stopped and no controlHolder", env)
	}

	// Operations while stopped fail with 409 Conflict.
	if r := h.request(http.MethodPost, baseURL+"/vnc-ticket", nil, owner.AccessToken, nil, true); r.status != http.StatusConflict {
		t.Fatalf("vnc-ticket while stopped status=%d, want 409", r.status)
	}
	if r := h.request(http.MethodPost, baseURL+"/terminal/exec", map[string]any{"command": "echo hi"}, owner.AccessToken, nil, true); r.status != http.StatusConflict {
		t.Fatalf("terminal/exec while stopped status=%d, want 409", r.status)
	}

	// Viewer cannot start sandbox -> 403.
	if r := h.request(http.MethodPost, baseURL+"/environment/start", nil, viewer.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("viewer start status=%d, want 403", r.status)
	}

	// Cross-workspace user cannot probe or access bot -> 404 across all Phase 2 endpoints.
	for _, ep := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, baseURL + "/environment", nil},
		{http.MethodPost, baseURL + "/environment/start", nil},
		{http.MethodPost, baseURL + "/environment/stop", nil},
		{http.MethodPost, baseURL + "/environment/restart", nil},
		{http.MethodPost, baseURL + "/control/take", nil},
		{http.MethodPost, baseURL + "/control/release", nil},
		{http.MethodPost, baseURL + "/vnc-ticket", nil},
		{http.MethodPost, baseURL + "/terminal/exec", map[string]any{"command": "pwd"}},
		{http.MethodGet, baseURL + "/files", nil},
		{http.MethodGet, baseURL + "/files/content?path=/home/bot/notes.txt", nil},
		{http.MethodPut, baseURL + "/files/content", map[string]any{"path": "/home/bot/notes.txt", "content": "nope"}},
	} {
		if r := h.request(ep.method, ep.path, ep.body, eve.AccessToken, nil, ep.method != http.MethodGet); r.status != http.StatusNotFound {
			t.Fatalf("cross-workspace %s %s status=%d, want 404 (body=%s)", ep.method, ep.path, r.status, r.body)
		}
	}

	// Owner starts sandbox -> status running, bot reflects idle.
	startResp := h.request(http.MethodPost, baseURL+"/environment/start", nil, owner.AccessToken, nil, true)
	if startResp.status != http.StatusOK {
		t.Fatalf("start environment status=%d body=%s", startResp.status, startResp.body)
	}
	startResp.decode(t, &env)
	if env.Status != "running" || env.EnvId == "" || env.CpuMillis != 2000 || env.MemoryMiB != 2048 {
		t.Fatalf("started env unexpected: %+v", env)
	}

	botGet := h.request(http.MethodGet, baseURL, nil, owner.AccessToken, nil, false)
	var updatedBot apigen.Bot
	botGet.decode(t, &updatedBot)
	if updatedBot.Status != apigen.BotStatusIdle || updatedBot.Mood != apigen.AvatarMoodIdle {
		t.Fatalf("running bot status/mood = (%s, %s), want (idle, idle)", updatedBot.Status, updatedBot.Mood)
	}

	// Write a file in /home/bot and verify persistence across stop + start.
	writeResp := h.request(http.MethodPut, baseURL+"/files/content", map[string]any{
		"path":    "/home/bot/notes/todo.txt",
		"content": "ship phase 2",
	}, owner.AccessToken, nil, true)
	if writeResp.status != http.StatusOK {
		t.Fatalf("write file status=%d body=%s", writeResp.status, writeResp.body)
	}

	// Terminal exec inside /home/bot.
	termResp := h.request(http.MethodPost, baseURL+"/terminal/exec", map[string]any{
		"command": "echo hello-sandbox && cat notes/todo.txt",
	}, owner.AccessToken, nil, true)
	if termResp.status != http.StatusOK {
		t.Fatalf("terminal/exec status=%d body=%s", termResp.status, termResp.body)
	}
	var termOut apigen.TerminalExecResponse
	termResp.decode(t, &termOut)
	if termOut.ExitCode != 0 || !strings.Contains(termOut.Stdout, "hello-sandbox") || !strings.Contains(termOut.Stdout, "ship phase 2") {
		t.Fatalf("unexpected terminal output: %+v", termOut)
	}

	// Path traversal negative checks -> 400.
	if r := h.request(http.MethodGet, baseURL+"/files?path=../etc", nil, owner.AccessToken, nil, false); r.status != http.StatusBadRequest {
		t.Fatalf("list path traversal status=%d, want 400", r.status)
	}
	if r := h.request(http.MethodGet, baseURL+"/files/content?path=/etc/passwd", nil, owner.AccessToken, nil, false); r.status != http.StatusBadRequest {
		t.Fatalf("read path traversal status=%d, want 400", r.status)
	}
	if r := h.request(http.MethodPost, baseURL+"/terminal/exec", map[string]any{"command": "pwd", "workdir": "../etc"}, owner.AccessToken, nil, true); r.status != http.StatusBadRequest {
		t.Fatalf("terminal workdir escape status=%d, want 400", r.status)
	}

	// Viewer can list/read files, but cannot write files or run terminal commands -> 403.
	if r := h.request(http.MethodGet, baseURL+"/files?path=/home/bot/notes", nil, viewer.AccessToken, nil, false); r.status != http.StatusOK {
		t.Fatalf("viewer list files status=%d, want 200", r.status)
	}
	if r := h.request(http.MethodPut, baseURL+"/files/content", map[string]any{"path": "/home/bot/bad.txt", "content": "x"}, viewer.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("viewer write file status=%d, want 403", r.status)
	}
	if r := h.request(http.MethodPost, baseURL+"/terminal/exec", map[string]any{"command": "echo nope"}, viewer.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("viewer exec status=%d, want 403", r.status)
	}

	// Restart keeps /home/bot/notes/todo.txt intact.
	restartResp := h.request(http.MethodPost, baseURL+"/environment/restart", nil, owner.AccessToken, nil, true)
	if restartResp.status != http.StatusOK {
		t.Fatalf("restart status=%d body=%s", restartResp.status, restartResp.body)
	}
	readAfterRestart := h.request(http.MethodGet, baseURL+"/files/content?path=/home/bot/notes/todo.txt", nil, owner.AccessToken, nil, false)
	if readAfterRestart.status != http.StatusOK {
		t.Fatalf("read after restart status=%d body=%s", readAfterRestart.status, readAfterRestart.body)
	}
	var fileContent apigen.FileContentResponse
	readAfterRestart.decode(t, &fileContent)
	if fileContent.Content != "ship phase 2" {
		t.Fatalf("persisted content = %q, want %q", fileContent.Content, "ship phase 2")
	}

	// Control lock lifecycle: viewer -> 403, owner claims -> member gets 409, VNC tickets reflect lock!
	if r := h.request(http.MethodPost, baseURL+"/control/take", nil, viewer.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("viewer take control status=%d, want 403", r.status)
	}

	takeOwner := h.request(http.MethodPost, baseURL+"/control/take", nil, owner.AccessToken, nil, true)
	if takeOwner.status != http.StatusOK {
		t.Fatalf("owner take control status=%d body=%s", takeOwner.status, takeOwner.body)
	}
	takeOwner.decode(t, &env)
	if env.ControlHolder == nil || env.ControlHolder.UserId != owner.User.Id {
		t.Fatalf("controlHolder = %+v, want owner %s", env.ControlHolder, owner.User.Id)
	}

	// Second member trying to take control receives 409 Conflict.
	if r := h.request(http.MethodPost, baseURL+"/control/take", nil, member.AccessToken, nil, true); r.status != http.StatusConflict {
		t.Fatalf("member take control while locked status=%d, want 409", r.status)
	}

	// Issue VNC tickets for owner vs member and verify live WebSocket RFB connection works.
	ownerTicketResp := h.request(http.MethodPost, baseURL+"/vnc-ticket", nil, owner.AccessToken, nil, true)
	memberTicketResp := h.request(http.MethodPost, baseURL+"/vnc-ticket", nil, member.AccessToken, nil, true)
	if ownerTicketResp.status != http.StatusOK || memberTicketResp.status != http.StatusOK {
		t.Fatalf("ticket statuses = (%d, %d)", ownerTicketResp.status, memberTicketResp.status)
	}
	var ownerTicket, memberTicket apigen.StreamTicketResponse
	ownerTicketResp.decode(t, &ownerTicket)
	memberTicketResp.decode(t, &memberTicket)
	if !ownerTicket.CanControl || memberTicket.CanControl {
		t.Fatalf("ticket control flags owner=%v member=%v, want (true, false)", ownerTicket.CanControl, memberTicket.CanControl)
	}

	// Verify VNC WebSocket endpoint upgrades over HTTP and completes RFB banner handshake.
	httpSrv := httptest.NewServer(h.handler)
	defer httpSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + memberTicket.WsPath + "?ticket=" + memberTicket.Ticket
	wsConn, wsResp, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{Subprotocols: []string{"binary"}})
	if wsResp != nil && wsResp.Body != nil {
		_ = wsResp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial VNC relay websocket: %v", err)
	}
	_, banner, err := wsConn.Read(context.Background())
	if err != nil || string(banner) != "RFB 003.008\n" {
		t.Fatalf("unexpected RFB banner (%q, %v)", string(banner), err)
	}
	_ = wsConn.Close(websocket.StatusNormalClosure, "done")

	// Member cannot release Owner's lock -> 409; Owner releases with hand-back note.
	if r := h.request(http.MethodPost, baseURL+"/control/release", map[string]any{"note": "hijack"}, member.AccessToken, nil, true); r.status != http.StatusConflict {
		t.Fatalf("member release locked control status=%d, want 409", r.status)
	}
	releaseResp := h.request(http.MethodPost, baseURL+"/control/release", map[string]any{"note": "logged into dashboard"}, owner.AccessToken, nil, true)
	if releaseResp.status != http.StatusOK {
		t.Fatalf("owner release control status=%d body=%s", releaseResp.status, releaseResp.body)
	}
	var releasedEnv apigen.EnvironmentStateResponse
	releaseResp.decode(t, &releasedEnv)
	if releasedEnv.ControlHolder != nil || releasedEnv.ControlNote != "logged into dashboard" {
		t.Fatalf("after release env = %+v", releasedEnv)
	}

	// After release, member can now take control!
	if r := h.request(http.MethodPost, baseURL+"/control/take", nil, member.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("member take control after release status=%d body=%s", r.status, r.body)
	}

	// Test startup reconcile detects a container missing from the compute driver.
	if err := h.computeDrv.DestroyEnvironment(context.Background(), compute.EnvID(releasedEnv.EnvId)); err != nil {
		t.Fatalf("DestroyEnvironment for reconcile test: %v", err)
	}
	if err := api.ReconcileEnvironments(context.Background(), h.pool, h.computeDrv, h.logger); err != nil {
		t.Fatalf("ReconcileEnvironments: %v", err)
	}
	afterReconcile := h.request(http.MethodGet, baseURL+"/environment", nil, owner.AccessToken, nil, false)
	var reconciledEnv apigen.EnvironmentStateResponse
	afterReconcile.decode(t, &reconciledEnv)
	if reconciledEnv.Status != "stopped" {
		t.Fatalf("status after reconcile = %q, want stopped", reconciledEnv.Status)
	}

	// Finally, stop environment explicitly when stopped is idempotent 200.
	if r := h.request(http.MethodPost, baseURL+"/environment/stop", nil, owner.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("stop environment status=%d, want 200", r.status)
	}
}
