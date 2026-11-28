// SPDX-License-Identifier: AGPL-3.0-only

// Package groups schedules bot turns for a group conversation.
// Direct messages do not enter this package. The router stays pure; this
// package is the only place that writes the pause, the counter, and the queue.
package groups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/notify"
	"github.com/dunkeltech/botbox/backend/internal/orchestrator"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrQueueFull means a selected bot already has 100 queued or running turns.
var ErrQueueFull = errors.New("bot queue is full")

// Turn is one bot_turns row the caller must publish before committing.
type Turn struct {
	WorkspaceID uuid.UUID
	BotID       uuid.UUID
	TurnID      uuid.UUID
}

type askRule struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// PlanHuman routes a human message that is already stored in q's transaction.
// A human clears every pause and the turn counter, then queues the selected bots.
func PlanHuman(ctx context.Context, q *db.Queries, workspaceID, conversationID pgtype.UUID, sender uuid.UUID, message db.Message) ([]Turn, error) {
	return plan(ctx, q, workspaceID, conversationID, orchestrator.MemberUser, sender, message, message.Content, time.Time{})
}

// PlanBotReply routes a stored bot reply. The directive stays out of the transcript.
// A reply to a request contributes to that request without opening a nested round.
func PlanBotReply(ctx context.Context, q *db.Queries, turn db.BotTurn, message db.Message, content string, inputReply bool, now time.Time) ([]Turn, error) {
	if inputReply {
		content = ""
	}
	next, err := plan(ctx, q, turn.WorkspaceID, turn.ConversationID, orchestrator.MemberBot, uuid.UUID(turn.BotID.Bytes), message, content, now)
	if err == nil && inputReply {
		err = q.RecordGroupInputReply(ctx, db.RecordGroupInputReplyParams{
			WorkspaceID: turn.WorkspaceID, ConversationID: turn.ConversationID,
			TurnID: turn.ID, ReplyMessageID: message.ID, AsOf: timestamp(now),
		})
	}
	return next, err
}

func plan(ctx context.Context, q *db.Queries, workspaceID, conversationID pgtype.UUID, senderKind orchestrator.MemberKind, sender uuid.UUID, message db.Message, content string, now time.Time) ([]Turn, error) {
	group, err := q.GetGroupForUpdate(ctx, db.GetGroupForUpdateParams{WorkspaceID: workspaceID, ID: conversationID})
	if err != nil {
		return nil, err
	}
	members, err := q.ListGroupMemberRows(ctx, db.ListGroupMemberRowsParams{WorkspaceID: workspaceID, ConversationID: conversationID})
	if err != nil {
		return nil, err
	}
	roster := rosterFrom(members)
	speakers, err := recentSpeakers(ctx, q, workspaceID, conversationID)
	if err != nil {
		return nil, err
	}
	decision := orchestrator.Route(orchestrator.Request{
		SenderKind:        senderKind,
		SenderID:          sender,
		Content:           content,
		BotTalk:           group.BotTalk,
		Policy:            orchestrator.AnswerPolicy(group.AnswerPolicy),
		LeadBotID:         uuid.UUID(group.LeadBotID.Bytes),
		TurnLimit:         int(group.TurnLimit),
		TurnsSinceHuman:   int(group.TurnsSinceHuman),
		RoundIndex:        int(group.RrIndex),
		Paused:            group.Paused,
		PauseReason:       orchestrator.PauseReason(group.PauseReason),
		Members:           roster,
		RecentBotSpeakers: speakers,
	})
	if senderKind == orchestrator.MemberBot && len(decision.Bots) > 0 {
		decision.Bots, err = openInputRequest(ctx, q, group, message, sender, roster, decision, now)
		if err != nil {
			return nil, err
		}
	}
	// Lock and count before writing the plan. A full queue then rolls back the
	// human message instead of pausing the group without a turn (ADR-0015).
	if err = lockBotQueues(ctx, q, workspaceID, decision.Bots); err != nil {
		return nil, err
	}
	summary, err := summaryFor(ctx, q, workspaceID, conversationID, roster, group.Summary)
	if err != nil {
		return nil, err
	}
	since, err := int32Counter(decision.NextTurnsSinceHuman)
	if err != nil {
		return nil, err
	}
	rr, err := int32Counter(decision.NextRoundIndex)
	if err != nil {
		return nil, err
	}
	if err = q.ApplyGroupPlan(ctx, db.ApplyGroupPlanParams{
		WorkspaceID:     workspaceID,
		ID:              conversationID,
		TurnsSinceHuman: since,
		Paused:          decision.Pause,
		PauseReason:     string(decision.Reason),
		RrIndex:         rr,
		Summary:         summary,
	}); err != nil {
		return nil, err
	}
	if decision.Pause && !group.Paused {
		if err = postPause(ctx, q, group, decision.Reason); err != nil {
			return nil, err
		}
	}
	turns := make([]Turn, 0, len(decision.Bots))
	for _, botID := range decision.Bots {
		var row db.BotTurn
		if senderKind == orchestrator.MemberBot {
			row, err = createInputRecipient(ctx, q, message, botID, roster)
		} else {
			row, err = q.CreateBotTurn(ctx, db.CreateBotTurnParams{
				WorkspaceID: workspaceID, BotID: pgID(botID), ConversationID: conversationID, MessageID: message.ID,
			})
		}
		if err != nil {
			return nil, err
		}
		turns = append(turns, Turn{WorkspaceID: uuid.UUID(workspaceID.Bytes), BotID: botID, TurnID: uuid.UUID(row.ID.Bytes)})
	}
	return turns, nil
}

