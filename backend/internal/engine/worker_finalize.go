package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/orchestrator"
	"github.com/dunkeltech/botbox/backend/internal/secretrequests"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type turnOutcome struct {
	status string
	code   string
}

func outcomeForTurn(err error, waitingSecret bool) turnOutcome {
	switch {
	case waitingSecret || errors.Is(err, ErrTurnWaitingSecret):
		return turnOutcome{status: "waiting_secret"}
	case errors.Is(err, llmproxy.ErrBudgetExceeded):
		return turnOutcome{status: "budget_exceeded", code: "budget_exceeded"}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return turnOutcome{status: "interrupted", code: "engine.interrupted"}
	case err != nil:
		return turnOutcome{status: "failed", code: TurnFailureCode(err)}
	default:
		return turnOutcome{status: "completed"}
	}
}

func (w *Worker) finalizeTurn(ctx context.Context, q *db.Queries, events *turnEvents, outcome turnOutcome, inputReply bool) error {
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	turn := events.turn
	kind, err := q.GetConversationType(finalCtx, db.GetConversationTypeParams{WorkspaceID: turn.WorkspaceID, ID: turn.ConversationID})
	if err != nil {
		return err
	}
	var reply db.Message
	if events.text.Len() > 0 {
		content := events.text.String()
		if kind == "group" {
			content, _, _ = orchestrator.SplitInputDirective(content)
		}
		reply, err = q.CreateChatMessage(finalCtx, db.CreateChatMessageParams{
			ID: pgID(uuid.New()), WorkspaceID: turn.WorkspaceID, ConversationID: turn.ConversationID,
			SenderType: "bot", SenderBotID: turn.BotID, Kind: "text", Content: content, Metadata: []byte("{}"),
		})
		if err != nil {
			return fmt.Errorf("persist bot reply: %w", err)
		}
	}
	if outcome.status == "waiting_secret" {
		return events.publish(finalCtx, *events.session, Event{Kind: EventKindStatus, Status: "idle"})
	}

	// Terminal completion revokes short-lived grants before another turn can start.
	if _, err := q.RevokeSecretLeasesForTurn(finalCtx, db.RevokeSecretLeasesForTurnParams{
		WorkspaceID: turn.WorkspaceID,
		TurnID:      turn.ID,
		RevokedAt:   pgtype.Timestamptz{Time: w.now(), Valid: true},
		Reason:      "turn_" + outcome.status,
	}); err != nil {
		return err
	}
	if err := secretrequests.CancelForTurnWithBroker(finalCtx, w.Pool, w.AuthBroker, uuidID(turn.WorkspaceID), uuidID(turn.ID)); err != nil {
		return err
	}
	if kind == "group" && events.text.Len() > 0 && outcome.status == "completed" {
		if err := w.routeGroupReply(finalCtx, turn, reply, events.text.String(), inputReply); err != nil {
			return err
		}
	} else if err := q.FinishRunningBotTurn(finalCtx, db.FinishRunningBotTurnParams{
		WorkspaceID: turn.WorkspaceID, ID: turn.ID, Status: outcome.status, ErrorCode: outcome.code,
	}); err != nil {
		return err
	}
	return events.publish(finalCtx, *events.session, Event{Kind: EventKindStatus, Status: "idle"})
}
