package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/desk"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// lockFailDriver implements the desktop bridge so a running sandbox must accept
// the control lock. The memory driver itself does not, which keeps phase 2 tests
// on the host-backed path.
type lockFailDriver struct {
	*compute.MemoryDriver
	err error
}

func (d lockFailDriver) SetDesktopLock(context.Context, compute.EnvID, bool) error {
	return d.err
}

func (d lockFailDriver) DesktopAction(context.Context, compute.EnvID, string, []byte) (desk.Result, error) {
	return desk.Result{}, d.err
}

func (d lockFailDriver) PullDesktopActions(context.Context, compute.EnvID) ([]desk.StoredAction, error) {
	return nil, d.err
}

func (d lockFailDriver) AckDesktopActions(context.Context, compute.EnvID, string, uint64) error {
	return nil
}

func TestPhase5DesktopTimelineRBACAndRetention(t *testing.T) {
	t.Parallel()
	h := newTestHarness(t, nil, harnessOpts{})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Alex", "Acme Bots", "acme")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	created := h.request(http.MethodPost, base+"/bots", map[string]any{
		"name": "Atlas", "avatarSeed": "atlas-01", "llmMode": "cli", "llmProvider": "openrouter", "llmModel": "openrouter/fixture",
	}, owner.AccessToken, nil, true)
	if created.status != http.StatusCreated {
		t.Fatalf("create bot: %d %s", created.status, created.body)
	}
	var bot apigen.Bot
	created.decode(t, &bot)
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "mia@example.com", "member", "Mia", "CorrectHorse123!")
	admin, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "ada@example.com", "admin", "Ada", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "sam@example.com", "viewer", "Sam", "CorrectHorse123!")
	other := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other Org", "slug": "other-org"}, owner.AccessToken, nil, true)
	if other.status != http.StatusCreated {
		t.Fatalf("other workspace: %d %s", other.status, other.body)
	}
	var otherWs apigen.WorkspaceSummary
	other.decode(t, &otherWs)
	eve, _ := h.inviteAndAcceptNewUser(owner.AccessToken, otherWs.Id, "eve@other.com", "owner", "Eve", "CorrectHorse123!")

	asPG := func(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }
	png := []byte{0x89, 'P', 'N', 'G', 1, 2, 3, 4}
	actionID, err := h.queries.InsertDesktopAction(context.Background(), db.InsertDesktopActionParams{
		ID: asPG(uuid.New()), WorkspaceID: asPG(ws), BotID: asPG(bot.Id),
		SessionID: "sess", AgentSeq: 1, Kind: "type",
		Params: []byte(`{"text":"secret-typed-value"}`), Result: "typed",
		Screenshot: png, ScreenshotMime: "image/png",
		CreatedAt: pgtype.Timestamptz{Time: h.clock.Now(), Valid: true},
	})
	if err != nil {
		t.Fatalf("insert action: %v", err)
	}

	listPath := base + "/bots/" + bot.Id.String() + "/desktop-actions"
	shotPath := listPath + "/" + uuid.UUID(actionID.Bytes).String() + "/screenshot"
	if r := h.request(http.MethodGet, listPath, nil, "", nil, false); r.status != http.StatusUnauthorized {
		t.Fatalf("unauth list=%d", r.status)
	}
	if r := h.request(http.MethodGet, listPath, nil, eve.AccessToken, nil, false); r.status != http.StatusNotFound {
		t.Fatalf("cross-workspace list=%d body=%s", r.status, r.body)
	}
	for _, token := range []string{owner.AccessToken, admin.AccessToken, member.AccessToken, viewer.AccessToken} {
		r := h.request(http.MethodGet, listPath, nil, token, nil, false)
		if r.status != http.StatusOK || !strings.Contains(string(r.body), `"kind":"type"`) || !strings.Contains(string(r.body), "secret-typed-value") {
			t.Fatalf("list=%d body=%s", r.status, r.body)
		}
	}
	if strings.Contains(h.logs.String(), "secret-typed-value") || strings.Contains(h.logs.String(), string(png)) {
		t.Fatal("desktop params or screenshot bytes were written to logs")
	}
	shot := h.request(http.MethodGet, shotPath, nil, viewer.AccessToken, nil, false)
	if shot.status != http.StatusOK || shot.headers.Get("Content-Type") != "image/png" || string(shot.body) != string(png) {
		t.Fatalf("screenshot=%d type=%s body=%q", shot.status, shot.headers.Get("Content-Type"), shot.body)
	}
	if r := h.request(http.MethodGet, listPath+"/not-a-uuid/screenshot", nil, owner.AccessToken, nil, false); r.status != http.StatusBadRequest {
		t.Fatalf("bad id=%d", r.status)
	}
	missing := listPath + "/" + uuid.NewString() + "/screenshot"
	if r := h.request(http.MethodGet, missing, nil, owner.AccessToken, nil, false); r.status != http.StatusNotFound {
		t.Fatalf("missing screenshot=%d body=%s", r.status, r.body)
	}

	patch := base
	for _, token := range []string{admin.AccessToken, member.AccessToken, viewer.AccessToken} {
		if r := h.request(http.MethodPatch, patch, map[string]any{"screenshotRetentionDays": 7}, token, nil, true); r.status != http.StatusForbidden {
			t.Fatalf("retention forbidden=%d token role body=%s", r.status, r.body)
		}
	}
	for _, body := range []any{map[string]any{}, map[string]any{"screenshotRetentionDays": 0}, map[string]any{"screenshotRetentionDays": 31}, map[string]any{"screenshotRetentionDays": "nope"}} {
		if r := h.request(http.MethodPatch, patch, body, owner.AccessToken, nil, true); r.status != http.StatusBadRequest {
			t.Fatalf("invalid retention=%d body=%s", r.status, r.body)
		}
	}
	updated := h.request(http.MethodPatch, patch, map[string]any{"screenshotRetentionDays": 1}, owner.AccessToken, nil, true)
	if updated.status != http.StatusOK || !strings.Contains(string(updated.body), `"screenshotRetentionDays":1`) {
		t.Fatalf("update=%d %s", updated.status, updated.body)
	}
	got := h.request(http.MethodGet, patch, nil, owner.AccessToken, nil, false)
	var wsSummary apigen.WorkspaceSummary
	got.decode(t, &wsSummary)
	if wsSummary.ScreenshotRetentionDays == nil || *wsSummary.ScreenshotRetentionDays != 1 {
		t.Fatalf("workspace retention=%v", wsSummary.ScreenshotRetentionDays)
	}
	if _, err = h.pool.Exec(context.Background(), `UPDATE desktop_actions SET created_at = $1 WHERE id = $2`, h.clock.Now().Add(-48*time.Hour), actionID); err != nil {
		t.Fatalf("age screenshot: %v", err)
	}
	expired := h.request(http.MethodGet, shotPath, nil, owner.AccessToken, nil, false)
	if expired.status != http.StatusNotFound || !strings.Contains(string(expired.body), "screenshot.expired") {
		t.Fatalf("expired=%d %s", expired.status, expired.body)
	}
	listed := h.request(http.MethodGet, listPath, nil, owner.AccessToken, nil, false)
	if listed.status != http.StatusOK || !strings.Contains(string(listed.body), `"hasScreenshot":false`) || !strings.Contains(string(listed.body), `"kind":"type"`) {
		t.Fatalf("row after expiry=%d %s", listed.status, listed.body)
	}
}

