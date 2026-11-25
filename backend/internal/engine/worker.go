// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/approvals"
	"github.com/dunkeltech/botbox/backend/internal/authbroker"
	"github.com/dunkeltech/botbox/backend/internal/events"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/groups"
	"github.com/dunkeltech/botbox/backend/internal/memory"
	"github.com/dunkeltech/botbox/backend/internal/notify"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrTurnWaitingSecret indicates the turn paused on a durable secret_request
// and must remain in waiting_secret until fulfilled, denied, expired, or cancelled.
var ErrTurnWaitingSecret = errors.New("turn waiting for secret")

// Session contains only one conversation's context and public bot configuration.
// Preface is the group brief for this turn. Direct messages leave it empty so
// a DM prompt is not wrapped in a second session (ADR-0015).
type Session struct {
	WorkspaceID, BotID, ConversationID, TurnID uuid.UUID
	Bot                                        db.Bot
	Messages                                   []db.Message
	Preface                                    string
	AskBeforePrompt                            string
	ContinuationPrompt                         string
	// MemoryPrompt injects always-in-context knowledge base entries and tool guidance.
	MemoryPrompt string
	// TeamTool is bound to the active group turn. Direct messages leave it nil.
	TeamTool func(context.Context, string, json.RawMessage) (string, error)
	// MemoryTool is bound to the active turn across both DMs and groups.
	MemoryTool func(context.Context, string, json.RawMessage) (string, error)
}

// ExecuteTurn runs one bounded turn and propagates event sink failures.
type ExecuteTurn func(context.Context, Session, func(Event) error) error

// Worker makes JetStream redelivery harmless using persisted turn states.
// Running turns are never replayed automatically because tools may have side effects.
type Worker struct {
	Now        func() time.Time
	mu         sync.Mutex
	active     map[string]context.CancelFunc
	Pool       *pgxpool.Pool
	KeyRing    *secrets.KeyRing
	AuthBroker authbroker.Client
	Execute    ExecuteTurn
	Publish    func(context.Context, Session, Event) error
	// Enqueue publishes the next group turn. Direct messages do not use it.
	Enqueue func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error
	// Signal publishes notification ids on the conversation channel. Nil skips it.
	// The payload must not include message text (issue #17).
	Signal notify.Signal
}

func pgID(id uuid.UUID) pgtype.UUID   { return pgtype.UUID{Bytes: id, Valid: true} }
func uuidID(id pgtype.UUID) uuid.UUID { return uuid.UUID(id.Bytes) }

