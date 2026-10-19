package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestPhase6GroupHandoffTurnLimitAndLoop(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue, chatLimit: 50})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	base := "/api/workspaces/" + owner.ActiveWorkspace.Id.String()
	atlas := createAPIBot(t, h, base, owner.AccessToken, "Atlas", "atlas-seed")
	nova := createAPIBot(t, h, base, owner.AccessToken, "Nova", "nova-seed")
	members := []any{groupBot(atlas.Id, "lead"), groupBot(nova.Id, "reviewer")}

	handoff := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name: "Launch", goal: "Ship the launch", lead: atlas.Id, botTalk: true, turnLimit: 20, members: members,
	})
	postGroup(t, h, base, owner.AccessToken, handoff.Id, "@Nova start")
	calls := map[uuid.UUID]int{}
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Enqueue: queue.Enqueue, Execute: func(_ context.Context, s engine.Session, emit func(engine.Event) error) error {
		calls[s.BotID]++
		if s.ConversationID == handoff.Id && (s.Preface == "" || !strings.Contains(s.Preface, "Ship the launch") || !strings.Contains(s.Preface, "Nova") || !strings.Contains(s.Preface, "Mention the bot you hand work to.")) {
			t.Errorf("preface=%q", s.Preface)
		}
		reply := "done"
		if s.BotID == nova.Id {
			reply = "@Atlas your turn"
		}
		return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: reply})
	}}
	drainTurns(t, worker, queue, 4)
	if calls[nova.Id] != 1 || calls[atlas.Id] != 1 || len(queue.jobs) != 0 {
		t.Fatalf("handoff calls=%v jobs=%d", calls, len(queue.jobs))
	}

	silent := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name: "Quiet", lead: atlas.Id, botTalk: false, turnLimit: 20, members: members,
	})
	postGroup(t, h, base, owner.AccessToken, silent.Id, "Hello Atlas")
	before := len(queue.jobs)
	drainTurns(t, worker, queue, 4)
	if calls[atlas.Id] != 2 || calls[nova.Id] != 1 || len(queue.jobs) != 0 || before != 1 {
		t.Fatalf("bot talk off calls=%v before=%d jobs=%d", calls, before, len(queue.jobs))
	}

	limited := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name: "Short", lead: atlas.Id, botTalk: true, turnLimit: 1, members: []any{groupBot(atlas.Id, "lead"), groupBot(nova.Id, "reviewer")},
	})
	postGroup(t, h, base, owner.AccessToken, limited.Id, "@Nova go")
	drainTurns(t, worker, queue, 4)
	paused := getGroup(t, h, base, owner.AccessToken, limited.Id)
	if !paused.Paused || paused.PauseReason != "turn_limit" || paused.TurnsSinceHuman != 1 {
		t.Fatalf("limit pause=%s", mustJSON(paused))
	}
	history := groupHistory(t, h, base, owner.AccessToken, limited.Id)
	if !historyHas(history, "Turn 1 of 1 · paused, waiting for you") {
		t.Fatalf("missing limit notice: %+v", history.Messages)
	}
	postGroup(t, h, base, owner.AccessToken, limited.Id, "continue")
	resumed := getGroup(t, h, base, owner.AccessToken, limited.Id)
	if resumed.Paused || resumed.TurnsSinceHuman != 0 || len(queue.jobs) != 1 {
		t.Fatalf("human resume paused=%v turns=%d jobs=%d", resumed.Paused, resumed.TurnsSinceHuman, len(queue.jobs))
	}
	drainTurns(t, worker, queue, 4)

	looped := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name: "Loop", lead: atlas.Id, botTalk: true, turnLimit: 20, members: members,
	})
	worker.Execute = func(_ context.Context, s engine.Session, emit func(engine.Event) error) error {
		other := "Atlas"
		if s.BotID == atlas.Id {
			other = "Nova"
		}
		return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "@" + other + " again"})
	}
	postGroup(t, h, base, owner.AccessToken, looped.Id, "@Atlas start")
	drainTurns(t, worker, queue, 6)
	got := getGroup(t, h, base, owner.AccessToken, looped.Id)
	if !got.Paused || got.PauseReason != "loop" || len(queue.jobs) != 0 {
		t.Fatalf("loop pause=%s jobs=%d", mustJSON(got), len(queue.jobs))
	}
}

