package api_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/budgets"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	"github.com/dunkeltech/botbox/backend/internal/events"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type fakeTurnQueue struct {
	jobs      []events.TurnNotification
	interrupt func(uuid.UUID, uuid.UUID)
	err       error
	signals   [][]byte
}

func (q *fakeTurnQueue) Enqueue(_ context.Context, ws, bot, turn uuid.UUID) error {
	if q.err != nil {
		return q.err
	}
	q.jobs = append(q.jobs, events.TurnNotification{WorkspaceID: ws, BotID: bot, TurnID: turn})
	return nil
}

// PublishConversation records the conversation signal. Notification payloads are ids only.
func (q *fakeTurnQueue) PublishConversation(_ context.Context, _, _ uuid.UUID, data []byte) error {
	q.signals = append(q.signals, append([]byte(nil), data...))
	return nil
}

func TestPhase4PrivateDirectMessagesAndWorker(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	created := h.request(http.MethodPost, base+"/bots", map[string]any{"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "cli", "llmProvider": "openrouter", "llmModel": "openrouter/fixture"}, owner.AccessToken, nil, true)
	if created.status != 201 {
		t.Fatalf("create: %d %s", created.status, created.body)
	}
	var bot apigen.Bot
	created.decode(t, &bot)
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member@example.com", "member", "Member", "CorrectHorse123!")
	admin, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "admin@example.com", "admin", "Admin", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "viewer@example.com", "viewer", "Viewer", "CorrectHorse123!")
	open := base + "/bots/" + bot.Id.String() + "/dm"
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {viewer.AccessToken, 403}, {owner.AccessToken, 200}, {admin.AccessToken, 200}, {member.AccessToken, 200}} {
		r := h.request(http.MethodPost, open, nil, tc.token, nil, true)
		if r.status != tc.status {
			t.Fatalf("open=%d want=%d body=%s", r.status, tc.status, r.body)
		}
	}
	r := h.request(http.MethodPost, open, nil, owner.AccessToken, nil, true)
	var conversation apigen.ChatConversation
	r.decode(t, &conversation)
	path := base + "/conversations/" + conversation.Id.String() + "/messages"
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {member.AccessToken, 404}, {admin.AccessToken, 404}, {viewer.AccessToken, 404}, {owner.AccessToken, 200}} {
		r := h.request(http.MethodGet, path, nil, tc.token, nil, false)
		if r.status != tc.status {
			t.Fatalf("history=%d want=%d", r.status, tc.status)
		}
	}
	for _, body := range []any{map[string]any{}, map[string]any{"id": uuid.New(), "content": " "}, map[string]any{"id": uuid.New(), "content": 7}} {
		r := h.request(http.MethodPost, path, body, owner.AccessToken, nil, true)
		if r.status != 400 {
			t.Fatalf("invalid=%d body=%s", r.status, r.body)
		}
	}
	input := map[string]any{"id": uuid.New(), "content": "Run the fixture tests"}
	for range 2 {
		r := h.request(http.MethodPost, path, input, owner.AccessToken, nil, true)
		if r.status != 202 {
			t.Fatalf("send=%d %s", r.status, r.body)
		}
	}
	if len(queue.jobs) != 1 {
		t.Fatalf("duplicate scheduled %d jobs", len(queue.jobs))
	}
	input["content"] = "Changed content"
	if r := h.request(http.MethodPost, path, input, owner.AccessToken, nil, true); r.status != 409 {
		t.Fatalf("conflict=%d", r.status)
	}
	called := 0
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Execute: func(ctx context.Context, s engine.Session, emit func(engine.Event) error) error {
		called++
		if s.ConversationID != conversation.Id || len(s.Messages) != 1 {
			t.Fatal("wrong context")
		}
		//nolint:contextcheck // independent browser requests inspect live worker state
		current := h.request(http.MethodGet, base+"/bots/"+bot.Id.String(), nil, owner.AccessToken, nil, false)
		var liveBot apigen.Bot
		current.decode(t, &liveBot)
		if liveBot.Status != "thinking" || liveBot.Mood != "thinking" {
			t.Fatalf("thinking state=%s", current.body)
		}
		if err := emit(engine.Event{Kind: engine.EventKindToolCall, ToolID: "fixture-tool", ToolName: "exec"}); err != nil {
			return err
		}
		//nolint:contextcheck // the browser request intentionally has its own context
		current = h.request(http.MethodGet, base+"/bots/"+bot.Id.String(), nil, owner.AccessToken, nil, false)
		current.decode(t, &liveBot)
		if liveBot.Status != "action" || liveBot.Mood != "action" {
			t.Fatalf("action state=%s", current.body)
		}
		if err := emit(engine.Event{Kind: engine.EventKindToolResult, ToolID: "fixture-tool", Text: "tests passed"}); err != nil {
			return err
		}
		for _, ev := range []engine.Event{{Kind: engine.EventKindTextDelta, Text: "Tests passed"}, {Kind: engine.EventKindUsage, InputTokens: 10, OutputTokens: 3}} {
			if err := emit(ev); err != nil {
				return err
			}
		}
		return nil
	}}
	for range 2 {
		if err := worker.Process(context.Background(), queue.jobs[0]); err != nil {
			t.Fatal(err)
		}
	}
	if called != 1 {
		t.Fatalf("turn replayed %d times", called)
	}
	history := h.request(http.MethodGet, path, nil, owner.AccessToken, nil, false)
	var out apigen.ChatHistory
	history.decode(t, &out)
	if len(out.Messages) != 4 || out.Messages[3].Content != "Tests passed" || out.Turns[0].Status != "completed" {
		t.Fatalf("history=%s", history.body)
	}
	queue.err = errors.New("NATS unavailable")
	if r := h.request(http.MethodPost, path, map[string]any{"id": uuid.New(), "content": "Must roll back"}, owner.AccessToken, nil, true); r.status != 503 {
		t.Fatalf("queue failure=%d", r.status)
	}
	history = h.request(http.MethodGet, path, nil, owner.AccessToken, nil, false)
	history.decode(t, &out)
	if len(out.Messages) != 4 {
		t.Fatal("failed publish persisted input")
	}
	if r := h.request(http.MethodGet, path+"?before=invalid", nil, owner.AccessToken, nil, false); r.status != 400 {
		t.Fatalf("bad cursor=%d", r.status)
	}
	other := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other", "slug": "other"}, owner.AccessToken, nil, true)
	var otherWS apigen.WorkspaceSummary
	other.decode(t, &otherWS)
	if r := h.request(http.MethodGet, "/api/workspaces/"+otherWS.Id.String()+"/conversations/"+conversation.Id.String()+"/messages", nil, owner.AccessToken, nil, false); r.status != 404 {
		t.Fatalf("cross workspace=%d", r.status)
	}
}

