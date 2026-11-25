// SPDX-License-Identifier: AGPL-3.0-only

package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/egresspolicy"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const queueCap = 100

var (
	// ErrConflict means the approval was already decided.
	ErrConflict = errors.New("approval already decided")
	// ErrHostNotBlocked means the host is already allowed, so the card stays pending.
	ErrHostNotBlocked = errors.New("host is not blocked")
	// ErrCapNotRaised means spend is still at the cap or a cost is unknown.
	ErrCapNotRaised = errors.New("budget cap is not above spend")
	// ErrQueueFull means the resumed bot already has 100 queued or running turns.
	ErrQueueFull = errors.New("bot queue is full")
	// ErrInvalid means the decision or the stored card cannot be applied.
	ErrInvalid = errors.New("invalid approval decision")
)

// Turn is the bot turn the caller must publish before committing an approval.
type Turn struct {
	WorkspaceID uuid.UUID
	BotID       uuid.UUID
	TurnID      uuid.UUID
}

// ApplyOptions carries caller authorization context for policy-changing approvals.
type ApplyOptions struct {
	CanManageBot bool
}

// Apply records a decision. Approving an approval pause queues the waiting
// bot and does not reset the turn counter. A turn-limit, loop, or budget
// pause stays. Denying posts a system line and leaves the group paused.
// The caller publishes Turn before commit.
func Apply(ctx context.Context, q *db.Queries, approval db.Approval, decision string, actor pgtype.UUID, now time.Time, opts ...ApplyOptions) (*Turn, error) {
	if decision != "approved" && decision != "denied" {
		return nil, ErrInvalid
	}
	if approval.Status != "pending" {
		return nil, ErrConflict
	}
	var applyOpts ApplyOptions
	if len(opts) > 0 {
		applyOpts = opts[0]
	}
	if decision == "approved" {
		if err := allow(ctx, q, approval, actor, now, applyOpts); err != nil {
			return nil, err
		}
	} else if approval.Kind == kindMCPTool {
		if _, err := q.UpdateMCPToolGrantByApproval(ctx, db.UpdateMCPToolGrantByApprovalParams{
			WorkspaceID: approval.WorkspaceID,
			ApprovalID:  approval.ID,
			Status:      "denied",
		}); err != nil {
			return nil, err
		}
	}
	n, err := q.DecideApproval(ctx, db.DecideApprovalParams{
		WorkspaceID: approval.WorkspaceID,
		ID:          approval.ID,
		Status:      decision,
		DecidedBy:   actor,
	})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrConflict
	}
	meta, err := json.Marshal(map[string]string{"decision": decision, "kind": approval.Kind})
	if err != nil {
		return nil, err
	}
	if _, err = q.CreateAuditLog(ctx, db.CreateAuditLogParams{
		WorkspaceID: approval.WorkspaceID,
		ActorUserID: actor,
		Action:      "approval.decided",
		TargetType:  pgtype.Text{String: "approval", Valid: true},
		TargetID:    approval.ID,
		Metadata:    meta,
	}); err != nil {
		return nil, err
	}
	if !approval.ConversationID.Valid {
		return nil, nil
	}
	if decision == "denied" {
		_, err = q.CreateChatMessage(ctx, db.CreateChatMessageParams{
			ID:             pgID(uuid.New()),
			WorkspaceID:    approval.WorkspaceID,
			ConversationID: approval.ConversationID,
			SenderType:     "system",
			Kind:           "system",
			Content:        "Denied · this request stays blocked",
			Metadata:       []byte("{}"),
		})
		return nil, err
	}
	cleared, err := q.ClearApprovalPause(ctx, db.ClearApprovalPauseParams{
		WorkspaceID: approval.WorkspaceID,
		ID:          approval.ConversationID,
	})
	if err != nil {
		return nil, err
	}
	// Only an approval pause resumes a group. A turn limit stays in place.
	// Direct messages do not set group pause flags, so approved DM cards still
	// queue continuation when their kind can resume a DM turn.
	if cleared != 1 {
		if !resumableDMKind(approval.Kind) {
			return nil, nil
		}
		convType, typeErr := q.GetConversationType(ctx, db.GetConversationTypeParams{
			WorkspaceID: approval.WorkspaceID,
			ID:          approval.ConversationID,
		})
		if typeErr != nil || convType != "dm" {
			return nil, typeErr
		}
	}
	return queueResume(ctx, q, approval)
}

func resumableDMKind(kind string) bool {
	switch kind {
	case kindMCPTool, kindNewHost, kindPushProtected, kindCustom:
		return true
	default:
		return false
	}
}

func allow(ctx context.Context, q *db.Queries, approval db.Approval, actor pgtype.UUID, now time.Time, opts ApplyOptions) error {
	switch approval.Kind {
	case kindNewHost:
		return allowHost(ctx, q, approval, actor, opts.CanManageBot)
	case kindRaiseBudget:
		if approval.ConversationID.Valid {
			return allowGroupCap(ctx, q, approval)
		}
		return allowBudget(ctx, q, approval, now)
	case kindMCPTool:
		_, err := q.UpdateMCPToolGrantByApproval(ctx, db.UpdateMCPToolGrantByApprovalParams{
			WorkspaceID: approval.WorkspaceID,
			ApprovalID:  approval.ID,
			Status:      "approved",
		})
		return err
	default:
		return nil
	}
}

