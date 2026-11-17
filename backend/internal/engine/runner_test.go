package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/engine"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func runnerInput() (engine.Session, db.Message) {
	s := engine.Session{WorkspaceID: uuid.New(), BotID: uuid.New(), ConversationID: uuid.New()}
	return s, db.Message{WorkspaceID: pgtype.UUID{Bytes: s.WorkspaceID, Valid: true}, ConversationID: pgtype.UUID{Bytes: s.ConversationID, Valid: true}, Content: "Run fixture", SenderType: "user"}
}

func TestRunnerLifecycle(t *testing.T) {
	session, input := runnerInput()
	started := make(chan struct{})
	r := engine.NewFunctionRunner(func(ctx context.Context, s engine.Session, emit func(engine.Event) error) error {
		if len(s.Messages) != 1 || s.Messages[0].Content != input.Content {
			t.Error("missing message context")
		}
		if err := emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "live"}); err != nil {
			return err
		}
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	if err := r.Send(context.Background(), input); err == nil {
		t.Fatal("send before start")
	}
	if err := r.Start(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background(), session); err == nil {
		t.Fatal("double start")
	}
	done := make(chan error, 1)
	go func() { done <- r.Send(context.Background(), input) }()
	<-started
	if err := r.Send(context.Background(), input); err == nil {
		t.Fatal("concurrent send")
	}
	if ev := <-r.Events(); ev.Text != "live" {
		t.Fatal(ev)
	}
	if err := r.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := r.Stop(); err != nil {
		t.Fatal(err)
	}
	_ = r.Stop()
	if _, ok := <-r.Events(); ok {
		t.Fatal("events not closed")
	}
	if err := r.Send(context.Background(), input); err == nil {
		t.Fatal("send after stop")
	}
	if err := r.Start(context.Background(), session); err == nil {
		t.Fatal("restart after stop")
	}
}

func TestRunnerStopUnblocksBackpressure(t *testing.T) {
	s, m := runnerInput()
	filled := make(chan struct{})
	r := engine.NewFunctionRunner(func(_ context.Context, _ engine.Session, emit func(engine.Event) error) error {
		for range 64 {
			if err := emit(engine.Event{Kind: engine.EventKindTextDelta}); err != nil {
				return err
			}
		}
		close(filled)
		for {
			if err := emit(engine.Event{Kind: engine.EventKindTextDelta}); err != nil {
				return err
			}
		}
	})
	if err := r.Start(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- r.Send(context.Background(), m) }()
	<-filled
	_ = r.Stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	count := 0
	for range r.Events() {
		count++
	}
	if count != 64 {
		t.Fatalf("unbounded buffer: %d", count)
	}
}

func TestRunnerRejectsInvalidAndForeignInput(t *testing.T) {
	s, m := runnerInput()
	r := engine.NewFunctionRunner(func(context.Context, engine.Session, func(engine.Event) error) error {
		t.Error("invalid input executed")
		return nil
	})
	if err := r.Start(context.Background(), engine.Session{}); err == nil {
		t.Fatal("empty session accepted")
	}
	if err := r.Start(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Stop() }()
	for _, change := range []func(*db.Message){func(m *db.Message) { m.WorkspaceID.Bytes = uuid.New() }, func(m *db.Message) { m.ConversationID.Bytes = uuid.New() }, func(m *db.Message) { m.Content = "" }, func(m *db.Message) { m.SenderType = "system" }, func(m *db.Message) { m.WorkspaceID.Valid = false }} {
		invalid := m
		change(&invalid)
		if err := r.Send(context.Background(), invalid); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Send(ctx, m); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	bad := engine.NewFunctionRunner(nil)
	if err := bad.Start(context.Background(), s); err == nil {
		t.Fatal("nil executor accepted")
	}
}

func TestRunnerAcceptsBotHandoff(t *testing.T) {
	session, input := runnerInput()
	input.SenderType = "bot"
	input.Content = "@Atlas your turn"
	r := engine.NewFunctionRunner(func(_ context.Context, s engine.Session, emit func(engine.Event) error) error {
		if len(s.Messages) != 1 || s.Messages[0].SenderType != "bot" || s.Messages[0].Content != "@Atlas your turn" {
			t.Errorf("handoff context: %+v", s.Messages)
		}
		return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "done"})
	})
	if err := r.Start(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if err := r.Send(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	_ = r.Stop()
	for range r.Events() {
	}
}

func TestRunnerAcceptsDesktopHandback(t *testing.T) {
	session, input := runnerInput()
	input.SenderType = "system"
	input.Kind = "text"
	input.Content = engine.HandbackNote + "\nChecked the form"
	input.Metadata = []byte(`{"source":"desktop_handback"}`)
	calls := 0
	r := engine.NewFunctionRunner(func(_ context.Context, s engine.Session, _ func(engine.Event) error) error {
		calls++
		if len(s.Messages) != 1 || s.Messages[0].SenderType != "system" || s.Messages[0].Content != input.Content {
			t.Errorf("hand-back context: %+v", s.Messages)
		}
		return nil
	})
	ctx := context.Background()
	if err := r.Start(ctx, session); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Stop() }()
	for _, tt := range []struct {
		name   string
		change func(*db.Message)
	}{
		{"missing source", func(m *db.Message) { m.Metadata = nil }},
		{"other source", func(m *db.Message) { m.Metadata = []byte(`{"source":"status","detail":"desktop_handback"}`) }},
		{"malformed metadata", func(m *db.Message) { m.Metadata = []byte(`{"source":"desktop_handback"`) }},
		{"non-text message", func(m *db.Message) { m.Kind = "tool" }},
		{"foreign workspace", func(m *db.Message) { m.WorkspaceID.Bytes = uuid.New() }},
		{"foreign conversation", func(m *db.Message) { m.ConversationID.Bytes = uuid.New() }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			invalid := input
			tt.change(&invalid)
			if err := r.Send(ctx, invalid); err == nil {
				t.Fatal("invalid hand-back accepted")
			}
		})
	}
	if err := r.Send(ctx, input); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("executor calls = %d, want 1", calls)
	}
}

func TestRunnerRetainsConversationBetweenMessages(t *testing.T) {
	session, input := runnerInput()
	calls := 0
	r := engine.NewFunctionRunner(func(_ context.Context, s engine.Session, emit func(engine.Event) error) error {
		calls++
		if calls == 2 && (len(s.Messages) != 3 || s.Messages[1].SenderType != "bot" || s.Messages[1].Content != "first reply") {
			t.Errorf("missing assistant context: %+v", s.Messages)
		}
		if err := emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "first reply"}); err != nil {
			return err
		}
		return emit(engine.Event{Kind: engine.EventKindMessage, Text: "first reply"})
	})
	if err := r.Start(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if err := r.Send(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	input.Content = "Follow up"
	if err := r.Send(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	_ = r.Stop()
	for range r.Events() {
	}
}