// Process handles one notification while a transaction lock serializes this bot.
func (w *Worker) Process(ctx context.Context, job events.TurnNotification) error {
	if w.Pool == nil || w.Execute == nil {
		return errors.New("worker is not configured")
	}
	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	// Scheduling locks the conversation before bot queues. Claim in the same
	// order so concurrent handoffs cannot deadlock another bot's claim.
	pending, err := q.GetBotTurn(ctx, db.GetBotTurnParams{WorkspaceID: pgID(job.WorkspaceID), ID: pgID(job.TurnID)})
	if err != nil {
		return err
	}
	if uuidID(pending.BotID) != job.BotID {
		return errors.New("turn identity mismatch")
	}
	if _, err = q.GetGroupForUpdate(ctx, db.GetGroupForUpdateParams{WorkspaceID: pending.WorkspaceID, ID: pending.ConversationID}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	// Reclaim only after the saved deadline and cleanup grace have passed.
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
	// Count and enforce the cap while the conversation and bot locks are held.
	inputReply, allowed, err := groups.InputTurnClaim(ctx, q, turn, w.now())
	if err != nil {
		return err
	}
	if allowed {
		allowed, err = groups.NoteClaim(ctx, q, turn.WorkspaceID, turn.ConversationID)
		if err != nil {
			return err
		}
	}
	if !allowed {
		if err = q.UpdateBotTurn(ctx, db.UpdateBotTurnParams{WorkspaceID: turn.WorkspaceID, ID: turn.ID, Status: "interrupted", ErrorCode: "engine.interrupted"}); err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		if w.Publish != nil {
			return w.Publish(ctx, Session{WorkspaceID: job.WorkspaceID, BotID: job.BotID, ConversationID: uuidID(turn.ConversationID), TurnID: job.TurnID}, Event{Kind: EventKindStatus, Status: "idle", BotID: job.BotID.String(), TurnID: job.TurnID.String(), RunID: uuid.NewString(), Sequence: 1})
		}
		return nil
	}
	config, err := q.GetBotByWorkspaceAndID(ctx, db.GetBotByWorkspaceAndIDParams{WorkspaceID: turn.WorkspaceID, ID: turn.BotID})
	if err != nil {
		return err
	}
	// Commit the running marker before any external side effect. The partial
	// unique index prevents another worker from claiming a second turn per bot.
	claimedAt := w.now()
	runTimeout := time.Duration(config.TurnTimeoutMinutes) * time.Minute
	runCtx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	if err = q.StartBotTurn(runCtx, db.StartBotTurnParams{
		WorkspaceID:   turn.WorkspaceID,
		ID:            turn.ID,
		AsOf:          pgtype.Timestamptz{Time: claimedAt, Valid: true},
		RunDeadlineAt: pgtype.Timestamptz{Time: claimedAt.Add(runTimeout), Valid: true},
	}); err != nil {
		return err
	}
	return w.executeClaimed(runCtx, cancel, tx, turn, config, inputReply)
}

func (w *Worker) executeClaimed(runCtx context.Context, cancel context.CancelFunc, tx pgx.Tx, turn db.BotTurn, bot db.Bot, inputReply bool) (resultErr error) {
	if err := tx.Commit(runCtx); err != nil {
		return err
	}
	q := db.New(w.Pool)
	publish := newTurnPublisher(w.Publish)
	// A failure after claim must not leave a healthy worker showing Thinking
	// forever. Conditional finalization cannot overwrite a secret-wait state.
	defer func() {
		if resultErr == nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(runCtx), 5*time.Second)
		defer cancel()
		status, code := "failed", TurnFailureCode(resultErr)
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) && errors.Is(resultErr, context.DeadlineExceeded) {
			status, code = "interrupted", "engine.interrupted"
		}
		_ = q.FinishRunningBotTurn(cleanup, db.FinishRunningBotTurnParams{WorkspaceID: turn.WorkspaceID, ID: turn.ID, Status: status, ErrorCode: code})
		_ = publish(cleanup, Session{WorkspaceID: uuidID(turn.WorkspaceID), BotID: uuidID(turn.BotID), ConversationID: uuidID(turn.ConversationID), TurnID: uuidID(turn.ID)}, Event{Kind: EventKindStatus, Status: "idle"})
	}()
	// Keep the configuration that was validated before the turn was claimed.
	input, err := q.GetTurnMessage(runCtx, db.GetTurnMessageParams{WorkspaceID: turn.WorkspaceID, ID: turn.MessageID})
	if err != nil {
		return err
	}
	history, err := q.ListTurnContext(runCtx, db.ListTurnContextParams{WorkspaceID: turn.WorkspaceID, ConversationID: turn.ConversationID, UpToPosition: input.Position})
	if err != nil {
		return err
	}
	preface, err := groups.Preface(runCtx, q, turn.WorkspaceID, turn.ConversationID, turn.BotID, w.now())
	if err != nil {
		return err
	}
	session := Session{
		WorkspaceID:        uuidID(turn.WorkspaceID),
		BotID:              uuidID(turn.BotID),
		ConversationID:     uuidID(turn.ConversationID),
		TurnID:             uuidID(turn.ID),
		Bot:                bot,
		Messages:           history,
		Preface:            preface,
		ContinuationPrompt: turn.ContinuationPrompt,
	}
	if preface == "" {
		session.AskBeforePrompt = formatAskBeforePrompt(bot.AskBefore)
	}
	if preface != "" {
		tools := groups.TeamTools{Pool: w.Pool, Now: w.now, Signal: w.Signal}
		scope := groups.TeamToolScope{WorkspaceID: session.WorkspaceID, BotID: session.BotID, TurnID: session.TurnID}
		session.TeamTool = func(ctx context.Context, name string, args json.RawMessage) (string, error) {
			return tools.Call(ctx, scope, name, args)
		}
	}
	memSvc := memory.Service{Pool: w.Pool, Ring: w.KeyRing, Signal: w.Signal, Now: w.now}
	if memPrompt, memErr := memSvc.FormatPromptContext(runCtx, session.WorkspaceID, session.BotID, session.TurnID, true); memErr == nil {
		session.MemoryPrompt = memPrompt
	} else {
		return memErr
	}
	memScope := memory.ToolScope{WorkspaceID: session.WorkspaceID, BotID: session.BotID, TurnID: session.TurnID}
	session.MemoryTool = func(ctx context.Context, name string, args json.RawMessage) (string, error) {
		return memSvc.Call(ctx, memScope, name, args)
	}
	stop, err := groups.ScreenBudget(runCtx, w.Pool, turn.WorkspaceID, turn.ConversationID, turn.BotID, turn.MessageID, w.Signal)
	if err != nil {
		return err
	}
	if stop {
		if err = q.FinishRunningBotTurn(runCtx, db.FinishRunningBotTurnParams{WorkspaceID: turn.WorkspaceID, ID: turn.ID, Status: "budget_exceeded", ErrorCode: "budget_exceeded"}); err != nil {
			return err
		}
		if w.Publish != nil {
			return publish(runCtx, session, Event{Kind: EventKindStatus, Status: "idle"})
		}
		return nil
	}
	events := turnEvents{
		worker: w, queries: q, turn: turn, session: &session, publish: publish,
		savedCredentialTried: make(map[string]bool), teamToolCalls: make(map[string]bool),
	}
	emit := func(ev Event) error { return events.emit(runCtx, ev) }
	key := session.WorkspaceID.String() + "/" + session.BotID.String()
	w.mu.Lock()
	if w.active == nil {
		w.active = make(map[string]context.CancelFunc)
	}
	w.active[key] = cancel
	w.mu.Unlock()
	defer func() { w.mu.Lock(); delete(w.active, key); w.mu.Unlock() }()
	if w.Publish != nil {
		_ = publish(runCtx, session, Event{Kind: EventKindStatus, Status: "thinking"})
	}
	// Recheck after registering cancellation: a pause between claim and
	// registration must not slip through a transient control notification.
	runtime, stateErr := q.GetBotRuntime(runCtx, db.GetBotRuntimeParams{WorkspaceID: turn.WorkspaceID, ID: turn.BotID})
	var runErr error
	if stateErr != nil {
		runErr = stateErr
	} else if runtime.Paused || runtime.Controlled {
		runErr = context.Canceled
	} else {
		stop := db.IsBotTurnStopRequestedParams{WorkspaceID: turn.WorkspaceID, ID: turn.ID}
		requested, err := q.IsBotTurnStopRequested(runCtx, stop)
		switch {
		case err != nil:
			runErr = err
		case requested:
			runErr = context.Canceled
		default:
			go w.watchTurn(runCtx, q, stop, cancel, session.TeamTool != nil)
			runErr = runWithLifecycle(runCtx, w.Execute, session, input, emit)
		}
		// Return only the placeholder through the same continuation path used
		// after human fulfillment. Keep the original turn and time limit.
		for errors.Is(runErr, errUseSavedCredential) && runCtx.Err() == nil {
			session.ContinuationPrompt = strings.TrimSpace(session.ContinuationPrompt + "\n" + events.savedCredentialPrompt)
			runErr = runWithLifecycle(runCtx, w.Execute, session, input, emit)
		}
		if errors.Is(runErr, errUseSavedCredential) {
			runErr = runCtx.Err()
		}
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		runErr = context.DeadlineExceeded
	}
	cancel()
	return w.finalizeTurn(runCtx, q, &events, outcomeForTurn(runErr, events.waitingSecret), inputReply)
}