func allowHost(ctx context.Context, q *db.Queries, approval db.Approval, actor pgtype.UUID, canManageBot bool) error {
	var payload cardPayload
	if err := json.Unmarshal(approval.Payload, &payload); err != nil || payload.Host == "" {
		return ErrInvalid
	}
	var actorUUID uuid.UUID
	if actor.Valid {
		actorUUID = uuid.UUID(actor.Bytes)
	}
	_, err := egresspolicy.ApproveNewHostTx(
		ctx,
		q,
		uuid.UUID(approval.WorkspaceID.Bytes),
		uuid.UUID(approval.BotID.Bytes),
		actorUUID,
		payload.Host,
		canManageBot,
	)
	if errors.Is(err, egresspolicy.ErrAlreadyAllowed) {
		return ErrHostNotBlocked
	}
	return err
}

func allowGroupCap(ctx context.Context, q *db.Queries, approval db.Approval) error {
	group, err := q.GetGroupForUpdate(ctx, db.GetGroupForUpdateParams{
		WorkspaceID: approval.WorkspaceID,
		ID:          approval.ConversationID,
	})
	if err != nil {
		return err
	}
	spend, err := q.GroupSpend(ctx, db.GroupSpendParams{
		WorkspaceID:    approval.WorkspaceID,
		ConversationID: approval.ConversationID,
	})
	if err != nil {
		return err
	}
	if !group.BudgetLimitMicros.Valid || spend.UnknownRows != 0 || spend.SpentMicros >= group.BudgetLimitMicros.Int64 {
		return ErrCapNotRaised
	}
	return nil
}

func allowBudget(ctx context.Context, q *db.Queries, approval db.Approval, now time.Time) error {
	budget, err := q.GetBudgetForUpdate(ctx, db.GetBudgetForUpdateParams{
		WorkspaceID: approval.WorkspaceID,
		ID:          approval.BudgetID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalid
	}
	if err != nil {
		return err
	}
	states, err := q.ListBudgetState(ctx, db.ListBudgetStateParams{
		WorkspaceID: approval.WorkspaceID,
		BotID:       budget.BotID,
		AsOf:        pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return err
	}
	var spent int64
	var limit int64
	found := false
	for _, state := range states {
		if state.ID == budget.ID {
			spent, limit, found = state.SpentMicros, state.LimitMicros, true
			break
		}
	}
	if !found || spent >= limit {
		return ErrCapNotRaised
	}
	unknown, err := q.HasUnpricedAPIUsage(ctx, db.HasUnpricedAPIUsageParams{
		WorkspaceID: approval.WorkspaceID,
		BotID:       budget.BotID,
		Period:      budget.Period,
		AsOf:        pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return err
	}
	if unknown {
		return ErrCapNotRaised
	}
	return nil
}

func queueResume(ctx context.Context, q *db.Queries, approval db.Approval) (*Turn, error) {
	messageID, err := resumeMessage(ctx, q, approval)
	if err != nil {
		return nil, err
	}
	if err = q.LockBotQueue(ctx, uuid.UUID(approval.WorkspaceID.Bytes).String()+"/"+uuid.UUID(approval.BotID.Bytes).String()); err != nil {
		return nil, err
	}
	pending, err := q.CountPendingBotTurns(ctx, db.CountPendingBotTurnsParams{
		WorkspaceID: approval.WorkspaceID,
		BotID:       approval.BotID,
	})
	if err != nil {
		return nil, err
	}
	if pending >= queueCap {
		return nil, ErrQueueFull
	}
	row, err := q.CreateBotTurn(ctx, db.CreateBotTurnParams{
		WorkspaceID:    approval.WorkspaceID,
		BotID:          approval.BotID,
		ConversationID: approval.ConversationID,
		MessageID:      messageID,
	})
	if err != nil {
		return nil, err
	}
	return &Turn{
		WorkspaceID: uuid.UUID(approval.WorkspaceID.Bytes),
		BotID:       uuid.UUID(approval.BotID.Bytes),
		TurnID:      uuid.UUID(row.ID.Bytes),
	}, nil
}

func resumeMessage(ctx context.Context, q *db.Queries, approval db.Approval) (pgtype.UUID, error) {
	var payload cardPayload
	_ = json.Unmarshal(approval.Payload, &payload)
	if id, err := uuid.Parse(payload.TriggerMessageID); err == nil {
		msg, err := q.GetTurnMessage(ctx, db.GetTurnMessageParams{
			WorkspaceID: approval.WorkspaceID,
			ID:          pgID(id),
		})
		if err == nil && msg.ConversationID == approval.ConversationID && msg.Kind == "text" && (msg.SenderType == "user" || msg.SenderType == "bot") {
			return msg.ID, nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, err
		}
	}
	id, err := q.LatestRunnableGroupMessage(ctx, db.LatestRunnableGroupMessageParams{
		WorkspaceID:    approval.WorkspaceID,
		ConversationID: approval.ConversationID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, ErrInvalid
	}
	return id, err
}

func hostAllowed(policy string, patterns []string, host string) bool {
	allowed, _ := mitmproxy.EvaluateHostAccess(mitmproxy.EgressPolicy(policy), patterns, nil, host)
	return allowed
}
