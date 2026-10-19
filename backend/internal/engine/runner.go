package engine

import (
	"context"
	"errors"
	"sync"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
)

// AgentRunner owns one session's execution and its bounded event stream.
// Send runs a turn synchronously; consumers must drain Events concurrently.
// Interrupt cancels the current turn. Stop cancels all work and closes Events.
type AgentRunner interface {
	Start(context.Context, Session) error
	Send(context.Context, db.Message) error
	Events() <-chan Event
	Interrupt() error
	Stop() error
}

// FunctionRunner applies the same lifecycle to CLI and API turn executors.
// A runner cannot be restarted after Stop; instantiate a new one for a new session.
type FunctionRunner struct {
	mu         sync.Mutex
	execute    ExecuteTurn
	session    Session
	lifetime   context.Context
	cancel     context.CancelFunc
	turnCancel context.CancelFunc
	done       chan struct{}
	events     chan Event
	stopped    bool
	stopOnce   sync.Once
}

var _ AgentRunner = (*FunctionRunner)(nil)

// NewFunctionRunner creates a runner with bounded event backpressure.
func NewFunctionRunner(execute ExecuteTurn) *FunctionRunner {
	return &FunctionRunner{execute: execute, events: make(chan Event, 64)}
}

// Start binds the runner to one private conversation without starting external work.
func (r *FunctionRunner) Start(ctx context.Context, s Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped || r.lifetime != nil || r.execute == nil {
		return errors.New("runner cannot start")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.WorkspaceID == [16]byte{} || s.BotID == [16]byte{} || s.ConversationID == [16]byte{} {
		return errors.New("session identity is required")
	}
	for _, m := range s.Messages {
		if !m.WorkspaceID.Valid || !m.ConversationID.Valid || m.WorkspaceID.Bytes != s.WorkspaceID || m.ConversationID.Bytes != s.ConversationID {
			return errors.New("invalid session history")
		}
	}
	r.lifetime, r.cancel = context.WithCancel(ctx)
	r.session = s
	r.session.Messages = append([]db.Message(nil), s.Messages...)
	return nil
}

// Send appends the message that triggered this turn and runs one bounded turn.
// A direct message is always a human post. A group handoff is another bot's
// text, so both sender types are valid input (ADR-0015).
func (r *FunctionRunner) Send(ctx context.Context, message db.Message) error {
	r.mu.Lock()
	if r.stopped || r.lifetime == nil || r.done != nil {
		r.mu.Unlock()
		return errors.New("runner is not ready")
	}
	if message.Content == "" || !message.WorkspaceID.Valid || !message.ConversationID.Valid || message.WorkspaceID.Bytes != r.session.WorkspaceID || message.ConversationID.Bytes != r.session.ConversationID || (message.SenderType != "user" && message.SenderType != "bot") {
		r.mu.Unlock()
		return errors.New("invalid runner message")
	}
	if err := ctx.Err(); err != nil {
		r.mu.Unlock()
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	//nolint:contextcheck // the Start lifetime must also cancel independently supplied Send contexts
	stopLifetime := context.AfterFunc(r.lifetime, cancel)
	if r.lifetime.Err() != nil {
		cancel()
	}
	r.turnCancel = cancel
	done := make(chan struct{})
	r.done = done
	r.session.Messages = append([]db.Message{message}, r.session.Messages...)
	snapshot := r.session
	r.mu.Unlock()
	defer func() {
		stopLifetime()
		cancel()
		r.mu.Lock()
		r.done = nil
		r.turnCancel = nil
		close(done)
		r.mu.Unlock()
	}()
	if err := runCtx.Err(); err != nil {
		return err
	}
	var reply replyText
	var emitMu sync.Mutex
	err := r.execute(runCtx, snapshot, func(ev Event) error {
		emitMu.Lock()
		defer emitMu.Unlock()
		if err := reply.Add(ev); err != nil {
			return err
		}
		if err := runCtx.Err(); err != nil {
			return err
		}
		select {
		case r.events <- ev:
			return nil
		case <-runCtx.Done():
			return runCtx.Err()
		}
	})
	if reply.Len() > 0 {
		r.mu.Lock()
		r.session.Messages = append([]db.Message{{WorkspaceID: message.WorkspaceID, ConversationID: message.ConversationID, SenderType: "bot", SenderBotID: pgID(snapshot.BotID), Kind: "text", Content: reply.String()}}, r.session.Messages...)
		r.mu.Unlock()
	}
	if err != nil {
		return err
	}
	return runCtx.Err()
}

// Events returns the session stream; Stop closes it after producers exit.
func (r *FunctionRunner) Events() <-chan Event { return r.events }

// Interrupt cancels an active turn while leaving the session available.
func (r *FunctionRunner) Interrupt() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.turnCancel != nil {
		r.turnCancel()
	}
	return nil
}

// Stop waits for cancellation before closing Events, so consumers never race a send.
func (r *FunctionRunner) Stop() error {
	r.stopOnce.Do(func() {
		r.mu.Lock()
		r.stopped = true
		if r.cancel != nil {
			r.cancel()
		}
		if r.turnCancel != nil {
			r.turnCancel()
		}
		done := r.done
		r.mu.Unlock()
		if done != nil {
			<-done
		}
		close(r.events)
	})
	return nil
}

func runWithLifecycle(ctx context.Context, execute ExecuteTurn, session Session, input db.Message, emit func(Event) error) error {
	runner := NewFunctionRunner(execute)
	// Session history is newest first and already includes the triggering message.
	if len(session.Messages) > 0 && session.Messages[0].ID == input.ID {
		session.Messages = session.Messages[1:]
	}
	if err := runner.Start(ctx, session); err != nil {
		return err
	}
	result := make(chan error, 1)
	go func() {
		err := runner.Send(ctx, input)
		_ = runner.Stop()
		result <- err
	}()
	var sinkErr error
	for ev := range runner.Events() {
		if sinkErr == nil {
			sinkErr = emit(ev)
			if sinkErr != nil {
				_ = runner.Interrupt()
			}
		}
	}
	err := <-result
	if sinkErr != nil {
		return sinkErr
	}
	return err
}
