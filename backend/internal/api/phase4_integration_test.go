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
	created := h.request(http.MethodPost, base+"/bots", map[string]any{"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "api", "llmProvider": "openrouter", "llmModel": "fixture"}, owner.AccessToken, nil, true)
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
	r := h.request(http.MethodPost, base+"/bots", map[string]any{"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "api", "llmProvider": "openrouter", "llmModel": "fixture"}, owner.AccessToken, nil, true)
	var bot apigen.Bot
	r.decode(t, &bot)
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member@example.com", "member", "Member", "CorrectHorse123!")
	admin, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "admin@example.com", "admin", "Admin", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "viewer@example.com", "viewer", "Viewer", "CorrectHorse123!")
	budget := map[string]any{"botId": bot.Id, "period": "day", "limitMicros": 100}
	price := map[string]any{"provider": "openrouter", "model": "fixture", "inputMicrosPerMillion": 1000000, "outputMicrosPerMillion": 1000000, "cacheMicrosPerMillion": 1000000}
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
		return emit(engine.Event{Kind: engine.EventKindUsage, InputTokens: 100})
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
		if err := emit(engine.Event{Kind: engine.EventKindUsage, IsError: true, ErrorCode: "usage_unavailable"}); err != nil {
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
	r := h.request(http.MethodPost, base+"/bots", map[string]any{"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "api", "llmProvider": "openrouter", "llmModel": "fixture"}, owner.AccessToken, nil, true)
	var bot apigen.Bot
	r.decode(t, &bot)
	input := map[string]any{"name": "llm-openrouter", "domainMatch": "openrouter.ai", "pathPrefix": "/api/v1", "authScheme": "bearer", "value": "fixture-provider-value"}
	r = h.request(http.MethodPost, base+"/bots/"+bot.Id.String()+"/secrets", input, owner.AccessToken, nil, true)
	if r.status != 201 {
		t.Fatalf("create secret=%d", r.status)
	}
	var secret apigen.SecretMetadata
	r.decode(t, &secret)
	resolver := secrets.ProviderResolver(h.queries, h.keyRing, ws, bot.Id)
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
	r := h.request(http.MethodPost, base+"/bots", map[string]any{"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "api", "llmProvider": "openrouter", "llmModel": "fixture"}, owner.AccessToken, nil, true)
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