func TestPhase6GroupBudgetAndQueueGuards(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue, chatLimit: 200})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	atlas := createAPIBot(t, h, base, owner.AccessToken, "Atlas", "atlas-seed")
	nova := createAPIBot(t, h, base, owner.AccessToken, "Nova", "nova-seed")
	calls := 0
	var cost int64 = 80
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Enqueue: queue.Enqueue, Execute: func(_ context.Context, _ engine.Session, emit func(engine.Event) error) error {
		calls++
		amount := cost
		if calls > 1 {
			amount = 20
		}
		return emit(engine.Event{Kind: engine.EventKindUsage, CostMicros: &amount, InputTokens: 1, OutputTokens: 1})
	}}
	capped := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name: "Cap", lead: atlas.Id, botTalk: false, turnLimit: 20, budget: 100, members: []any{groupBot(atlas.Id, "lead")},
	})
	postGroup(t, h, base, owner.AccessToken, capped.Id, "first")
	drainTurns(t, worker, queue, 2)
	postGroup(t, h, base, owner.AccessToken, capped.Id, "second")
	drainTurns(t, worker, queue, 2)
	warned := groupHistory(t, h, base, owner.AccessToken, capped.Id)
	if calls != 2 || !historyHas(warned, "Budget 80% · this group is close to its cap") {
		t.Fatalf("warning calls=%d history=%+v", calls, warned.Messages)
	}
	postGroup(t, h, base, owner.AccessToken, capped.Id, "third")
	if err := worker.Process(context.Background(), queue.jobs[0]); err != nil {
		t.Fatal(err)
	}
	queue.jobs = nil
	if calls != 2 {
		t.Fatalf("budget stop still called the model %d times", calls)
	}
	stopped := getGroup(t, h, base, owner.AccessToken, capped.Id)
	if !stopped.Paused || stopped.PauseReason != "budget" {
		t.Fatalf("budget pause=%s", mustJSON(stopped))
	}
	approvals, err := h.queries.ListBudgetApprovals(context.Background(), pgtype.UUID{Bytes: ws, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(approvals) != 1 || approvals[0].Kind != "raise_budget" || approvals[0].BudgetID.Valid || !approvals[0].ConversationID.Valid || uuid.UUID(approvals[0].ConversationID.Bytes) != capped.Id {
		t.Fatalf("approval=%+v", approvals)
	}
	postGroup(t, h, base, owner.AccessToken, capped.Id, "fourth")
	if err := worker.Process(context.Background(), queue.jobs[0]); err != nil {
		t.Fatal(err)
	}
	queue.jobs = nil
	approvals, err = h.queries.ListBudgetApprovals(context.Background(), pgtype.UUID{Bytes: ws, Valid: true})
	if err != nil || len(approvals) != 1 {
		t.Fatalf("duplicate approvals=%v err=%v", approvals, err)
	}

	unknown := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name: "Unknown", lead: atlas.Id, botTalk: false, turnLimit: 20, budget: 1000, members: []any{groupBot(atlas.Id, "lead")},
	})
	worker.Execute = func(_ context.Context, _ engine.Session, emit func(engine.Event) error) error {
		calls++
		return emit(engine.Event{Kind: engine.EventKindUsage, IsError: true, ErrorCode: "usage_unavailable"})
	}
	postGroup(t, h, base, owner.AccessToken, unknown.Id, "meter")
	drainTurns(t, worker, queue, 2)
	ran := calls
	postGroup(t, h, base, owner.AccessToken, unknown.Id, "again")
	if err := worker.Process(context.Background(), queue.jobs[0]); err != nil {
		t.Fatal(err)
	}
	queue.jobs = nil
	if calls != ran {
		t.Fatal("unknown cost was treated as zero")
	}
	unknownGroup := getGroup(t, h, base, owner.AccessToken, unknown.Id)
	if !unknownGroup.Paused || unknownGroup.PauseReason != "budget" {
		t.Fatalf("unknown pause=%s", mustJSON(unknownGroup))
	}

	// The cap is per bot. Earlier sections leave Atlas turns in other statuses,
	// so this group uses a bot whose queue starts empty.
	fullBot := createAPIBot(t, h, base, owner.AccessToken, "Quill", "quill-seed")
	full := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name: "Full", lead: fullBot.Id, botTalk: false, turnLimit: 100, members: []any{groupBot(fullBot.Id, "lead")},
	})
	for range 100 {
		postGroup(t, h, base, owner.AccessToken, full.Id, "fill")
	}
	if len(queue.jobs) != 100 {
		t.Fatalf("filled %d", len(queue.jobs))
	}
	blocked := h.request(http.MethodPost, base+"/groups/"+full.Id.String()+"/messages", map[string]any{"id": uuid.New(), "content": "one more"}, owner.AccessToken, nil, true)
	if blocked.status != http.StatusTooManyRequests {
		t.Fatalf("queue full=%d %s", blocked.status, blocked.body)
	}
	var stored int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM messages WHERE workspace_id=$1 AND conversation_id=$2 AND sender_type='user'`, ws, full.Id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 100 || len(queue.jobs) != 100 {
		t.Fatalf("rollback stored=%d jobs=%d", stored, len(queue.jobs))
	}

	queue.jobs = nil
	queue.err = errors.New("NATS unavailable")
	failed := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name: "Publish", lead: nova.Id, botTalk: false, turnLimit: 5, members: []any{groupBot(nova.Id, "lead")},
	})
	r := h.request(http.MethodPost, base+"/groups/"+failed.Id.String()+"/messages", map[string]any{"id": uuid.New(), "content": "must roll back"}, owner.AccessToken, nil, true)
	if r.status != http.StatusServiceUnavailable {
		t.Fatalf("publish=%d %s", r.status, r.body)
	}
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM messages WHERE workspace_id=$1 AND conversation_id=$2`, ws, failed.Id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Fatalf("failed publish stored %d messages", stored)
	}

	queue.err = nil
	queue.jobs = nil
	// API mode holds the workspace spend lock for the whole turn, so a second
	// Process would wait on that lock. A CLI bot leaves the lock to its proxy
	// and the per-bot running turn is what refuses the other group (ADR-0015).
	shared := createAPIBot(t, h, base, owner.AccessToken, "Shared", "shared-seed")
	cli := h.request(http.MethodPatch, base+"/bots/"+shared.Id.String(), map[string]any{"llmMode": "cli", "llmProvider": "anthropic", "llmModel": "sonnet"}, owner.AccessToken, nil, true)
	if cli.status != http.StatusOK {
		t.Fatalf("cli bot=%d %s", cli.status, cli.body)
	}
	held := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name: "Held", lead: shared.Id, botTalk: false, turnLimit: 5, members: []any{groupBot(shared.Id, "only")},
	})
	other := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name: "Other", lead: shared.Id, botTalk: false, turnLimit: 5, members: []any{groupBot(shared.Id, "only")},
	})
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	worker.Execute = func(_ context.Context, s engine.Session, emit func(engine.Event) error) error {
		if s.ConversationID == held.Id {
			once.Do(func() { close(started) })
			<-release
		}
		return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "busy"})
	}
	postGroup(t, h, base, owner.AccessToken, held.Id, "hold")
	first := queue.jobs[0]
	queue.jobs = queue.jobs[1:]
	errCh := make(chan error, 1)
	go func() { errCh <- worker.Process(context.Background(), first) }()
	<-started
	postGroup(t, h, base, owner.AccessToken, other.Id, "next")
	second := queue.jobs[len(queue.jobs)-1]
	if err := worker.Process(context.Background(), second); err == nil {
		t.Fatal("second group started a running turn")
	}
	var running int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM bot_turns WHERE workspace_id=$1 AND bot_id=$2 AND status='running'`, ws, shared.Id).Scan(&running); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM bot_turns WHERE workspace_id=$1 AND conversation_id=$2 AND status='queued'`, ws, other.Id).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if running != 1 || queued != 1 {
		t.Fatalf("running=%d queued=%d", running, queued)
	}
	close(release)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

