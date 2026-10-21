// Package notify stores mention, approval, and budget rows for human members.
// Routing still does not create a turn for a human. The row is the durable
// notice; a missed NATS signal is recovered by reloading this list (issue #17).
package notify

import (
	"context"
	"encoding/json"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/orchestrator"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	kindMention  = "mention"
	kindApproval = "approval"
	kindBudget   = "budget"
)

// Signal publishes notification ids after the rows commit. Nil is safe.
// Callers must not put message text in the payload.
type Signal func(ctx context.Context, workspaceID, conversationID uuid.UUID, ids []uuid.UUID)

// Send skips an empty batch and a missing conversation. Workspace-budget
// approvals have no conversation channel, so they stay on the list endpoint.
func (s Signal) Send(ctx context.Context, workspaceID, conversationID uuid.UUID, ids []uuid.UUID) {
	if s == nil || len(ids) == 0 || conversationID == uuid.Nil {
		return
	}
	s(ctx, workspaceID, conversationID, ids)
}

type signalBody struct {
	NotificationIDs []uuid.UUID `json:"notificationIds"`
	ConversationID  uuid.UUID   `json:"conversationId"`
}

// MarshalSignal encodes ids only. Message text stays in Postgres.
func MarshalSignal(conversationID uuid.UUID, ids []uuid.UUID) ([]byte, error) {
	return json.Marshal(signalBody{NotificationIDs: ids, ConversationID: conversationID})
}

// HumanMentions inserts one mention row per named human who has not muted the
// group. The same name twice is one row. q must be the transaction that owns
// the message, so a rolled-back post does not leave a notice behind.
func HumanMentions(ctx context.Context, q *db.Queries, workspaceID, conversationID, messageID pgtype.UUID, content string) ([]uuid.UUID, error) {
	rows, err := q.ListConversationNotify(ctx, db.ListConversationNotifyParams{WorkspaceID: workspaceID, ConversationID: conversationID})
	if err != nil {
		return nil, err
	}
	_, humans := orchestrator.ParseMentions(content, roster(rows))
	return insertEach(ctx, q, workspaceID, conversationID, messageID, pgtype.UUID{}, kindMention, mentionIDs(humans, people(rows)))
}

// HumanMentionsCommit stores mentions in their own transaction. A bot reply is
// already durable when routing runs, and a full follow-up queue rolls the plan
// back. The mention still has to stick to the stored reply.
func HumanMentionsCommit(ctx context.Context, pool *pgxpool.Pool, workspaceID, conversationID, messageID pgtype.UUID, content string) ([]uuid.UUID, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ids, err := HumanMentions(ctx, db.New(tx), workspaceID, conversationID, messageID, content)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return ids, nil
}

// Approval notifies humans who may decide this group card. notify=mentions and
// mute skip it. A duplicate pending card must not call this again.
func Approval(ctx context.Context, q *db.Queries, workspaceID, conversationID, messageID, approvalID pgtype.UUID) ([]uuid.UUID, error) {
	rows, err := q.ListConversationNotify(ctx, db.ListConversationNotifyParams{WorkspaceID: workspaceID, ConversationID: conversationID})
	if err != nil {
		return nil, err
	}
	deciders, err := q.ListWorkspaceDeciders(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return insertEach(ctx, q, workspaceID, conversationID, messageID, approvalID, kindApproval, approvalIDs(people(rows), deciderPeople(deciders)))
}

// BudgetWarning notifies members with notify=all. The caller inserts it only
// on the turn that crosses 80%, so later turns above the line do not repeat it.
func BudgetWarning(ctx context.Context, q *db.Queries, workspaceID, conversationID, messageID pgtype.UUID) ([]uuid.UUID, error) {
	rows, err := q.ListConversationNotify(ctx, db.ListConversationNotifyParams{WorkspaceID: workspaceID, ConversationID: conversationID})
	if err != nil {
		return nil, err
	}
	return insertEach(ctx, q, workspaceID, conversationID, messageID, pgtype.UUID{}, kindBudget, budgetIDs(people(rows)))
}

// WorkspaceApproval notifies owners and admins of a Phase 4 budget card.
// There is no group notify preference on that path.
func WorkspaceApproval(ctx context.Context, q *db.Queries, workspaceID, approvalID pgtype.UUID) ([]uuid.UUID, error) {
	deciders, err := q.ListWorkspaceDeciders(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(deciders))
	seen := map[uuid.UUID]bool{}
	for _, row := range deciders {
		id := uuid.UUID(row.UserID.Bytes)
		if id == uuid.Nil || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return insertEach(ctx, q, workspaceID, pgtype.UUID{}, pgtype.UUID{}, approvalID, kindApproval, ids)
}

func insertEach(ctx context.Context, q *db.Queries, workspaceID, conversationID, messageID, approvalID pgtype.UUID, kind string, users []uuid.UUID) ([]uuid.UUID, error) {
	out := make([]uuid.UUID, 0, len(users))
	for _, userID := range users {
		id, err := q.InsertNotification(ctx, db.InsertNotificationParams{
			WorkspaceID:    workspaceID,
			UserID:         pgtype.UUID{Bytes: userID, Valid: true},
			Kind:           kind,
			ConversationID: conversationID,
			MessageID:      messageID,
			ApprovalID:     approvalID,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, uuid.UUID(id.Bytes))
	}
	return out, nil
}

func people(rows []db.ListConversationNotifyRow) []person {
	out := make([]person, 0, len(rows))
	for _, row := range rows {
		if row.MemberType != "user" {
			continue
		}
		out = append(out, person{ID: uuid.UUID(row.MemberID.Bytes), Notify: row.Notify, Role: row.Role})
	}
	return out
}

func deciderPeople(rows []db.ListWorkspaceDecidersRow) []person {
	out := make([]person, 0, len(rows))
	for _, row := range rows {
		out = append(out, person{ID: uuid.UUID(row.UserID.Bytes), Role: row.Role, Notify: notifyAll})
	}
	return out
}

func roster(rows []db.ListConversationNotifyRow) []orchestrator.Member {
	out := make([]orchestrator.Member, 0, len(rows))
	for _, row := range rows {
		kind := orchestrator.MemberUser
		if row.MemberType == "bot" {
			kind = orchestrator.MemberBot
		}
		out = append(out, orchestrator.Member{Kind: kind, ID: uuid.UUID(row.MemberID.Bytes), Name: row.DisplayName})
	}
	return out
}
