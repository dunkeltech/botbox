package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/approvals"
	"github.com/dunkeltech/botbox/backend/internal/authbroker"
	"github.com/dunkeltech/botbox/backend/internal/budgets"
	"github.com/dunkeltech/botbox/backend/internal/events"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/groups"
	"github.com/dunkeltech/botbox/backend/internal/notify"
	"github.com/dunkeltech/botbox/backend/internal/orchestrator"
	"github.com/dunkeltech/botbox/backend/internal/secretrequests"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
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
	ContinuationPrompt                         string
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
	config, err := db.New(w.Pool).GetBotByWorkspaceAndID(ctx, db.GetBotByWorkspaceAndIDParams{WorkspaceID: pgID(job.WorkspaceID), ID: pgID(job.BotID)})
	if err != nil {
		return err
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
	// Commit the running marker before any external side effect. The partial
	// unique index prevents another worker from claiming a second turn per bot.
	if err = q.UpdateBotTurn(ctx, db.UpdateBotTurnParams{WorkspaceID: turn.WorkspaceID, ID: turn.ID, Status: "running"}); err != nil {
		return err
	}
	return w.executeClaimed(ctx, tx, turn, config, inputReply)
}

func (w *Worker) executeClaimed(ctx context.Context, tx pgx.Tx, turn db.BotTurn, bot db.Bot, inputReply bool) (resultErr error) {
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	q := db.New(w.Pool)
	runID := uuid.NewString()
	var sequence atomic.Uint64
	publish := func(ctx context.Context, session Session, event Event) error {
		event.RunID = runID
		event.BotID = session.BotID.String()
		event.TurnID = session.TurnID.String()
		event.Sequence = sequence.Add(1)
		if w.Publish != nil {
			return w.Publish(ctx, session, event)
		}
		return nil
	}
	// A failure after claim must not leave a healthy worker showing Thinking
	// forever. Conditional finalization cannot overwrite a secret-wait state.
	defer func() {
		if resultErr == nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = q.FinishRunningBotTurn(cleanup, db.FinishRunningBotTurnParams{WorkspaceID: turn.WorkspaceID, ID: turn.ID, Status: "failed", ErrorCode: TurnFailureCode(resultErr)})
		_ = publish(cleanup, Session{WorkspaceID: uuidID(turn.WorkspaceID), BotID: uuidID(turn.BotID), ConversationID: uuidID(turn.ConversationID), TurnID: uuidID(turn.ID)}, Event{Kind: EventKindStatus, Status: "idle"})
	}()
	// Keep the configuration that was validated before the turn was claimed.
	input, err := q.GetTurnMessage(ctx, db.GetTurnMessageParams{WorkspaceID: turn.WorkspaceID, ID: turn.MessageID})
	if err != nil {
		return err
	}
	history, err := q.ListTurnContext(ctx, db.ListTurnContextParams{WorkspaceID: turn.WorkspaceID, ConversationID: turn.ConversationID, UpToPosition: input.Position})
	if err != nil {
		return err
	}
	preface, err := groups.Preface(ctx, q, turn.WorkspaceID, turn.ConversationID, turn.BotID, w.now())
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
	stop, err := groups.ScreenBudget(ctx, w.Pool, turn.WorkspaceID, turn.ConversationID, turn.BotID, turn.MessageID, w.Signal)
	if err != nil {
		return err
	}
	if stop {
		if err = q.FinishRunningBotTurn(ctx, db.FinishRunningBotTurnParams{WorkspaceID: turn.WorkspaceID, ID: turn.ID, Status: "budget_exceeded", ErrorCode: "budget_exceeded"}); err != nil {
			return err
		}
		if w.Publish != nil {
			return publish(ctx, session, Event{Kind: EventKindStatus, Status: "idle"})
		}
		return nil
	}
	var text replyText
	var waitingSecret bool
	savedCredentialTried := make(map[string]bool)
	var savedCredentialPrompt string
	useSavedCredential := errors.New("continue with saved GitLab credential")
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	emit := func(ev Event) error {
		ev.BotID = uuidID(turn.BotID).String()
		ev.TurnID = uuidID(turn.ID).String()
		// Provider completion is only a model step, not completion of the turn.
		if ev.Kind == EventKindStatus && (ev.Status == "idle" || ev.Status == "offline") {
			return nil
		}
		switch ev.Kind {
		case EventKindTextDelta, EventKindMessage:
			if err := text.Add(ev); err != nil {
				return err
			}
		case EventKindToolCall, EventKindToolResult:
			if ev.Kind == EventKindToolCall && IsRequestSecretTool(ev.ToolName) {
				var reqArgs struct {
					Kind               string `json:"kind"`
					AdapterID          string `json:"adapter_id"`
					TargetOrigin       string `json:"target_origin"`
					Reason             string `json:"reason"`
					EnvPlaceholderName string `json:"env_placeholder_name"`
					UsernameHint       string `json:"username_hint"`
				}
				if uErr := json.Unmarshal(ev.ToolInput, &reqArgs); uErr == nil {
					canCreate := false
					var adapter mitmproxy.HTTPSAdapter
					switch reqArgs.Kind {
					case secretrequests.KindCLIEgress:
						var ok bool
						adapter, ok = mitmproxy.LookupAdapter(reqArgs.AdapterID)
						canCreate = ok && strings.TrimSpace(reqArgs.Reason) != "" && mitmproxy.ValidateEnvPlaceholderName(reqArgs.EnvPlaceholderName) == nil
					case secretrequests.KindBrowserLogin:
						_, ok := authbroker.LookupLoginAdapter(reqArgs.AdapterID)
						canCreate = w.AuthBroker != nil && ok && strings.TrimSpace(reqArgs.Reason) != "" && strings.TrimSpace(reqArgs.EnvPlaceholderName) == ""
					}
					if canCreate {
						res, srErr := secretrequests.CreateRequest(runCtx, w.Pool, secretrequests.CreateInput{
							WorkspaceID:        session.WorkspaceID,
							BotID:              session.BotID,
							ConversationID:     session.ConversationID,
							TurnID:             session.TurnID,
							ToolCallID:         ev.ToolID,
							Kind:               reqArgs.Kind,
							AdapterID:          reqArgs.AdapterID,
							TargetOrigin:       reqArgs.TargetOrigin,
							Reason:             reqArgs.Reason,
							EnvPlaceholderName: reqArgs.EnvPlaceholderName,
							UsernameHint:       reqArgs.UsernameHint,
							TrySavedGitLab:     !savedCredentialTried[adapter.ID],
							Broker:             w.AuthBroker,
							Now:                w.now(),
						})
						if srErr != nil {
							if errors.Is(srErr, context.Canceled) {
								return srErr
							}
							code := secretrequests.FailureCode(srErr)
							failure := Event{Kind: EventKindToolResult, TurnID: ev.TurnID, ToolID: ev.ToolID, ToolName: ev.ToolName, IsError: true, ErrorCode: code, Text: secretrequests.FailureMessage(code)}
							metadata, _ := json.Marshal(failure)
							if _, err := q.CreateChatMessage(ctx, db.CreateChatMessageParams{ID: pgID(uuid.New()), WorkspaceID: turn.WorkspaceID, ConversationID: turn.ConversationID, SenderType: "bot", SenderBotID: turn.BotID, Kind: "tool", Content: failure.Text, Metadata: metadata}); err != nil {
								return err
							}
							if w.Publish != nil {
								_ = w.Publish(ctx, session, failure)
							}
							return WithFailureCode(code, srErr)
						}
						if res.SavedCredentialPrompt != "" {
							savedCredentialTried[adapter.ID] = true
							savedCredentialPrompt = res.SavedCredentialPrompt
							return useSavedCredential
						}
						waitingSecret = true
						if w.Publish != nil {
							_ = publish(ctx, session, Event{Kind: EventKindCard, Text: res.Card.Reason, Metadata: res.Message.Metadata, TurnID: ev.TurnID})
						}
						return ErrTurnWaitingSecret
					}
				}
			}
			if ev.Kind == EventKindToolCall {
				ev.Text = ev.ToolName + "\n" + string(ev.ToolInput)
				// The card is the gate. bot.Box does not push; the git host still
				// has to reject a protected branch (ADR-0015).
				if err := approvals.NoteTool(ctx, w.Pool, turn, ev.Text, w.Signal); err != nil {
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
		case EventKindCard:
			if strings.TrimSpace(ev.Text) == "" {
				return errors.New("card text is required")
			}
			data := []byte("{}")
			if len(ev.Metadata) > 0 {
				if !json.Valid(ev.Metadata) {
					return errors.New("invalid card metadata")
				}
				data = ev.Metadata
			}
			if _, e := q.CreateChatMessage(ctx, db.CreateChatMessageParams{ID: pgID(uuid.New()), WorkspaceID: turn.WorkspaceID, ConversationID: turn.ConversationID, SenderType: "bot", SenderBotID: turn.BotID, Kind: "card", Content: ev.Text, Metadata: data}); e != nil {
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
			{
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
			return publish(ctx, session, ev)
		}
		return nil
	}
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
			go watchTurnStop(runCtx, q, stop, cancel)
			runErr = runWithLifecycle(runCtx, w.Execute, session, input, emit)
		}
		// Return only the placeholder through the same continuation path used
		// after human fulfillment. Keep the original turn and time limit.
		for errors.Is(runErr, useSavedCredential) && runCtx.Err() == nil {
			session.ContinuationPrompt = strings.TrimSpace(session.ContinuationPrompt + "\n" + savedCredentialPrompt)
			runErr = runWithLifecycle(runCtx, w.Execute, session, input, emit)
		}
		if errors.Is(runErr, useSavedCredential) {
			runErr = runCtx.Err()
		}
	}
	status, code := "completed", ""
	if waitingSecret || errors.Is(runErr, ErrTurnWaitingSecret) {
		status = "waiting_secret"
	} else if runErr != nil {
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
		content := text.String()
		if kind == "group" {
			content, _, _ = orchestrator.SplitInputDirective(content)
		}
		reply, err = q.CreateChatMessage(finalCtx, db.CreateChatMessageParams{ID: pgID(uuid.New()), WorkspaceID: turn.WorkspaceID, ConversationID: turn.ConversationID, SenderType: "bot", SenderBotID: turn.BotID, Kind: "text", Content: content, Metadata: []byte("{}")})
		if err != nil {
			return fmt.Errorf("persist bot reply: %w", err)
		}
	}
	if status == "waiting_secret" {
		if w.Publish != nil {
			return publish(finalCtx, session, Event{Kind: EventKindStatus, Status: "idle"})
		}
		return nil
	}
	// Terminal turn completion revokes ephemeral secret leases and cancels any dangling requests.
	if _, revErr := q.RevokeSecretLeasesForTurn(finalCtx, db.RevokeSecretLeasesForTurnParams{
		WorkspaceID: turn.WorkspaceID,
		TurnID:      turn.ID,
		RevokedAt:   pgtype.Timestamptz{Time: w.now(), Valid: true},
		Reason:      "turn_" + status,
	}); revErr != nil {
		return revErr
	}
	if cancelErr := secretrequests.CancelForTurnWithBroker(finalCtx, w.Pool, w.AuthBroker, uuidID(turn.WorkspaceID), uuidID(turn.ID)); cancelErr != nil {
		return cancelErr
	}
	if kind == "group" && text.Len() > 0 && status == "completed" {
		if err = w.routeGroupReply(finalCtx, turn, reply, text.String(), inputReply); err != nil {
			return err
		}
	} else if err = q.FinishRunningBotTurn(finalCtx, db.FinishRunningBotTurnParams{WorkspaceID: turn.WorkspaceID, ID: turn.ID, Status: status, ErrorCode: code}); err != nil {
		return err
	}
	if w.Publish != nil {
		return publish(finalCtx, session, Event{Kind: EventKindStatus, Status: "idle"})
	}
	return nil
}

func watchTurnStop(ctx context.Context, q *db.Queries, turn db.IsBotTurnStopRequestedParams, cancel context.CancelFunc) {
	// The request is durable. A missed check or a temporary DB failure can
	// be retried without depending on a transient queue notification.
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
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

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}