func isTeamTool(name string) bool {
	name = strings.ReplaceAll(name, "-", "_")
	for _, prefix := range []string{"mcp__botbox_team__", "botbox_team."} {
		name = strings.TrimPrefix(name, prefix)
	}
	return name == groups.TeamStatusTool || name == groups.TeamProgressTool
}

func (w *Worker) watchTurn(ctx context.Context, q *db.Queries, turn db.IsBotTurnStopRequestedParams, cancel context.CancelFunc, group bool) {
	// The request is durable. A missed check or a temporary DB failure can
	// be retried without depending on a transient queue notification.
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var heartbeat <-chan time.Time
	if group {
		lease := time.NewTicker(10 * time.Second)
		defer lease.Stop()
		heartbeat = lease.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat:
			_ = q.TouchBotTurnHeartbeat(ctx, db.TouchBotTurnHeartbeatParams{
				WorkspaceID: turn.WorkspaceID, ID: turn.ID, AsOf: pgtype.Timestamptz{Time: w.now(), Valid: true},
			})
		case <-ticker.C:
			requested, err := q.IsBotTurnStopRequested(ctx, turn)
			if err == nil && requested {
				cancel()
				return
			}
		}
	}
}

func (w *Worker) routeGroupReply(ctx context.Context, turn db.BotTurn, reply db.Message, content string, inputReply bool) error {
	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	// Use the claim lock order. Holding the turn row makes the stop request
	// and routing side effects commit in a well-defined order.
	if _, err = q.GetGroupForUpdate(ctx, db.GetGroupForUpdateParams{WorkspaceID: turn.WorkspaceID, ID: turn.ConversationID}); err != nil {
		return err
	}
	stop, err := q.LockRunningBotTurnForFinalization(ctx, db.LockRunningBotTurnForFinalizationParams{WorkspaceID: turn.WorkspaceID, ID: turn.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var ids []uuid.UUID
	var handoffErr error
	done := db.FinishRunningBotTurnParams{WorkspaceID: turn.WorkspaceID, ID: turn.ID, Status: "completed"}
	if !stop {
		ids, err = approvals.NoteReply(ctx, q, turn, reply.Content)
		if err != nil {
			return err
		}
		mentions, err := notify.HumanMentions(ctx, q, turn.WorkspaceID, turn.ConversationID, reply.ID, reply.Content)
		if err != nil {
			return err
		}
		ids = append(ids, mentions...)
		handoffErr = w.planGroupReply(ctx, tx, turn, reply, content, inputReply)
		if handoffErr != nil {
			// A handoff failure does not invalidate notices from the stored reply.
			done.Status, done.ErrorCode = "failed", TurnFailureCode(handoffErr)
		}
	}
	if err = q.FinishRunningBotTurn(ctx, done); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	w.Signal.Send(ctx, uuidID(turn.WorkspaceID), uuidID(turn.ConversationID), ids)
	return handoffErr
}

func (w *Worker) planGroupReply(ctx context.Context, tx pgx.Tx, turn db.BotTurn, reply db.Message, content string, inputReply bool) error {
	// Roll back a full handoff queue independently of the stored reply's
	// notifications and approvals.
	plan, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = plan.Rollback(ctx) }()
	next, err := groups.PlanBotReply(ctx, db.New(plan), turn, reply, content, inputReply, w.now())
	if errors.Is(err, groups.ErrQueueFull) {
		return plan.Rollback(ctx)
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
	return plan.Commit(ctx)
}

// Interrupt cancels the active local turn; queued turns keep their position.
func (w *Worker) Interrupt(ws, bot uuid.UUID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if cancel := w.active[ws.String()+"/"+bot.String()]; cancel != nil {
		cancel()
	}
}

func formatAskBeforePrompt(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var rules []struct {
		Kind  string `json:"kind"`
		Label string `json:"label"`
	}
	if json.Unmarshal(raw, &rules) != nil || len(rules) == 0 {
		return ""
	}
	parts := make([]string, 0, len(rules))
	for _, rule := range rules {
		label := strings.TrimSpace(rule.Label)
		switch {
		case label != "" && rule.Kind == "push_protected":
			parts = append(parts, rule.Kind+" ("+label+")")
		case label != "":
			parts = append(parts, label)
		case rule.Kind != "":
			parts = append(parts, rule.Kind)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "Ask before: " + strings.Join(parts, ", ")
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}
