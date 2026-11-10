package api_test

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/google/uuid"
)

func TestGroupBotsRunConcurrentlyWithIndependentStatus(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	base := "/api/workspaces/" + owner.ActiveWorkspace.Id.String()
	lead := createPiBot(t, h, base, owner.AccessToken, "Yoda", "yoda-seed")
	other := createPiBot(t, h, base, owner.AccessToken, "Nova", "nova-seed")
	group := createGroup(t, h, base, owner.AccessToken, groupSettings{name: "Parallel", lead: lead.Id, turnLimit: 20, members: []any{groupBot(lead.Id, "lead"), groupBot(other.Id, "reviewer")}})
	postGroup(t, h, base, owner.AccessToken, group.Id, "@Yoda @Nova work")
	started := make(chan uuid.UUID, 2)
	release := map[uuid.UUID]chan struct{}{lead.Id: make(chan struct{}), other.Id: make(chan struct{})}
	if _, err := h.pool.Exec(context.Background(), "INSERT INTO environments (bot_id,driver,status) VALUES ($1,'docker','running'),($2,'docker','running')", lead.Id, other.Id); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var events []engine.Event
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	worker := &engine.Worker{Pool: h.pool, Publish: func(_ context.Context, _ engine.Session, event engine.Event) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
		return nil
	}, Execute: func(ctx context.Context, s engine.Session, emit func(engine.Event) error) error {
		if err := emit(engine.Event{Kind: engine.EventKindStatus, Status: "idle"}); err != nil {
			return err
		}
		started <- s.BotID
		select {
		case <-release[s.BotID]:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	type completion struct {
		bot uuid.UUID
		err error
	}
	done := make(chan completion, 2)
	for _, job := range queue.jobs {
		go func() { done <- completion{job.BotID, worker.Process(ctx, job)} }()
	}
	remaining := 2
	defer func() {
		cancel()
		for range remaining {
			<-done
		}
	}()
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("workspace lock serialized different bots")
		}
	}
	var list apigen.BotListResponse
	h.request(http.MethodGet, base+"/bots", nil, owner.AccessToken, nil, false).decode(t, &list)
	for _, bot := range list.Bots {
		if bot.Status != "thinking" {
			t.Errorf("bot %s status=%s", bot.Name, bot.Status)
		}
	}
	history := groupHistory(t, h, base, owner.AccessToken, group.Id)
	for _, turn := range history.Turns {
		if turn.BotId == nil {
			t.Error("turn has no bot identity")
		}
	}
	if err := worker.Process(ctx, queue.jobs[0]); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
		t.Fatal("duplicate delivery executed again")
	default:
	}
	mu.Lock()
	for _, event := range events {
		if event.BotID == "" || event.TurnID == "" || event.RunID == "" || event.Sequence == 0 {
			t.Error("unidentified live event")
		}
		if event.Status == "idle" {
			t.Error("provider step ended the bot status early")
		}
	}
	mu.Unlock()
	close(release[lead.Id])
	select {
	case result := <-done:
		remaining--
		if result.bot != lead.Id || result.err != nil {
			t.Fatalf("completion=%+v", result)
		}
	case <-ctx.Done():
		t.Fatal("lead did not finish")
	}
	h.request(http.MethodGet, base+"/bots", nil, owner.AccessToken, nil, false).decode(t, &list)
	for _, bot := range list.Bots {
		want := "thinking"
		if bot.Id == lead.Id {
			want = "idle"
		}
		if string(bot.Status) != want {
			t.Errorf("bot %s=%s want=%s", bot.Name, bot.Status, want)
		}
	}

}

func TestParallelFanoutCannotExceedGroupTurnLimit(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	base := "/api/workspaces/" + owner.ActiveWorkspace.Id.String()
	a := createPiBot(t, h, base, owner.AccessToken, "Yoda", "yoda-seed")
	b := createPiBot(t, h, base, owner.AccessToken, "Nova", "nova-seed")
	g := createGroup(t, h, base, owner.AccessToken, groupSettings{name: "Limit", lead: a.Id, turnLimit: 1, members: []any{groupBot(a.Id, "lead"), groupBot(b.Id, "reviewer")}})
	postGroup(t, h, base, owner.AccessToken, g.Id, "@Yoda @Nova work")
	var calls atomic.Int32
	worker := &engine.Worker{Pool: h.pool, Execute: func(context.Context, engine.Session, func(engine.Event) error) error { calls.Add(1); return nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 2)
	for _, job := range queue.jobs {
		go func() { done <- worker.Process(ctx, job) }()
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("executed %d turns under limit 1", calls.Load())
	}
	current := getGroup(t, h, base, owner.AccessToken, g.Id)
	if current.TurnsSinceHuman != 1 || !current.Paused || current.PauseReason != "turn_limit" {
		t.Fatalf("group=%+v", current)
	}
}