type groupSettings struct {
	name      string
	goal      string
	lead      uuid.UUID
	botTalk   bool
	turnLimit int
	budget    int64
	members   []any
}

func createGroup(t *testing.T, h *testHarness, base, token string, in groupSettings) apigen.Group {
	t.Helper()
	body := newGroupBody(uuid.New(), in.name, in.lead, in.members)
	body["botTalk"] = in.botTalk
	body["turnLimit"] = in.turnLimit
	if in.goal != "" {
		body["goal"] = in.goal
	}
	if in.budget > 0 {
		body["budgetLimitMicros"] = in.budget
	}
	r := h.request(http.MethodPost, base+"/groups", body, token, nil, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create group=%d %s", r.status, r.body)
	}
	var group apigen.Group
	r.decode(t, &group)
	return group
}

func postGroup(t *testing.T, h *testHarness, base, token string, groupID uuid.UUID, content string) {
	t.Helper()
	r := h.request(http.MethodPost, base+"/groups/"+groupID.String()+"/messages", map[string]any{"id": uuid.New(), "content": content}, token, nil, true)
	if r.status != http.StatusAccepted {
		t.Fatalf("post=%d %s", r.status, r.body)
	}
}

func getGroup(t *testing.T, h *testHarness, base, token string, id uuid.UUID) apigen.Group {
	t.Helper()
	r := h.request(http.MethodGet, base+"/groups/"+id.String(), nil, token, nil, false)
	if r.status != http.StatusOK {
		t.Fatalf("get=%d %s", r.status, r.body)
	}
	var group apigen.Group
	r.decode(t, &group)
	return group
}

func groupHistory(t *testing.T, h *testHarness, base, token string, id uuid.UUID) apigen.ChatHistory {
	t.Helper()
	r := h.request(http.MethodGet, base+"/groups/"+id.String()+"/messages", nil, token, nil, false)
	if r.status != http.StatusOK {
		t.Fatalf("history=%d %s", r.status, r.body)
	}
	var history apigen.ChatHistory
	r.decode(t, &history)
	return history
}

func drainTurns(t *testing.T, worker *engine.Worker, queue *fakeTurnQueue, limit int) {
	t.Helper()
	for range limit {
		if len(queue.jobs) == 0 {
			return
		}
		job := queue.jobs[0]
		queue.jobs = queue.jobs[1:]
		if err := worker.Process(context.Background(), job); err != nil {
			t.Fatal(err)
		}
	}
	if len(queue.jobs) != 0 {
		t.Fatalf("turn chain did not stop, %d jobs left", len(queue.jobs))
	}
}

func historyHas(history apigen.ChatHistory, content string) bool {
	for _, message := range history.Messages {
		if message.Content == content {
			return true
		}
	}
	return false
}

func mustJSON(group apigen.Group) string {
	state := "running"
	if group.Paused {
		state = "paused"
	}
	return group.Name + " " + state + " " + group.PauseReason
}