func TestPhase4BudgetsAndApprovals(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	r := h.request(http.MethodPost, base+"/bots", map[string]any{"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "cli", "llmProvider": "openrouter", "llmModel": "openrouter/fixture"}, owner.AccessToken, nil, true)
	var bot apigen.Bot
	r.decode(t, &bot)
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member@example.com", "member", "Member", "CorrectHorse123!")
	admin, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "admin@example.com", "admin", "Admin", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "viewer@example.com", "viewer", "Viewer", "CorrectHorse123!")
	budget := map[string]any{"botId": bot.Id, "period": "day", "limitMicros": 100}
	price := map[string]any{"provider": "openrouter", "model": "openrouter/fixture", "inputMicrosPerMillion": 1000000, "outputMicrosPerMillion": 1000000, "cacheMicrosPerMillion": 1000000}
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {member.AccessToken, 403}, {viewer.AccessToken, 403}, {admin.AccessToken, 200}, {owner.AccessToken, 200}} {
		for path, body := range map[string]any{"/budgets": budget, "/model-prices": price} {
			r := h.request(http.MethodPut, base+path, body, tc.token, nil, true)
			if r.status != tc.status {
				t.Fatalf("%s=%d want=%d %s", path, r.status, tc.status, r.body)
			}
		}
	}
	for _, body := range []any{map[string]any{"period": "day", "limitMicros": 10}, map[string]any{"period": "month", "limitMicros": -1}, map[string]any{"period": "month", "limitMicros": "wrong"}} {
		if r := h.request(http.MethodPut, base+"/budgets", body, owner.AccessToken, nil, true); r.status != 400 {
			t.Fatalf("invalid budget=%d", r.status)
		}
	}
	r = h.request(http.MethodPost, base+"/bots/"+bot.Id.String()+"/dm", nil, owner.AccessToken, nil, true)
	var conversation apigen.ChatConversation
	r.decode(t, &conversation)
	r = h.request(http.MethodPost, base+"/conversations/"+conversation.Id.String()+"/messages", map[string]any{"id": uuid.New(), "content": "Meter this turn"}, owner.AccessToken, nil, true)
	if r.status != 202 {
		t.Fatal(string(r.body))
	}
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Execute: func(ctx context.Context, s engine.Session, emit func(engine.Event) error) error {
		return recordMeteredFixture(ctx, h, s, 100, true)
	}}
	if err := worker.Process(context.Background(), queue.jobs[0]); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := budgets.Check(context.Background(), h.queries, ws, bot.Id, h.clock.Now()); !errors.Is(err, llmproxy.ErrBudgetExceeded) {
			t.Fatalf("budget check=%v", err)
		}
	}
	approvals, err := h.queries.ListBudgetApprovals(context.Background(), pgtype.UUID{Bytes: ws, Valid: true})
	if err != nil || len(approvals) != 1 {
		t.Fatalf("approvals=%v err=%v", approvals, err)
	}
	r = h.request(http.MethodGet, base+"/budgets?botId="+bot.Id.String(), nil, viewer.AccessToken, nil, false)
	var summary apigen.BudgetSummary
	r.decode(t, &summary)
	if len(summary.Budgets) != 1 || !summary.Budgets[0].Exceeded || summary.Budgets[0].SpentMicros != 100 {
		t.Fatalf("summary=%s", r.body)
	}
	budget["limitMicros"] = 200
	r = h.request(http.MethodPut, base+"/budgets", budget, admin.AccessToken, nil, true)
	if r.status != 200 {
		t.Fatal(string(r.body))
	}
	if err := budgets.Check(context.Background(), h.queries, ws, bot.Id, h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	approvals, err = h.queries.ListBudgetApprovals(context.Background(), pgtype.UUID{Bytes: ws, Valid: true})
	if err != nil || len(approvals) != 0 {
		t.Fatalf("pending approvals=%v err=%v", approvals, err)
	}
	r = h.request(http.MethodPost, base+"/conversations/"+conversation.Id.String()+"/messages", map[string]any{"id": uuid.New(), "content": "Simulate a truncated provider stream"}, owner.AccessToken, nil, true)
	if r.status != 202 {
		t.Fatal(string(r.body))
	}
	worker.Execute = func(ctx context.Context, s engine.Session, emit func(engine.Event) error) error {
		if err := recordMeteredFixture(ctx, h, s, 0, false); err != nil {
			return err
		}
		return errors.New("incomplete provider stream")
	}
	if err := worker.Process(context.Background(), queue.jobs[1]); err != nil {
		t.Fatal(err)
	}
	if err := budgets.Check(context.Background(), h.queries, ws, bot.Id, h.clock.Now()); err == nil {
		t.Fatal("unreconciled API spend allowed another request")
	}

}

