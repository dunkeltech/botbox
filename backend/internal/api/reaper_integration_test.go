// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestIdleStopRechecksWorkspaceAndEnvironment(t *testing.T) {
	t.Parallel()
	h := newTestHarness(t, nil, harnessOpts{})
	owner, _ := h.completeSetup("idle-race@example.com", "CorrectHorse123!", "Owner", "Workspace", "idle-race")
	wsID := owner.ActiveWorkspace.Id
	seedTestModelPrice(t, h, owner.AccessToken, wsID, "openrouter", "openrouter/fixture")
	bot, _ := createAndStartBotForTest(t, h, owner.AccessToken, wsID, "IdleCandidate")
	wsPG, botPG := pgtype.UUID{Bytes: wsID, Valid: true}, pgtype.UUID{Bytes: bot.Id, Valid: true}
	h.clock.Advance(2 * time.Hour)
	ctx := t.Context()
	candidates, err := h.queries.ListIdleRunningEnvironments(ctx)
	if err != nil || len(candidates) != 1 || candidates[0].BotID != botPG {
		t.Fatalf("expected one idle candidate: %v, %v", candidates, err)
	}
	asOf := h.clock.Now()
	args := db.MarkIdleEnvironmentStoppedParams{
		WorkspaceID:    wsPG,
		BotID:          botPG,
		AsOf:           pgtype.Timestamptz{Time: asOf, Valid: true},
		DefaultTimeout: pgtype.Interval{Microseconds: time.Hour.Microseconds(), Valid: true},
	}
	assertProtected := func(reason string) {
		t.Helper()
		if _, err := h.queries.MarkIdleEnvironmentStopped(ctx, args); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("idle stop with %s: %v", reason, err)
		}
	}
	if err := h.queries.TouchEnvironmentActivity(ctx, db.TouchEnvironmentActivityParams{WorkspaceID: wsPG, BotID: botPG, AsOf: args.AsOf}); err != nil {
		t.Fatal(err)
	}
	assertProtected("fresh activity")
	args.AsOf.Time = asOf.Add(2 * time.Hour)
	userPG := pgtype.UUID{Bytes: owner.User.Id, Valid: true}
	if _, err := h.queries.ClaimEnvironmentControl(ctx, db.ClaimEnvironmentControlParams{BotID: botPG, ControlHolderUserID: userPG}); err != nil {
		t.Fatal(err)
	}
	assertProtected("a newly claimed desktop")
	if _, err := h.queries.ReleaseEnvironmentControl(ctx, db.ReleaseEnvironmentControlParams{BotID: botPG, ControlHolderUserID: userPG}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE workspaces SET sandbox_idle_timeout_minutes = 0 WHERE id = $1`, wsID); err != nil {
		t.Fatal(err)
	}
	assertProtected("auto-stop newly disabled")
}

func seedTestModelPrice(t *testing.T, h *testHarness, accessToken string, wsID uuid.UUID, provider, model string) {
	t.Helper()
	res := h.request(http.MethodPut, "/api/workspaces/"+wsID.String()+"/model-prices", apigen.ModelPriceRequest{
		Provider: provider,
		Model:    model,
	}, accessToken, nil, true)
	if res.status != http.StatusOK {
		t.Fatalf("seed model price %s/%s: %d %s", provider, model, res.status, string(res.body))
	}
}

func createAndStartBotForTest(t *testing.T, h *testHarness, accessToken string, wsID uuid.UUID, name string) (apigen.Bot, apigen.EnvironmentStateResponse) {
	t.Helper()
	base := "/api/workspaces/" + wsID.String()
	botRes := h.request(http.MethodPost, base+"/bots", map[string]any{
		"name": name, "avatarSeed": strings.ToLower(name) + "-seed", "llmMode": "cli", "llmProvider": "openrouter", "llmModel": "openrouter/fixture",
	}, accessToken, nil, true)
	if botRes.status != http.StatusCreated {
		t.Fatalf("create bot %s: %d %s", name, botRes.status, string(botRes.body))
	}
	var bot apigen.Bot
	botRes.decode(t, &bot)

	startRes := h.request(http.MethodPost, base+"/bots/"+bot.Id.String()+"/environment/start", nil, accessToken, nil, true)
	if startRes.status != http.StatusOK {
		t.Fatalf("start bot %s: %d %s", name, startRes.status, string(startRes.body))
	}
	var env apigen.EnvironmentStateResponse
	startRes.decode(t, &env)
	return bot, env
}

func TestIdleSandboxReaperPoliciesAndExemptions(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t, nil, harnessOpts{
		defaultSandboxIdleTimeout: 60 * time.Minute,
	})

	owner, _ := h.completeSetup("reaper-owner@example.com", "CorrectHorse123!", "Reaper Owner", "Reaper Corp", "reaper-corp")
	wsID := owner.ActiveWorkspace.Id
	seedTestModelPrice(t, h, owner.AccessToken, wsID, "openrouter", "openrouter/fixture")

	// Bot A: idle past 60m server default -> auto-stopped with audit log.
	botA, _ := createAndStartBotForTest(t, h, owner.AccessToken, wsID, "BotIdleStop")
	// Bot B: desktop control currently held -> exempt from auto-stop.
	botB, _ := createAndStartBotForTest(t, h, owner.AccessToken, wsID, "BotControlled")
	takeRes := h.request(http.MethodPost, "/api/workspaces/"+wsID.String()+"/bots/"+botB.Id.String()+"/control/take", nil, owner.AccessToken, nil, true)
	if takeRes.status != http.StatusOK {
		t.Fatalf("take control botB: %d %s", takeRes.status, string(takeRes.body))
	}
	// Bot C: pending queued turn -> exempt from auto-stop.
	botC, _ := createAndStartBotForTest(t, h, owner.AccessToken, wsID, "BotQueuedTurn")
	convRow, err := h.queries.OpenDirectMessage(t.Context(), db.OpenDirectMessageParams{
		WorkspaceID: pgtype.UUID{Bytes: wsID, Valid: true},
		UserID:      pgtype.UUID{Bytes: owner.User.Id, Valid: true},
		BotID:       pgtype.UUID{Bytes: botC.Id, Valid: true},
	})
	if err != nil {
		t.Fatalf("OpenDirectMessage: %v", err)
	}
	msgRow, err := h.queries.CreateChatMessage(t.Context(), db.CreateChatMessageParams{
		ID:             pgtype.UUID{Bytes: uuid.New(), Valid: true},
		WorkspaceID:    pgtype.UUID{Bytes: wsID, Valid: true},
		ConversationID: convRow.ID,
		SenderType:     "user",
		SenderUserID:   pgtype.UUID{Bytes: owner.User.Id, Valid: true},
		Kind:           "text",
		Content:        "still working",
		Metadata:       []byte("{}"),
	})
	if err != nil {
		t.Fatalf("CreateChatMessage: %v", err)
	}
	if _, err := h.queries.CreateBotTurn(t.Context(), db.CreateBotTurnParams{
		WorkspaceID:    pgtype.UUID{Bytes: wsID, Valid: true},
		BotID:          pgtype.UUID{Bytes: botC.Id, Valid: true},
		ConversationID: convRow.ID,
		MessageID:      msgRow.ID,
	}); err != nil {
		t.Fatalf("CreateBotTurn: %v", err)
	}
	// Bot W: active UI watcher -> exempt while watcher presence is fresh.
	botW, _ := createAndStartBotForTest(t, h, owner.AccessToken, wsID, "BotWatched")

	// Workspace 2 with custom 15m timeout, Workspace 3 with 0m (disabled).
	ws2Res := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Fast Idle", "slug": "fast-idle"}, owner.AccessToken, nil, true)
	var ws2 apigen.WorkspaceSummary
	ws2Res.decode(t, &ws2)
	seedTestModelPrice(t, h, owner.AccessToken, ws2.Id, "openrouter", "openrouter/fixture")
	patchWS2 := h.request(http.MethodPatch, "/api/workspaces/"+ws2.Id.String(), map[string]any{"sandboxIdleTimeoutMinutes": 15}, owner.AccessToken, nil, true)
	if patchWS2.status != http.StatusOK {
		t.Fatalf("patch ws2 idle timeout: %d %s", patchWS2.status, string(patchWS2.body))
	}
	var ws2Updated apigen.WorkspaceSummary
	patchWS2.decode(t, &ws2Updated)
	if ws2Updated.SandboxIdleTimeoutMinutes == nil || *ws2Updated.SandboxIdleTimeoutMinutes != 15 {
		t.Fatalf("ws2 SandboxIdleTimeoutMinutes = %v, want 15", ws2Updated.SandboxIdleTimeoutMinutes)
	}
	botFast, _ := createAndStartBotForTest(t, h, owner.AccessToken, ws2.Id, "BotFastIdle")

	ws3Res := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Never Idle", "slug": "never-idle"}, owner.AccessToken, nil, true)
	var ws3 apigen.WorkspaceSummary
	ws3Res.decode(t, &ws3)
	seedTestModelPrice(t, h, owner.AccessToken, ws3.Id, "openrouter", "openrouter/fixture")
	patchWS3 := h.request(http.MethodPatch, "/api/workspaces/"+ws3.Id.String(), map[string]any{"sandboxIdleTimeoutMinutes": 0}, owner.AccessToken, nil, true)
	if patchWS3.status != http.StatusOK {
		t.Fatalf("patch ws3 idle timeout: %d %s", patchWS3.status, string(patchWS3.body))
	}
	botNever, _ := createAndStartBotForTest(t, h, owner.AccessToken, ws3.Id, "BotNeverIdle")

	// Validation: out-of-range workspace timeout must fail with 400.
	badTimeout := h.request(http.MethodPatch, "/api/workspaces/"+ws3.Id.String(), map[string]any{"sandboxIdleTimeoutMinutes": 10081}, owner.AccessToken, nil, true)
	if badTimeout.status != http.StatusBadRequest {
		t.Fatalf("excessive idle timeout status = %d, want 400", badTimeout.status)
	}

	// After 20m: only botFast (15m workspace policy) should be reaped.
	h.clock.Advance(20 * time.Minute)
	stoppedCount, err := h.runtime.ReapIdleEnvironments(t.Context())
	if err != nil {
		t.Fatalf("ReapIdleEnvironments at 20m: %v", err)
	}
	if stoppedCount != 1 {
		t.Fatalf("stoppedCount at 20m = %d, want 1", stoppedCount)
	}

	// Advance another 50m (70m total), refresh token after mock clock passed 15m JWT TTL,
	// and poll botW right before reap so its watcher is active.
	h.clock.Advance(50 * time.Minute)
	loginRes := h.request(http.MethodPost, "/api/auth/login", map[string]any{
		"email": "reaper-owner@example.com", "password": "CorrectHorse123!",
	}, "", nil, true)
	if loginRes.status != http.StatusOK {
		t.Fatalf("re-login after clock advance: %d %s", loginRes.status, string(loginRes.body))
	}
	loginRes.decode(t, &owner)
	watchRes := h.request(http.MethodGet, "/api/workspaces/"+wsID.String()+"/bots/"+botW.Id.String()+"/environment", nil, owner.AccessToken, nil, true)
	if watchRes.status != http.StatusOK {
		t.Fatalf("poll botW environment: %d", watchRes.status)
	}

	stoppedCount, err = h.runtime.ReapIdleEnvironments(t.Context())
	if err != nil {
		t.Fatalf("ReapIdleEnvironments at 70m: %v", err)
	}
	if stoppedCount != 1 {
		t.Fatalf("stoppedCount at 70m = %d, want 1 (only botA)", stoppedCount)
	}

	// Expire botW watcher TTL (watcherPresenceTTL = 2m) -> botW is now reaped.
	h.clock.Advance(3 * time.Minute)
	stoppedCount, err = h.runtime.ReapIdleEnvironments(t.Context())
	if err != nil {
		t.Fatalf("ReapIdleEnvironments after watcher expiry: %v", err)
	}
	if stoppedCount != 1 {
		t.Fatalf("stoppedCount after watcher expiry = %d, want 1 (botW)", stoppedCount)
	}

	for _, tc := range []struct {
		ws     uuid.UUID
		bot    uuid.UUID
		status string
		name   string
	}{
		{wsID, botA.Id, "stopped", "botA"},
		{wsID, botW.Id, "stopped", "botW"},
		{wsID, botB.Id, "running", "botB"},
		{wsID, botC.Id, "running", "botC"},
		{ws2.Id, botFast.Id, "stopped", "botFast"},
		{ws3.Id, botNever.Id, "running", "botNever"},
	} {
		envRes := h.request(http.MethodGet, "/api/workspaces/"+tc.ws.String()+"/bots/"+tc.bot.String()+"/environment", nil, owner.AccessToken, nil, true)
		var env apigen.EnvironmentStateResponse
		envRes.decode(t, &env)
		if string(env.Status) != tc.status {
			t.Fatalf("%s status = %s, want %s", tc.name, env.Status, tc.status)
		}
	}

	// Verify auto_stopped_idle audit log recorded for ws1 (botA + botW).
	auditRes := h.request(http.MethodGet, "/api/workspaces/"+wsID.String()+"/audit-logs?action=environment.auto_stopped_idle", nil, owner.AccessToken, nil, true)
	var auditOut apigen.AuditLogListResponse
	if err := json.Unmarshal(auditRes.body, &auditOut); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if auditOut.Total != 2 {
		t.Fatalf("auto_stopped_idle audit total = %d, want 2", auditOut.Total)
	}

	workspacePath := "/api/workspaces/" + ws3.Id.String()
	for _, value := range []any{"30", -1, 12.5} {
		bad := h.request(http.MethodPatch, workspacePath, map[string]any{"sandboxIdleTimeoutMinutes": value}, owner.AccessToken, nil, true)
		if bad.status != http.StatusBadRequest {
			t.Fatalf("idle timeout %v: status = %d, want 400", value, bad.status)
		}
	}
	cleared := h.request(http.MethodPatch, workspacePath, map[string]any{"sandboxIdleTimeoutMinutes": nil}, owner.AccessToken, nil, true)
	if cleared.status != http.StatusOK {
		t.Fatalf("inherit default: %d %s", cleared.status, cleared.body)
	}
	var inherited apigen.WorkspaceSummary
	cleared.decode(t, &inherited)
	if inherited.SandboxIdleTimeoutMinutes != nil {
		t.Fatalf("workspace retained idle override: %v", inherited.SandboxIdleTimeoutMinutes)
	}
	h.clock.Advance(3 * time.Minute)
	if stopped, err := h.runtime.ReapIdleEnvironments(t.Context()); err != nil || stopped != 1 {
		t.Fatalf("reap using restored default: stopped=%d err=%v", stopped, err)
	}
}
