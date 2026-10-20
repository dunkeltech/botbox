package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/approvals"
	"github.com/dunkeltech/botbox/backend/internal/budgets"
	"github.com/dunkeltech/botbox/backend/internal/events"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/groups"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Session contains only one conversation's context and public bot configuration.
// Preface is the group brief for this turn. Direct messages leave it empty so
// a DM prompt is not wrapped in a second session (ADR-0015).
type Session struct {
	WorkspaceID, BotID, ConversationID uuid.UUID
	Bot                                db.Bot
	Messages                           []db.Message
	Preface                            string
}

// ExecuteTurn runs one bounded turn and propagates event sink failures.
type ExecuteTurn func(context.Context, Session, func(Event) error) error

// Worker makes JetStream redelivery harmless using persisted turn states.
// Running turns are never replayed automatically because tools may have side effects.
type Worker struct {
	Now     func() time.Time
	mu      sync.Mutex
	active  map[string]context.CancelFunc
	Pool    *pgxpool.Pool
	Execute ExecuteTurn
	Publish func(context.Context, Session, Event) error
	// Enqueue publishes the next group turn. Direct messages do not use it.
	Enqueue func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error
}

func pgID(id uuid.UUID) pgtype.UUID   { return pgtype.UUID{Bytes: id, Valid: true} }
func uuidID(id pgtype.UUID) uuid.UUID { return uuid.UUID(id.Bytes) }

// Process handles one notification while a transaction lock serializes this bot.
func (w *Worker) Process(ctx context.Context, job events.TurnNotification) error {
	if w.Pool == nil || w.Execute == nil {
		return errors.New("worker is not configured")
	}
	config, err := db.New(w.Pool).GetBotByWorkspaceAndID(ctx, db.GetBotByWorkspaceAndIDParams{WorkspaceID: pgID(job.WorkspaceID), ID: pgID(job.BotID)})
	if err != nil {
		return err
	}
	// CLI requests obtain the same lock in their proxy; retaining it here would
	// deadlock the CLI waiting for a response from that proxy.
	if config.LlmMode != "cli" {
		spendConn, err := w.Pool.Acquire(ctx)
		if err != nil {
			return err
		}
		spendQ := db.New(spendConn)
		if err = spendQ.LockWorkspaceSpend(ctx, job.WorkspaceID.String()); err != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_ = spendConn.Hijack().Close(cleanup)
			cancel()
			return err
		}
		defer func() {
			unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := spendQ.UnlockWorkspaceSpend(unlockCtx, job.WorkspaceID.String()); err != nil {
				_ = spendConn.Hijack().Close(unlockCtx)
			} else {
				spendConn.Release()
			}
		}()
	}

	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	// Notifications have a 15-minute deadline, so after 20 minutes the old
	// execution is past its maximum lifetime. Fail it without replaying effects.
	if err = q.ExpireStaleBotTurns(ctx, db.ExpireStaleBotTurnsParams{WorkspaceID: pgID(job.WorkspaceID), BotID: pgID(job.BotID), AsOf: pgtype.Timestamptz{Time: w.now(), Valid: true}}); err != nil {
		return err
	}
	if err = q.LockBotQueue(ctx, job.WorkspaceID.String()+"/"+job.BotID.String()); err != nil {
		return err
	}
	runtime, err := q.GetBotRuntime(ctx, db.GetBotRuntimeParams{WorkspaceID: pgID(job.WorkspaceID), ID: pgID(job.BotID)})
	if err != nil {
		return err
	}
	if runtime.Paused || runtime.Controlled {
		return errors.New("bot execution is suspended")
	}
	notified, err := q.GetBotTurn(ctx, db.GetBotTurnParams{WorkspaceID: pgID(job.WorkspaceID), ID: pgID(job.TurnID)})
	if err != nil {
		return err
	}
	if uuidID(notified.BotID) != job.BotID {
		return errors.New("turn identity mismatch")
	}
	if notified.Status != "queued" {
		return nil
	}
	turn, err := q.NextQueuedBotTurn(ctx, db.NextQueuedBotTurnParams{WorkspaceID: pgID(job.WorkspaceID), BotID: pgID(job.BotID)})
	if err != nil {
		return err
	}
	if turn.ID != notified.ID {
		return errors.New("earlier turn is queued")
	}
	// Commit the running marker before any external side effect. A unique partial
	// index prevents another worker from claiming a second turn for this bot.
	if err = q.UpdateBotTurn(ctx, db.UpdateBotTurnParams{WorkspaceID: turn.WorkspaceID, ID: turn.ID, Status: "running"}); err != nil {
		return err
	}
	// The counter moves on claim, before the model runs, so a reply cannot
	// schedule past the limit (ADR-0015). Direct messages update zero rows.
	if err = groups.NoteClaim(ctx, q, turn.WorkspaceID, turn.ConversationID); err != nil {
		return err
	}
	return w.executeClaimed(ctx, tx, turn, config)
}