func TestPhase4EncryptedProviderCredentialScope(t *testing.T) {
	h := newTestHarness(t, nil, harnessOpts{})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	r := h.request(http.MethodPost, base+"/bots", map[string]any{"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "cli", "llmProvider": "openrouter", "llmModel": "openrouter/fixture"}, owner.AccessToken, nil, true)
	var bot apigen.Bot
	r.decode(t, &bot)
	input := map[string]any{"name": "llm-openrouter", "domainMatch": "openrouter.ai", "pathPrefix": "/api/v1", "authScheme": "bearer", "value": "fixture-provider-value"}
	r = h.request(http.MethodPost, base+"/bots/"+bot.Id.String()+"/secrets", input, owner.AccessToken, nil, true)
	if r.status != 201 {
		t.Fatalf("create secret=%d", r.status)
	}
	var secret apigen.SecretMetadata
	r.decode(t, &secret)
	resolver := secrets.CLIProviderResolver(h.queries, h.keyRing, ws, bot.Id)
	cfg, err := resolver(context.Background(), bot.Id.String(), "openrouter")
	if err != nil || cfg.SecretValue != "fixture-provider-value" || cfg.PathPrefix != "/api/v1" {
		t.Fatal("provider credential was not resolved with its scope")
	}
	if _, err = resolver(context.Background(), uuid.NewString(), "openrouter"); !errors.Is(err, llmproxy.ErrSecretNotFound) {
		t.Fatal("foreign bot credential resolved")
	}
	input["domainMatch"] = "other.example"
	r = h.request(http.MethodPut, base+"/bots/"+bot.Id.String()+"/secrets/"+secret.Id.String(), input, owner.AccessToken, nil, true)
	if r.status != 200 {
		t.Fatalf("replace secret=%d", r.status)
	}
	if _, err = resolver(context.Background(), bot.Id.String(), "openrouter"); !errors.Is(err, llmproxy.ErrSecretNotFound) {
		t.Fatal("credential crossed its destination host boundary")
	}
}

func TestPhase4MessageRateLimit(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue, chatLimit: 1})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	base := "/api/workspaces/" + owner.ActiveWorkspace.Id.String()
	r := h.request(http.MethodPost, base+"/bots", map[string]any{"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "cli", "llmProvider": "openrouter", "llmModel": "openrouter/fixture"}, owner.AccessToken, nil, true)
	var bot apigen.Bot
	r.decode(t, &bot)
	r = h.request(http.MethodPost, base+"/bots/"+bot.Id.String()+"/dm", nil, owner.AccessToken, nil, true)
	var c apigen.ChatConversation
	r.decode(t, &c)
	path := base + "/conversations/" + c.Id.String() + "/messages"
	for _, want := range []int{202, 429} {
		r = h.request(http.MethodPost, path, map[string]any{"id": uuid.New(), "content": "fixture"}, owner.AccessToken, nil, true)
		if r.status != want {
			t.Fatalf("status=%d want=%d", r.status, want)
		}
	}
	h.clock.Advance(time.Minute)
	r = h.request(http.MethodPost, path, map[string]any{"id": uuid.New(), "content": "fixture"}, owner.AccessToken, nil, true)
	if r.status != 202 {
		t.Fatalf("rate window did not reset: %d", r.status)
	}
	asPG := func(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }
	for range 98 {
		message, err := h.queries.CreateChatMessage(context.Background(), db.CreateChatMessageParams{ID: asPG(uuid.New()), WorkspaceID: asPG(owner.ActiveWorkspace.Id), ConversationID: asPG(c.Id), SenderType: "user", SenderUserID: asPG(owner.User.Id), Kind: "text", Content: "queue fixture", Metadata: []byte("{}")})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = h.queries.CreateBotTurn(context.Background(), db.CreateBotTurnParams{WorkspaceID: asPG(owner.ActiveWorkspace.Id), BotID: asPG(bot.Id), ConversationID: asPG(c.Id), MessageID: message.ID}); err != nil {
			t.Fatal(err)
		}
	}
	h.clock.Advance(time.Minute)
	r = h.request(http.MethodPost, path, map[string]any{"id": uuid.New(), "content": "queue must stay bounded"}, owner.AccessToken, nil, true)
	if r.status != 429 || len(queue.jobs) != 2 {
		t.Fatalf("queue cap status=%d jobs=%d", r.status, len(queue.jobs))
	}
}

func TestPhase4BotFiveHourAndWeeklyBudgets(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()

	r := h.request(http.MethodPost, base+"/bots", map[string]any{
		"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "cli", "llmProvider": "openrouter", "llmModel": "openrouter/fixture",
	}, owner.AccessToken, nil, true)
	var bot apigen.Bot
	r.decode(t, &bot)

	r = h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "OtherWS", "slug": "other-ws"}, owner.AccessToken, nil, true)
	if r.status != 201 {
		t.Fatalf("create other workspace=%d %s", r.status, r.body)
	}
	var otherWS apigen.WorkspaceSummary
	r.decode(t, &otherWS)
	otherOwner, _ := h.inviteAndAcceptNewUser(owner.AccessToken, otherWS.Id, "other@example.com", "owner", "Other", "CorrectHorse123!")
	otherBase := "/api/workspaces/" + otherWS.Id.String()

	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member@example.com", "member", "Member", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "viewer@example.com", "viewer", "Viewer", "CorrectHorse123!")

	// Negative validation: workspace-level 5h/week and unknown period must be rejected.
	for _, body := range []any{
		map[string]any{"period": "5h", "limitMicros": 100},
		map[string]any{"period": "week", "limitMicros": 100},
		map[string]any{"botId": bot.Id, "period": "year", "limitMicros": 100},
		map[string]any{"botId": bot.Id, "period": "5h", "limitMicros": 0},
		map[string]any{"botId": bot.Id, "period": "week", "limitMicros": 1000000000001},
	} {
		if res := h.request(http.MethodPut, base+"/budgets", body, owner.AccessToken, nil, true); res.status != 400 {
			t.Fatalf("invalid budget %v=%d", body, res.status)
		}
	}

	// Cross-workspace access must return 404 across GET, PUT, and DELETE.
	if res := h.request(http.MethodGet, otherBase+"/budgets?botId="+bot.Id.String(), nil, otherOwner.AccessToken, nil, false); res.status != 404 {
		t.Fatalf("cross-workspace GET status=%d", res.status)
	}
	if res := h.request(http.MethodPut, otherBase+"/budgets", map[string]any{"botId": bot.Id, "period": "5h", "limitMicros": 100}, otherOwner.AccessToken, nil, true); res.status != 404 {
		t.Fatalf("cross-workspace PUT status=%d", res.status)
	}
	if res := h.request(http.MethodDelete, otherBase+"/budgets?botId="+bot.Id.String()+"&period=5h", nil, otherOwner.AccessToken, nil, true); res.status != 404 {
		t.Fatalf("cross-workspace DELETE status=%d", res.status)
	}

	// RBAC for setting 5h and week bot budgets.
	fiveHourBudget := map[string]any{"botId": bot.Id, "period": "5h", "limitMicros": 100}
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {member.AccessToken, 403}, {viewer.AccessToken, 403}, {owner.AccessToken, 200}} {
		if res := h.request(http.MethodPut, base+"/budgets", fiveHourBudget, tc.token, nil, true); res.status != tc.status {
			t.Fatalf("PUT 5h token status=%d want=%d %s", res.status, tc.status, res.body)
		}
	}
	if res := h.request(http.MethodPut, base+"/budgets", map[string]any{"botId": bot.Id, "period": "week", "limitMicros": 100}, owner.AccessToken, nil, true); res.status != 200 {
		t.Fatalf("PUT week=%d %s", res.status, res.body)
	}
	if res := h.request(http.MethodPut, base+"/model-prices", map[string]any{
		"provider": "openrouter", "model": "openrouter/fixture", "inputMicrosPerMillion": 1000000, "outputMicrosPerMillion": 1000000, "cacheMicrosPerMillion": 1000000,
	}, owner.AccessToken, nil, true); res.status != 200 {
		t.Fatalf("PUT model-prices=%d", res.status)
	}

	// Record 100 micros of spend on Sunday 2026-11-15 12:00 UTC.
	r = h.request(http.MethodPost, base+"/bots/"+bot.Id.String()+"/dm", nil, owner.AccessToken, nil, true)
	var conversation apigen.ChatConversation
	r.decode(t, &conversation)
	r = h.request(http.MethodPost, base+"/conversations/"+conversation.Id.String()+"/messages", map[string]any{"id": uuid.New(), "content": "Spend 100 micros"}, owner.AccessToken, nil, true)
	if r.status != 202 {
		t.Fatalf("send chat=%d %s", r.status, r.body)
	}
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Execute: func(ctx context.Context, s engine.Session, emit func(engine.Event) error) error {
		return recordMeteredFixture(ctx, h, s, 100, true)
	}}
	if err := worker.Process(context.Background(), queue.jobs[0]); err != nil {
		t.Fatal(err)
	}

	if err := budgets.Check(context.Background(), h.queries, ws, bot.Id, h.clock.Now()); !errors.Is(err, llmproxy.ErrBudgetExceeded) {
		t.Fatalf("expected budget exceeded on Sunday 12:00 UTC, got %v", err)
	}

	login := func(email string) string {
		res := h.request(http.MethodPost, "/api/auth/login", map[string]any{
			"email": email, "password": "CorrectHorse123!",
		}, "", nil, true)
		if res.status != 200 {
			t.Fatalf("login %s=%d %s", email, res.status, res.body)
		}
		out := owner
		res.decode(t, &out)
		return out.AccessToken
	}

	// Advance 5h + 1s (Sunday 17:00:01 UTC): 5h rolling window expires, week cap still holds.
	h.clock.Advance(5*time.Hour + time.Second)
	viewerToken := login("viewer@example.com")
	r = h.request(http.MethodGet, base+"/budgets?botId="+bot.Id.String(), nil, viewerToken, nil, false)
	if r.status != 200 {
		t.Fatalf("GET budgets=%d %s", r.status, r.body)
	}
	var summary apigen.BudgetSummary
	r.decode(t, &summary)
	byPeriod := map[apigen.BudgetStatePeriod]apigen.BudgetState{}
	for _, b := range summary.Budgets {
		byPeriod[b.Period] = b
	}
	if got := byPeriod[apigen.BudgetStatePeriodN5h]; got.SpentMicros != 0 || got.Exceeded {
		t.Fatalf("5h window should have decayed after 5h+1s: %+v", got)
	}
	if got := byPeriod[apigen.BudgetStatePeriodWeek]; got.SpentMicros != 100 || !got.Exceeded {
		t.Fatalf("week window should still be exhausted on Sunday evening: %+v", got)
	}
	if err := budgets.Check(context.Background(), h.queries, ws, bot.Id, h.clock.Now()); !errors.Is(err, llmproxy.ErrBudgetExceeded) {
		t.Fatalf("week cap should still block on Sunday evening, got %v", err)
	}

	// Advance past Monday 2026-11-16 00:00 UTC: week cap resets.
	h.clock.Advance(7 * time.Hour)
	if err := budgets.Check(context.Background(), h.queries, ws, bot.Id, h.clock.Now()); err != nil {
		t.Fatalf("week cap should reset on Monday UTC, got %v", err)
	}

	ownerToken := login("owner@example.com")
	memberToken := login("member@example.com")
	viewerToken = login("viewer@example.com")

	// Delete negative/RBAC tests and valid deletion.
	for _, path := range []string{
		base + "/budgets?period=5h",
		base + "/budgets?botId=not-a-uuid&period=5h",
		base + "/budgets?botId=" + bot.Id.String() + "&period=year",
	} {
		if res := h.request(http.MethodDelete, path, nil, ownerToken, nil, true); res.status != 400 {
			t.Fatalf("DELETE invalid %s=%d", path, res.status)
		}
	}
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {memberToken, 403}, {viewerToken, 403}, {ownerToken, 204}, {ownerToken, 204}} {
		if res := h.request(http.MethodDelete, base+"/budgets?botId="+bot.Id.String()+"&period=5h", nil, tc.token, nil, true); res.status != tc.status {
			t.Fatalf("DELETE 5h status=%d want=%d %s", res.status, tc.status, res.body)
		}
	}
	r = h.request(http.MethodGet, base+"/budgets?botId="+bot.Id.String(), nil, ownerToken, nil, false)
	if r.status != 200 {
		t.Fatalf("GET budgets after delete=%d %s", r.status, r.body)
	}
	summary = apigen.BudgetSummary{}
	r.decode(t, &summary)
	if len(summary.Budgets) != 1 || summary.Budgets[0].Period != apigen.BudgetStatePeriodWeek {
		t.Fatalf("expected only week budget after deleting 5h, got %+v", summary.Budgets)
	}
}