func TestPhase5HandbackResumesBot(t *testing.T) {
	t.Parallel()
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Alex", "Acme Bots", "acme")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	created := h.request(http.MethodPost, base+"/bots", map[string]any{
		"name": "Atlas", "avatarSeed": "atlas-01", "llmMode": "cli", "llmProvider": "openrouter", "llmModel": "openrouter/fixture",
	}, owner.AccessToken, nil, true)
	if created.status != http.StatusCreated {
		t.Fatalf("create: %d %s", created.status, created.body)
	}
	var bot apigen.Bot
	created.decode(t, &bot)
	open := h.request(http.MethodPost, base+"/bots/"+bot.Id.String()+"/dm", nil, owner.AccessToken, nil, true)
	if open.status != http.StatusOK {
		t.Fatalf("dm=%d %s", open.status, open.body)
	}
	var conversation apigen.ChatConversation
	open.decode(t, &conversation)
	sent := h.request(http.MethodPost, base+"/conversations/"+conversation.Id.String()+"/messages", map[string]any{
		"id": uuid.New(), "content": "fill the form",
	}, owner.AccessToken, nil, true)
	if sent.status != http.StatusAccepted || len(queue.jobs) != 1 {
		t.Fatalf("send=%d jobs=%d", sent.status, len(queue.jobs))
	}
	botPath := base + "/bots/" + bot.Id.String()
	if r := h.request(http.MethodPost, botPath+"/control/take", nil, owner.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("take while stopped=%d %s", r.status, r.body)
	}
	note := "\x01checked the form"
	released := h.request(http.MethodPost, botPath+"/control/release", map[string]any{"note": note}, owner.AccessToken, nil, true)
	if released.status != http.StatusOK {
		t.Fatalf("release=%d %s", released.status, released.body)
	}
	if len(queue.jobs) != 2 || queue.jobs[1].TurnID == queue.jobs[0].TurnID {
		t.Fatalf("jobs=%+v", queue.jobs)
	}
	asPG := func(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }
	msg, err := h.queries.LatestConversationMessage(context.Background(), db.LatestConversationMessageParams{
		WorkspaceID: asPG(ws), ConversationID: asPG(conversation.Id),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := engine.HandbackNote + "\nchecked the form"
	if msg.Content != want || msg.Kind != "text" || msg.SenderType != "system" || !strings.Contains(string(msg.Metadata), "desktop_handback") {
		t.Fatalf("handback message content=%q kind=%s sender=%s meta=%s", msg.Content, msg.Kind, msg.SenderType, msg.Metadata)
	}
	again := h.request(http.MethodPost, botPath+"/control/release", map[string]any{"note": "again"}, owner.AccessToken, nil, true)
	if again.status != http.StatusOK || len(queue.jobs) != 2 {
		t.Fatalf("dedup release=%d jobs=%d", again.status, len(queue.jobs))
	}

	ctx := context.Background()
	handbacks := 0
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Execute: func(_ context.Context, s engine.Session, emit func(engine.Event) error) error {
		if s.TurnID == queue.jobs[0].TurnID {
			return nil
		}
		handbacks++
		input := s.Messages[0]
		if input.SenderType != "system" || input.Content != want {
			return errors.New("missing hand-back instruction")
		}
		if handbacks == 1 {
			return errors.New("fixture screenshot failure")
		}
		return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "Screenshot captured"})
	}}
	for _, job := range queue.jobs {
		if err := worker.Process(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	if handbacks != 1 {
		t.Fatalf("executed %d hand-backs, want 1", handbacks)
	}
	checkTurn := func(id uuid.UUID, status string) {
		t.Helper()
		turn, err := h.queries.GetBotTurn(ctx, db.GetBotTurnParams{WorkspaceID: asPG(ws), ID: asPG(id)})
		if err != nil || turn.Status != status {
			t.Fatalf("turn status = %q, want %q: %v", turn.Status, status, err)
		}
	}
	checkTurn(queue.jobs[1].TurnID, "failed")
	if r := h.request(http.MethodPost, botPath+"/control/release", nil, owner.AccessToken, nil, true); r.status != http.StatusOK || len(queue.jobs) != 2 {
		t.Fatalf("duplicate release after failure=%d jobs=%d", r.status, len(queue.jobs))
	}
	if r := h.request(http.MethodPost, botPath+"/control/take", nil, owner.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("retake=%d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, botPath+"/control/release", map[string]any{"note": note}, owner.AccessToken, nil, true); r.status != http.StatusOK || len(queue.jobs) != 3 {
		t.Fatalf("fresh hand-back=%d jobs=%d body=%s", r.status, len(queue.jobs), r.body)
	}
	if err := worker.Process(ctx, queue.jobs[2]); err != nil {
		t.Fatal(err)
	}
	checkTurn(queue.jobs[2].TurnID, "completed")
	if handbacks != 2 {
		t.Fatalf("executed %d hand-backs, want 2", handbacks)
	}
}

func TestPhase5TakeFailsClosedWhenAgentRejectsLock(t *testing.T) {
	t.Parallel()
	h := newTestHarness(t, nil, harnessOpts{decorateCompute: func(drv *compute.MemoryDriver) compute.Driver {
		return lockFailDriver{MemoryDriver: drv, err: errors.New("agent down")}
	}})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Alex", "Acme Bots", "acme")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	created := h.request(http.MethodPost, base+"/bots", map[string]any{
		"name": "Atlas", "avatarSeed": "atlas-01", "llmMode": "cli", "llmProvider": "openrouter", "llmModel": "openrouter/fixture",
	}, owner.AccessToken, nil, true)
	if created.status != http.StatusCreated {
		t.Fatalf("create: %d %s", created.status, created.body)
	}
	var bot apigen.Bot
	created.decode(t, &bot)
	botPath := base + "/bots/" + bot.Id.String()
	started := h.request(http.MethodPost, botPath+"/environment/start", nil, owner.AccessToken, nil, true)
	if started.status != http.StatusOK {
		t.Fatalf("start=%d %s", started.status, started.body)
	}
	taken := h.request(http.MethodPost, botPath+"/control/take", nil, owner.AccessToken, nil, true)
	if taken.status != http.StatusServiceUnavailable || !strings.Contains(string(taken.body), "agent.unavailable") {
		t.Fatalf("take=%d %s", taken.status, taken.body)
	}
	var env apigen.EnvironmentStateResponse
	h.request(http.MethodGet, botPath+"/environment", nil, owner.AccessToken, nil, false).decode(t, &env)
	if env.ControlHolder != nil {
		t.Fatalf("holder left set: %+v", env.ControlHolder)
	}
	if strings.Contains(h.logs.String(), "agent down") {
		t.Fatal("agent error text leaked into logs")
	}
}