func (w *Worker) executeClaimed(ctx context.Context, tx pgx.Tx, turn db.BotTurn, bot db.Bot) error {
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	q := db.New(w.Pool)
	// Use the mode that selected the lock strategy. Re-reading a changed mode
	// here could run API tools without a spend lock or deadlock a CLI's proxy.
	input, err := q.GetTurnMessage(ctx, db.GetTurnMessageParams{WorkspaceID: turn.WorkspaceID, ID: turn.MessageID})
	if err != nil {
		return err
	}
	history, err := q.ListTurnContext(ctx, db.ListTurnContextParams{WorkspaceID: turn.WorkspaceID, ConversationID: turn.ConversationID, UpToPosition: input.Position})
	if err != nil {
		return err
	}
	preface, err := groups.Preface(ctx, q, turn.WorkspaceID, turn.ConversationID)
	if err != nil {
		return err
	}
	session := Session{WorkspaceID: uuidID(turn.WorkspaceID), BotID: uuidID(turn.BotID), ConversationID: uuidID(turn.ConversationID), Bot: bot, Messages: history, Preface: preface}
	stop, err := groups.ScreenBudget(ctx, w.Pool, turn.WorkspaceID, turn.ConversationID, turn.BotID, turn.MessageID)
	if err != nil {
		return err
	}
	if stop {
		if err = q.UpdateBotTurn(ctx, db.UpdateBotTurnParams{WorkspaceID: turn.WorkspaceID, ID: turn.ID, Status: "budget_exceeded", ErrorCode: "budget_exceeded"}); err != nil {
			return err
		}
		if w.Publish != nil {
			return w.Publish(ctx, session, Event{Kind: EventKindStatus, Status: "idle"})
		}
		return nil
	}
	var text replyText
	emit := func(ev Event) error {
		ev.TurnID = uuidID(turn.ID).String()
		switch ev.Kind {
		case EventKindTextDelta, EventKindMessage:
			if err := text.Add(ev); err != nil {
				return err
			}
		case EventKindToolCall, EventKindToolResult:
			if ev.Kind == EventKindToolCall {
				ev.Text = ev.ToolName + "\n" + string(ev.ToolInput)
				// The card is the gate. bot.Box does not push; the git host still
				// has to reject a protected branch (ADR-0015).
				if err := approvals.NoteTool(ctx, w.Pool, turn, ev.Text); err != nil {
					return err
				}
			}
			data, marshalErr := json.Marshal(ev)
			if marshalErr != nil {
				return marshalErr
			}
			if _, e := q.CreateChatMessage(ctx, db.CreateChatMessageParams{ID: pgID(uuid.New()), WorkspaceID: turn.WorkspaceID, ConversationID: turn.ConversationID, SenderType: "bot", SenderBotID: turn.BotID, Kind: "tool", Content: ev.Text, Metadata: data}); e != nil {
				return e
			}
		case EventKindError:
			// Soft denial is valid exit-zero CLI output. Persist the notice so
			// reconnecting clients see why the requested tool did not run.
			data, marshalErr := json.Marshal(ev)
			if marshalErr != nil {
				return marshalErr
			}
			if _, e := q.CreateChatMessage(ctx, db.CreateChatMessageParams{ID: pgID(uuid.New()), WorkspaceID: turn.WorkspaceID, ConversationID: turn.ConversationID, SenderType: "system", Kind: "system", Content: ev.ErrorMessage, Metadata: data}); e != nil {
				return e
			}
		case EventKindUsage:
			if bot.LlmMode == "cli" {
				metered, e := q.HasProxyTurnUsage(ctx, db.HasProxyTurnUsageParams{WorkspaceID: turn.WorkspaceID, TurnID: turn.ID})
				if e != nil {
					return e
				}
				if metered {
					break
				}
			}
			if ev.InputTokens < 0 || ev.OutputTokens < 0 || ev.CacheTokens < 0 {
				return errors.New("invalid usage")
			}
			cost := pgtype.Int8{}
			if ev.CostMicros != nil && bot.LlmMode != "cli" {
				if *ev.CostMicros < 0 {
					return errors.New("invalid reported cost")
				}
				cost = pgtype.Int8{Int64: *ev.CostMicros, Valid: true}
			}
			price, priceErr := q.GetModelPrice(ctx, db.GetModelPriceParams{WorkspaceID: turn.WorkspaceID, Provider: bot.LlmProvider, Model: bot.LlmModel})
			if priceErr == nil && !cost.Valid && !ev.IsError && bot.LlmMode != "cli" {
				amount, e := budgets.PriceMicros(ev.InputTokens, ev.OutputTokens, ev.CacheTokens, price.InputMicrosPerMillion, price.OutputMicrosPerMillion, price.CacheMicrosPerMillion)
				if e != nil {
					return e
				}
				cost = pgtype.Int8{Int64: amount, Valid: true}
			} else if priceErr != nil && !errors.Is(priceErr, pgx.ErrNoRows) {
				return priceErr
			}
			source := "api_response"
			if bot.LlmMode == "cli" {
				source = "cli_result"
			}
			if e := q.RecordTurnUsage(ctx, db.RecordTurnUsageParams{WorkspaceID: turn.WorkspaceID, BotID: turn.BotID, TurnID: turn.ID, RequestID: pgID(uuid.New()), Source: source, CostMicros: cost, CreatedAt: pgtype.Timestamptz{Time: w.now(), Valid: true}, Model: bot.LlmModel, InputTokens: ev.InputTokens, OutputTokens: ev.OutputTokens, CacheTokens: ev.CacheTokens}); e != nil {
				return e
			}
		}
		if w.Publish != nil {
			return w.Publish(ctx, session, ev)
		}
		return nil
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	key := session.WorkspaceID.String() + "/" + session.BotID.String()
	w.mu.Lock()
	if w.active == nil {
		w.active = make(map[string]context.CancelFunc)
	}
	w.active[key] = cancel
	w.mu.Unlock()
	defer func() { w.mu.Lock(); delete(w.active, key); w.mu.Unlock() }()
	if w.Publish != nil {
		_ = w.Publish(runCtx, session, Event{Kind: EventKindStatus, Status: "thinking"})
	}
	defer cancel()
	// Recheck after registering cancellation: a pause between claim and
	// registration must not slip through a transient control notification.
	runtime, stateErr := q.GetBotRuntime(runCtx, db.GetBotRuntimeParams{WorkspaceID: turn.WorkspaceID, ID: turn.BotID})
	var runErr error
	if stateErr != nil {
		runErr = stateErr
	} else if runtime.Paused || runtime.Controlled {
		runErr = context.Canceled
	} else {
		runErr = runWithLifecycle(runCtx, w.Execute, session, input, emit)
	}
	status, code := "completed", ""
	if runErr != nil {
		status, code = "failed", TurnFailureCode(runErr)
		if errors.Is(runErr, llmproxy.ErrBudgetExceeded) {
			status, code = "budget_exceeded", "budget_exceeded"
		}
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			status, code = "interrupted", "engine.interrupted"
		}
	}
	finalCtx, finalCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finalCancel()
	kind, err := q.GetConversationType(finalCtx, db.GetConversationTypeParams{WorkspaceID: turn.WorkspaceID, ID: turn.ConversationID})
	if err != nil {
		return err
	}
	var reply db.Message
	if text.Len() > 0 {
		reply, err = q.CreateChatMessage(finalCtx, db.CreateChatMessageParams{ID: pgID(uuid.New()), WorkspaceID: turn.WorkspaceID, ConversationID: turn.ConversationID, SenderType: "bot", SenderBotID: turn.BotID, Kind: "text", Content: text.String(), Metadata: []byte("{}")})
		if err != nil {
			return fmt.Errorf("persist bot reply: %w", err)
		}
	}
	if kind == "group" && text.Len() > 0 && status == "completed" {
		if err = approvals.NoteReply(finalCtx, w.Pool, turn, text.String()); err != nil {
			return err
		}
		if err = w.routeGroupReply(finalCtx, turn, reply); err != nil {
			return err
		}
	}
	if err = q.UpdateBotTurn(finalCtx, db.UpdateBotTurnParams{WorkspaceID: turn.WorkspaceID, ID: turn.ID, Status: status, ErrorCode: code}); err != nil {
		return err
	}
	if w.Publish != nil {
		return w.Publish(finalCtx, session, Event{Kind: EventKindStatus, Status: "idle"})
	}
	return nil
}

func (w *Worker) routeGroupReply(ctx context.Context, turn db.BotTurn, reply db.Message) error {
	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	next, err := groups.PlanBotReply(ctx, q, turn.WorkspaceID, turn.ConversationID, uuidID(turn.BotID), reply)
	if errors.Is(err, groups.ErrQueueFull) {
		// The reply is already stored. Leave the follow-up unqueued rather than
		// publishing a partial handoff.
		return nil
	}
	if err != nil {
		return err
	}
	if len(next) > 0 && w.Enqueue == nil {
		return errors.New("group turn queue is not configured")
	}
	for _, item := range next {
		if err = w.Enqueue(ctx, item.WorkspaceID, item.BotID, item.TurnID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Interrupt cancels the active local turn; queued turns keep their position.
func (w *Worker) Interrupt(ws, bot uuid.UUID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if cancel := w.active[ws.String()+"/"+bot.String()]; cancel != nil {
		cancel()
	}
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}