func TestPhase4WorkspaceSpendTotalsCombinedAndBoundaries(t *testing.T) {
	h := newTestHarness(t, nil, harnessOpts{})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()

	r := h.request(http.MethodPost, base+"/bots", map[string]any{
		"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "cli", "llmProvider": "anthropic", "llmModel": "claude-sonnet-4-6",
	}, owner.AccessToken, nil, true)
	if r.status != 201 {
		t.Fatalf("create bot=%d %s", r.status, r.body)
	}
	var bot apigen.Bot
	r.decode(t, &bot)

	r = h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "OtherWS", "slug": "other-ws-spend"}, owner.AccessToken, nil, true)
	if r.status != 201 {
		t.Fatalf("create other workspace=%d %s", r.status, r.body)
	}
	var otherWS apigen.WorkspaceSummary
	r.decode(t, &otherWS)
	otherOwner, _ := h.inviteAndAcceptNewUser(owner.AccessToken, otherWS.Id, "other-spend@example.com", "owner", "Other", "CorrectHorse123!")
	otherBase := "/api/workspaces/" + otherWS.Id.String()

	// Auth and validation negative checks on GET /budgets.
	if res := h.request(http.MethodGet, base+"/budgets", nil, "", nil, false); res.status != 401 {
		t.Fatalf("unauthenticated GET budgets=%d", res.status)
	}
	if res := h.request(http.MethodGet, base+"/budgets?botId=not-a-uuid", nil, owner.AccessToken, nil, false); res.status != 400 {
		t.Fatalf("invalid botId GET budgets=%d", res.status)
	}
	if res := h.request(http.MethodGet, otherBase+"/budgets?botId="+bot.Id.String(), nil, otherOwner.AccessToken, nil, false); res.status != 404 {
		t.Fatalf("cross-workspace bot GET budgets=%d", res.status)
	}

	// Harness clock starts on Sunday 2026-11-15 12:00 UTC. Insert:
	// - metered spend on Saturday 2026-11-14 23:00 UTC (same Mon-Sun week, prior day): 100 micros
	// - unbudgeted subscription usage on Sunday 2026-11-15 10:00 UTC: NULL cost & internal charge
	// - budgeted subscription internal charge on Sunday 2026-11-15 11:00 UTC: 250 micros
	// - metered spend from prior week (Sunday 2026-11-08 23:00 UTC): 999 micros (must be excluded)
	ctx := context.Background()
	saturday := h.clock.Now().Add(-13 * time.Hour)
	sundayMorning := h.clock.Now().Add(-2 * time.Hour)
	sundayLater := h.clock.Now().Add(-1 * time.Hour)
	priorWeek := h.clock.Now().Add(-7 * 24 * time.Hour)
	for _, rec := range []struct {
		at             time.Time
		billingKind    string
		subBudgeted    bool
		cost           *int64
		internalCharge *int64
	}{
		{at: priorWeek, billingKind: "metered", cost: ptrInt64(999)},
		{at: saturday, billingKind: "metered", cost: ptrInt64(100)},
		{at: sundayMorning, billingKind: "subscription"},
		{at: sundayLater, billingKind: "subscription", subBudgeted: true, internalCharge: ptrInt64(250)},
	} {
		_, err := h.pool.Exec(ctx, `INSERT INTO usage_records (workspace_id, bot_id, request_id, source, model, input_tokens, output_tokens, cache_tokens, billing_kind, subscription_budgeted, cost_micros, internal_charge_micros, created_at)
			VALUES ($1, $2, $3, 'proxy_meter', 'claude-sonnet-4-6', 10, 5, 0, $4, $5, $6, $7, $8)`,
			ws, bot.Id, uuid.New(), rec.billingKind, rec.subBudgeted, rec.cost, rec.internalCharge, rec.at)
		if err != nil {
			t.Fatalf("insert usage record: %v", err)
		}
	}

	var summary apigen.BudgetSummary
	res := h.request(http.MethodGet, base+"/budgets", nil, owner.AccessToken, nil, false)
	if res.status != 200 {
		t.Fatalf("GET workspace budgets=%d %s", res.status, res.body)
	}
	res.decode(t, &summary)
	if summary.SpendTodayMicros != 0 || summary.SpendThisWeekMicros != 100 ||
		summary.TotalSpendTodayMicros != 250 || summary.TotalSpendThisWeekMicros != 350 {
		t.Fatalf("unexpected Sunday totals: %+v", summary)
	}

	// Cross-workspace workspace summary must stay isolated at zero.
	var otherSummary apigen.BudgetSummary
	res = h.request(http.MethodGet, otherBase+"/budgets", nil, otherOwner.AccessToken, nil, false)
	if res.status != 200 {
		t.Fatalf("GET other workspace budgets=%d %s", res.status, res.body)
	}
	res.decode(t, &otherSummary)
	if otherSummary.TotalSpendTodayMicros != 0 || otherSummary.TotalSpendThisWeekMicros != 0 {
		t.Fatalf("cross-workspace spend leaked: %+v", otherSummary)
	}

	// Advance past Monday 2026-11-16 00:00 UTC: weekly and daily totals both reset.
	h.clock.Advance(13 * time.Hour)
	loginRes := h.request(http.MethodPost, "/api/auth/login", map[string]any{
		"email": "owner@example.com", "password": "CorrectHorse123!",
	}, "", nil, true)
	if loginRes.status != 200 {
		t.Fatalf("re-login=%d %s", loginRes.status, loginRes.body)
	}
	var refreshedOwner apigen.AuthSessionResponse
	loginRes.decode(t, &refreshedOwner)
	summary = apigen.BudgetSummary{}
	res = h.request(http.MethodGet, base+"/budgets", nil, refreshedOwner.AccessToken, nil, false)
	if res.status != 200 {
		t.Fatalf("GET Monday budgets=%d %s", res.status, res.body)
	}
	res.decode(t, &summary)
	if summary.SpendTodayMicros != 0 || summary.SpendThisWeekMicros != 0 ||
		summary.TotalSpendTodayMicros != 0 || summary.TotalSpendThisWeekMicros != 0 {
		t.Fatalf("expected Monday UTC reset to zero, got %+v", summary)
	}
}

func ptrInt64(v int64) *int64 { return &v }