func postPause(ctx context.Context, q *db.Queries, group db.Conversation, reason orchestrator.PauseReason) error {
	var content string
	switch reason {
	case orchestrator.PauseTurnLimit:
		content = fmt.Sprintf("Turn %d of %d · paused, waiting for you", group.TurnLimit, group.TurnLimit)
	case orchestrator.PauseLoop:
		content = "Paused · bots are handing work back and forth"
	default:
		return nil
	}
	_, err := q.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ID:             pgID(uuid.New()),
		WorkspaceID:    group.WorkspaceID,
		ConversationID: group.ID,
		SenderType:     "system",
		Kind:           "system",
		Content:        content,
		Metadata:       []byte("{}"),
	})
	return err
}

// NoteClaim admits one group turn under the conversation lock. Direct messages
// always proceed. Rechecking at claim prevents a queued fan-out exceeding the cap.
func NoteClaim(ctx context.Context, q *db.Queries, workspaceID, conversationID pgtype.UUID) (bool, error) {
	group, err := q.GetGroupForUpdate(ctx, db.GetGroupForUpdateParams{WorkspaceID: workspaceID, ID: conversationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if group.Paused {
		return false, nil
	}
	if group.TurnsSinceHuman >= group.TurnLimit {
		if err = q.ApplyGroupPlan(ctx, db.ApplyGroupPlanParams{WorkspaceID: workspaceID, ID: conversationID, TurnsSinceHuman: group.TurnsSinceHuman, Paused: true, PauseReason: string(orchestrator.PauseTurnLimit), RrIndex: group.RrIndex, Summary: group.Summary}); err != nil {
			return false, err
		}
		return false, postPause(ctx, q, group, orchestrator.PauseTurnLimit)
	}
	_, err = q.IncrementGroupTurnsSinceHuman(ctx, db.IncrementGroupTurnsSinceHumanParams{WorkspaceID: workspaceID, ID: conversationID})
	return err == nil, err
}

// Preface is the group brief for one bot turn. A direct message returns an empty string.
func Preface(ctx context.Context, q *db.Queries, workspaceID, conversationID, botID pgtype.UUID, now time.Time) (string, error) {
	group, err := q.GetGroup(ctx, db.GetGroupParams{WorkspaceID: workspaceID, ID: conversationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	members, err := q.ListGroupMemberRows(ctx, db.ListGroupMemberRowsParams{WorkspaceID: workspaceID, ConversationID: conversationID})
	if err != nil {
		return "", err
	}
	roster := rosterFrom(members)
	transcript, err := q.ListGroupTranscript(ctx, db.ListGroupTranscriptParams{WorkspaceID: workspaceID, ConversationID: conversationID})
	if err != nil {
		return "", err
	}
	recent := transcript
	if len(recent) > 50 {
		recent = recent[len(recent)-50:]
	}
	lines := make([]orchestrator.ContextMessage, 0, len(recent))
	for _, row := range recent {
		lines = append(lines, orchestrator.ContextMessage{Speaker: speakerName(roster, row), Body: row.Content})
	}
	var rules []askRule
	if len(group.AskBefore) > 0 {
		if err = json.Unmarshal(group.AskBefore, &rules); err != nil {
			return "", err
		}
	}
	ask := make([]orchestrator.AskRule, 0, len(rules))
	for _, rule := range rules {
		ask = append(ask, orchestrator.AskRule{Kind: rule.Kind, Label: rule.Label})
	}
	progress, err := openInputProgress(ctx, q, workspaceID, conversationID, botID, now)
	if err != nil {
		return "", err
	}
	return orchestrator.BuildContext(orchestrator.ContextInput{
		InputProgress: progress,
		BotID:         uuid.UUID(botID.Bytes),
		LeadBotID:     uuid.UUID(group.LeadBotID.Bytes),
		Goal:          group.Goal,
		Members:       roster,
		BotTalk:       group.BotTalk,
		TurnLimit:     int(group.TurnLimit),
		Policy:        orchestrator.AnswerPolicy(group.AnswerPolicy),
		AskBefore:     ask,
		Summary:       group.Summary,
		Recent:        lines,
	}), nil
}

// ScreenBudget applies the conversation cap before a group turn calls a model.
// Unknown metered cost blocks the turn. A direct message is always allowed here;
// workspace and bot budgets stay in the existing check. signal is ids only and
// runs after commit. Nil is safe.
func ScreenBudget(ctx context.Context, pool *pgxpool.Pool, workspaceID, conversationID, botID, triggerMessageID pgtype.UUID, signal notify.Signal) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	// An opaque metered request settles under this lock. Its provisional row
	// is not a billing failure and must not pause concurrently starting peers.
	if err = q.LockWorkspaceBudgetCheck(ctx, uuid.UUID(workspaceID.Bytes).String()); err != nil {
		return false, err
	}
	group, err := q.GetGroupForUpdate(ctx, db.GetGroupForUpdateParams{WorkspaceID: workspaceID, ID: conversationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !group.BudgetLimitMicros.Valid {
		return false, nil
	}
	spend, err := q.GroupSpend(ctx, db.GroupSpendParams{WorkspaceID: workspaceID, ConversationID: conversationID})
	if err != nil {
		return false, err
	}
	limit := group.BudgetLimitMicros.Int64
	var noteIDs []uuid.UUID
	// 80% is a single marker. Later turns must not repeat the message or the notice.
	if spend.UnknownRows == 0 && spend.SpentMicros < limit && spend.SpentMicros*100 >= limit*80 {
		warned, err := q.HasGroupBudgetWarning(ctx, db.HasGroupBudgetWarningParams{WorkspaceID: workspaceID, ConversationID: conversationID})
		if err != nil {
			return false, err
		}
		if !warned {
			warning, err := q.CreateChatMessage(ctx, db.CreateChatMessageParams{
				ID:             pgID(uuid.New()),
				WorkspaceID:    workspaceID,
				ConversationID: conversationID,
				SenderType:     "system",
				Kind:           "system",
				Content:        "Budget 80% · this group is close to its cap",
				Metadata:       []byte(`{"budgetWarning":true}`),
			})
			if err != nil {
				return false, err
			}
			noteIDs, err = notify.BudgetWarning(ctx, q, workspaceID, conversationID, warning.ID)
			if err != nil {
				return false, err
			}
		}
	}
	if spend.UnknownRows == 0 && spend.SpentMicros < limit {
		if err = tx.Commit(ctx); err != nil {
			return false, err
		}
		signal.Send(ctx, uuid.UUID(workspaceID.Bytes), uuid.UUID(conversationID.Bytes), noteIDs)
		return false, nil
	}
	// At the cap, or when a metered row has no cost, do not call the model.
	if err = q.ApplyGroupPlan(ctx, db.ApplyGroupPlanParams{
		WorkspaceID:     workspaceID,
		ID:              conversationID,
		TurnsSinceHuman: group.TurnsSinceHuman,
		Paused:          true,
		PauseReason:     string(orchestrator.PauseBudget),
		RrIndex:         group.RrIndex,
		Summary:         group.Summary,
	}); err != nil {
		return false, err
	}
	if group.PauseReason != string(orchestrator.PauseBudget) {
		if _, err = q.CreateChatMessage(ctx, db.CreateChatMessageParams{
			ID:             pgID(uuid.New()),
			WorkspaceID:    workspaceID,
			ConversationID: conversationID,
			SenderType:     "system",
			Kind:           "system",
			Content:        "Budget spent · paused, waiting for a higher cap",
			Metadata:       []byte("{}"),
		}); err != nil {
			return false, err
		}
	}
	payload, err := json.Marshal(map[string]string{"triggerMessageId": uuid.UUID(triggerMessageID.Bytes).String()})
	if err != nil {
		return false, err
	}
	approvalID, err := q.CreateGroupBudgetApproval(ctx, db.CreateGroupBudgetApprovalParams{
		WorkspaceID:    workspaceID,
		BotID:          botID,
		ConversationID: conversationID,
		Payload:        payload,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if err = tx.Commit(ctx); err != nil {
			return false, err
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	card, err := q.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ID:             pgID(uuid.New()),
		WorkspaceID:    workspaceID,
		ConversationID: conversationID,
		SenderType:     "system",
		Kind:           "approval",
		Content:        "Raise the group budget",
		Metadata:       []byte(`{"kind":"raise_budget"}`),
	})
	if err != nil {
		return false, err
	}
	if err = q.AttachApprovalMessage(ctx, db.AttachApprovalMessageParams{
		WorkspaceID: workspaceID,
		ID:          approvalID,
		MessageID:   card.ID,
	}); err != nil {
		return false, err
	}
	noteIDs, err = notify.Approval(ctx, q, workspaceID, conversationID, card.ID, approvalID)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	signal.Send(ctx, uuid.UUID(workspaceID.Bytes), uuid.UUID(conversationID.Bytes), noteIDs)
	return true, nil
}

func rosterFrom(rows []db.ListGroupMemberRowsRow) []orchestrator.Member {
	out := make([]orchestrator.Member, 0, len(rows))
	for _, row := range rows {
		kind := orchestrator.MemberUser
		instructions := row.UserRoleDescription
		if row.MemberType == "bot" {
			kind = orchestrator.MemberBot
			instructions = row.BotDescription
		}
		out = append(out, orchestrator.Member{
			Kind:         kind,
			ID:           uuid.UUID(row.MemberID.Bytes),
			Name:         row.DisplayName,
			Role:         row.RoleText,
			Instructions: instructions,
		})
	}
	return out
}

func recentSpeakers(ctx context.Context, q *db.Queries, workspaceID, conversationID pgtype.UUID) ([]uuid.UUID, error) {
	rows, err := q.ListRecentGroupBotSpeakers(ctx, db.ListRecentGroupBotSpeakersParams{WorkspaceID: workspaceID, ConversationID: conversationID})
	if err != nil {
		return nil, err
	}
	out := make([]uuid.UUID, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		out = append(out, uuid.UUID(rows[i].Bytes))
	}
	return out, nil
}

func summaryFor(ctx context.Context, q *db.Queries, workspaceID, conversationID pgtype.UUID, roster []orchestrator.Member, current string) (string, error) {
	rows, err := q.ListGroupTranscript(ctx, db.ListGroupTranscriptParams{WorkspaceID: workspaceID, ConversationID: conversationID})
	if err != nil {
		return "", err
	}
	// The live window is the last 50 conversation messages. Older lines are copied, not
	// summarized by a model, and the column stops at 8 KiB (ADR-0015).
	if len(rows) <= 50 {
		return current, nil
	}
	older := rows[:len(rows)-50]
	lines := make([]string, 0, len(older))
	for _, row := range older {
		lines = append(lines, speakerName(roster, row)+": "+row.Content)
	}
	return clipUTF8(strings.Join(lines, "\n"), 8192), nil
}

func speakerName(roster []orchestrator.Member, row db.ListGroupTranscriptRow) string {
	id := uuid.UUID(row.SenderUserID.Bytes)
	kind := orchestrator.MemberUser
	if row.SenderType == "bot" {
		id = uuid.UUID(row.SenderBotID.Bytes)
		kind = orchestrator.MemberBot
	}
	for _, member := range roster {
		if member.Kind == kind && member.ID == id && member.Name != "" {
			if row.Kind == "progress" {
				return member.Name + " (progress update)"
			}
			return member.Name
		}
	}
	if row.SenderType == "bot" {
		return "bot"
	}
	return "user"
}

func clipUTF8(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	cut := text[len(text)-maxBytes:]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[1:]
	}
	return cut
}

func pgID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

// int32Counter rejects a router counter that cannot be stored. The columns are
// int32, and the router only copies a stored value or a member index.
func int32Counter(n int) (int32, error) {
	if n < 0 || n > math.MaxInt32 {
		return 0, fmt.Errorf("group counter %d is out of range", n)
	}
	return int32(n), nil // #nosec G115 -- rejected values outside the int32 range above
}
