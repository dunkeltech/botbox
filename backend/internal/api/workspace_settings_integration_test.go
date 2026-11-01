package api_test

import (
	"context"
	"net/http"
	"testing"

	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestWorkspaceSettingsUpdateDeleteAndModelPrices(t *testing.T) {
	h := newTestHarness(t, nil, harnessOpts{turnQueue: &fakeTurnQueue{}})

	ownerA, _ := h.completeSetup("owner-a@dunkel.tech", "CorrectHorse123!", "Owner A", "Alpha Lab", "alpha-lab")
	wsA := ownerA.ActiveWorkspace.Id
	baseA := "/api/workspaces/" + wsA.String()

	adminA, _ := h.inviteAndAcceptNewUser(ownerA.AccessToken, wsA, "admin-a@dunkel.tech", "admin", "Admin A", "CorrectHorse123!")
	memberA, _ := h.inviteAndAcceptNewUser(ownerA.AccessToken, wsA, "member-a@dunkel.tech", "member", "Member A", "CorrectHorse123!")
	viewerA, _ := h.inviteAndAcceptNewUser(ownerA.AccessToken, wsA, "viewer-a@dunkel.tech", "viewer", "Viewer A", "CorrectHorse123!")

	// Separate user in Workspace B for cross-tenant 404 verification.
	ownerBResp := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Beta Lab", "slug": "beta-lab"}, adminA.AccessToken, nil, true)
	if ownerBResp.status != http.StatusCreated {
		t.Fatalf("create beta workspace=%d %s", ownerBResp.status, ownerBResp.body)
	}
	var wsB apigen.WorkspaceSummary
	ownerBResp.decode(t, &wsB)

	// 1. Model prices list + RBAC.
	emptyPricesResp := h.request(http.MethodGet, baseA+"/model-prices", nil, viewerA.AccessToken, nil, false)
	if emptyPricesResp.status != http.StatusOK {
		t.Fatalf("get empty model prices=%d %s", emptyPricesResp.status, emptyPricesResp.body)
	}
	var emptyPrices apigen.ModelPriceListResponse
	emptyPricesResp.decode(t, &emptyPrices)
	if len(emptyPrices.Prices) != 0 {
		t.Fatalf("expected 0 initial model prices, got %d", len(emptyPrices.Prices))
	}

	for _, price := range []map[string]any{
		{"provider": "openai", "model": "gpt-5", "inputMicrosPerMillion": 2000000, "outputMicrosPerMillion": 8000000, "cacheMicrosPerMillion": 200000},
		{"provider": "anthropic", "model": "claude-sonnet-4", "inputMicrosPerMillion": 3000000, "outputMicrosPerMillion": 15000000, "cacheMicrosPerMillion": 300000},
	} {
		putResp := h.request(http.MethodPut, baseA+"/model-prices", price, adminA.AccessToken, nil, true)
		if putResp.status != http.StatusOK {
			t.Fatalf("put model price=%d %s", putResp.status, putResp.body)
		}
	}

	for _, tc := range []struct {
		name       string
		token      string
		path       string
		wantStatus int
	}{
		{name: "unauthenticated", token: "", path: baseA + "/model-prices", wantStatus: http.StatusUnauthorized},
		{name: "owner reads model prices", token: ownerA.AccessToken, path: baseA + "/model-prices", wantStatus: http.StatusOK},
		{name: "member reads model prices", token: memberA.AccessToken, path: baseA + "/model-prices", wantStatus: http.StatusOK},
		{name: "viewer reads model prices", token: viewerA.AccessToken, path: baseA + "/model-prices", wantStatus: http.StatusOK},
		{name: "cross-workspace returns 404", token: ownerA.AccessToken, path: "/api/workspaces/" + wsB.Id.String() + "/model-prices", wantStatus: http.StatusNotFound},
	} {
		t.Run("list_model_prices/"+tc.name, func(t *testing.T) {
			resp := h.request(http.MethodGet, tc.path, nil, tc.token, nil, false)
			if resp.status != tc.wantStatus {
				t.Fatalf("got %d (%s), want %d", resp.status, resp.body, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusOK {
				var out apigen.ModelPriceListResponse
				resp.decode(t, &out)
				if len(out.Prices) != 2 || out.Prices[0].Provider != "anthropic" || out.Prices[1].Provider != "openai" {
					t.Fatalf("unexpected sorted prices: %+v", out.Prices)
				}
			}
		})
	}

	// 2. PATCH /api/workspaces/{workspaceID}: validation, RBAC, partial & full updates.
	for _, tc := range []struct {
		name       string
		token      string
		path       string
		body       any
		wantStatus int
	}{
		{name: "unauthenticated", token: "", path: baseA, body: map[string]any{"name": "X"}, wantStatus: http.StatusUnauthorized},
		{name: "admin forbidden", token: adminA.AccessToken, path: baseA, body: map[string]any{"name": "X"}, wantStatus: http.StatusForbidden},
		{name: "member forbidden", token: memberA.AccessToken, path: baseA, body: map[string]any{"name": "X"}, wantStatus: http.StatusForbidden},
		{name: "viewer forbidden", token: viewerA.AccessToken, path: baseA, body: map[string]any{"name": "X"}, wantStatus: http.StatusForbidden},
		{name: "cross-workspace 404", token: ownerA.AccessToken, path: "/api/workspaces/" + wsB.Id.String(), body: map[string]any{"name": "X"}, wantStatus: http.StatusNotFound},
		{name: "empty body 400", token: ownerA.AccessToken, path: baseA, body: map[string]any{}, wantStatus: http.StatusBadRequest},
		{name: "blank name 400", token: ownerA.AccessToken, path: baseA, body: map[string]any{"name": "   "}, wantStatus: http.StatusBadRequest},
		{name: "invalid driver 400", token: ownerA.AccessToken, path: baseA, body: map[string]any{"defaultDriver": "nomad"}, wantStatus: http.StatusBadRequest},
		{name: "blank locale 400", token: ownerA.AccessToken, path: baseA, body: map[string]any{"defaultLocale": ""}, wantStatus: http.StatusBadRequest},
		{name: "retention too low 400", token: ownerA.AccessToken, path: baseA, body: map[string]any{"screenshotRetentionDays": 0}, wantStatus: http.StatusBadRequest},
		{name: "retention too high 400", token: ownerA.AccessToken, path: baseA, body: map[string]any{"screenshotRetentionDays": 31}, wantStatus: http.StatusBadRequest},
	} {
		t.Run("patch_workspace/"+tc.name, func(t *testing.T) {
			resp := h.request(http.MethodPatch, tc.path, tc.body, tc.token, nil, true)
			if resp.status != tc.wantStatus {
				t.Fatalf("got %d (%s), want %d", resp.status, resp.body, tc.wantStatus)
			}
		})
	}

	patchResp := h.request(http.MethodPatch, baseA, map[string]any{
		"name":                    "Alpha Lab Updated",
		"defaultDriver":           "kubernetes",
		"defaultLocale":           "de",
		"screenshotRetentionDays": 14,
	}, ownerA.AccessToken, nil, true)
	if patchResp.status != http.StatusOK {
		t.Fatalf("patch workspace=%d %s", patchResp.status, patchResp.body)
	}
	var updatedWS apigen.WorkspaceSummary
	patchResp.decode(t, &updatedWS)
	if updatedWS.Name != "Alpha Lab Updated" || updatedWS.DefaultDriver != apigen.Kubernetes || updatedWS.DefaultLocale != "de" || updatedWS.ScreenshotRetentionDays == nil || *updatedWS.ScreenshotRetentionDays != 14 {
		t.Fatalf("unexpected updated workspace: %+v", updatedWS)
	}

	// Partial update preserves untouched fields.
	partialResp := h.request(http.MethodPatch, baseA, map[string]any{"screenshotRetentionDays": 21}, ownerA.AccessToken, nil, true)
	if partialResp.status != http.StatusOK {
		t.Fatalf("partial patch workspace=%d %s", partialResp.status, partialResp.body)
	}
	partialResp.decode(t, &updatedWS)
	if updatedWS.Name != "Alpha Lab Updated" || updatedWS.DefaultDriver != apigen.Kubernetes || *updatedWS.ScreenshotRetentionDays != 21 {
		t.Fatalf("partial update overwrote fields: %+v", updatedWS)
	}

	// 3. DELETE /api/workspaces/{workspaceID}: last workspace guard, RBAC, busy guard, and clean cascade.
	lastResp := h.request(http.MethodDelete, baseA, nil, ownerA.AccessToken, nil, true)
	if lastResp.status != http.StatusBadRequest {
		t.Fatalf("delete last workspace=%d %s, want 400", lastResp.status, lastResp.body)
	}
	var errEnv apigen.ErrorEnvelope
	lastResp.decode(t, &errEnv)
	if errEnv.Error.Code != "workspace.last_workspace" {
		t.Fatalf("expected workspace.last_workspace, got %s", errEnv.Error.Code)
	}

	// Create a second workspace owned by ownerA with a bot, DM conversation, message, and running turn.
	ws2Resp := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Disposable Lab", "slug": "disposable-lab"}, ownerA.AccessToken, nil, true)
	if ws2Resp.status != http.StatusCreated {
		t.Fatalf("create second workspace=%d %s", ws2Resp.status, ws2Resp.body)
	}
	var ws2 apigen.WorkspaceSummary
	ws2Resp.decode(t, &ws2)
	base2 := "/api/workspaces/" + ws2.Id.String()

	botResp := h.request(http.MethodPost, base2+"/bots", map[string]any{
		"name":        "Atlas",
		"avatarSeed":  "atlas-001",
		"llmMode":     "cli",
		"llmProvider": "anthropic",
		"llmModel":    "claude-sonnet-4",
	}, ownerA.AccessToken, nil, true)
	if botResp.status != http.StatusCreated {
		t.Fatalf("create bot in ws2=%d %s", botResp.status, botResp.body)
	}
	var bot2 apigen.Bot
	botResp.decode(t, &bot2)

	dmResp := h.request(http.MethodPost, base2+"/bots/"+bot2.Id.String()+"/dm", nil, ownerA.AccessToken, nil, true)
	if dmResp.status != http.StatusOK {
		t.Fatalf("open dm in ws2=%d %s", dmResp.status, dmResp.body)
	}
	var dm apigen.ChatConversation
	dmResp.decode(t, &dm)

	sendResp := h.request(http.MethodPost, base2+"/conversations/"+dm.Id.String()+"/messages", map[string]any{
		"id":      uuid.New(),
		"content": "Hold turn active",
	}, ownerA.AccessToken, nil, true)
	if sendResp.status != http.StatusAccepted {
		t.Fatalf("send dm message=%d %s", sendResp.status, sendResp.body)
	}

	// Simulate an active running turn inside ws2 -> must return 409 workspace.busy.
	if _, err := h.pool.Exec(context.Background(), `UPDATE bot_turns SET status = 'running' WHERE workspace_id = $1`, ws2.Id); err != nil {
		t.Fatalf("mark turn running: %v", err)
	}
	busyResp := h.request(http.MethodDelete, base2, nil, ownerA.AccessToken, nil, true)
	if busyResp.status != http.StatusConflict {
		t.Fatalf("delete busy workspace=%d %s, want 409", busyResp.status, busyResp.body)
	}
	busyResp.decode(t, &errEnv)
	if errEnv.Error.Code != "workspace.busy" {
		t.Fatalf("expected workspace.busy, got %s", errEnv.Error.Code)
	}

	// RBAC negative checks on wsA (where admin, member, viewer belong) and cross-tenant ws2.
	for _, tc := range []struct {
		name       string
		token      string
		path       string
		wantStatus int
	}{
		{name: "unauthenticated", token: "", path: base2, wantStatus: http.StatusUnauthorized},
		{name: "admin forbidden", token: adminA.AccessToken, path: baseA, wantStatus: http.StatusForbidden},
		{name: "member forbidden", token: memberA.AccessToken, path: baseA, wantStatus: http.StatusForbidden},
		{name: "viewer forbidden", token: viewerA.AccessToken, path: baseA, wantStatus: http.StatusForbidden},
		{name: "cross-workspace 404", token: adminA.AccessToken, path: base2, wantStatus: http.StatusNotFound},
	} {
		t.Run("delete_workspace/"+tc.name, func(t *testing.T) {
			resp := h.request(http.MethodDelete, tc.path, nil, tc.token, nil, true)
			if resp.status != tc.wantStatus {
				t.Fatalf("got %d (%s), want %d", resp.status, resp.body, tc.wantStatus)
			}
		})
	}

	// Mark turn completed, then delete ws2 cleanly.
	if _, err := h.pool.Exec(context.Background(), `UPDATE bot_turns SET status = 'completed' WHERE workspace_id = $1`, ws2.Id); err != nil {
		t.Fatalf("mark turn completed: %v", err)
	}
	delResp := h.request(http.MethodDelete, base2, nil, ownerA.AccessToken, nil, true)
	if delResp.status != http.StatusNoContent {
		t.Fatalf("delete workspace=%d %s", delResp.status, delResp.body)
	}

	if check := h.request(http.MethodGet, base2, nil, ownerA.AccessToken, nil, false); check.status != http.StatusNotFound {
		t.Fatalf("expected deleted workspace to return 404, got %d", check.status)
	}

	var auditCount int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action = 'workspace.deleted' AND target_id = $1`, pgtype.UUID{Bytes: ws2.Id, Valid: true}).Scan(&auditCount); err != nil {
		t.Fatalf("query workspace.deleted audit log: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("expected 1 workspace.deleted audit log, got %d", auditCount)
	}
}
