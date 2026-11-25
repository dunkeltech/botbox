// SPDX-License-Identifier: AGPL-3.0-only

// Package approvals records ask-before cards and applies a human decision.
// The proxy, the worker, and the HTTP handler share Open so a turn-limit,
// loop, or budget pause is never replaced by an approval pause (ADR-0015).
package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/notify"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	kindNewHost       = "new_host"
	kindRaiseBudget   = "raise_budget"
	kindPushProtected = "push_protected"
	kindCustom        = "custom"
	kindMCPTool       = "mcp_tool"
)

// Card is one ask-before request. TriggerMessageID is the user or bot text
// the resumed turn reads. It is not the approval card itself.
type Card struct {
	WorkspaceID      pgtype.UUID
	ConversationID   pgtype.UUID
	BotID            pgtype.UUID
	TriggerMessageID pgtype.UUID
	Kind             string
	Host             string
	Branch           string
	Label            string
}

type askRule struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

type cardPayload struct {
	Host             string `json:"host,omitempty"`
	Branch           string `json:"branch,omitempty"`
	Label            string `json:"label,omitempty"`
	ServerSlug       string `json:"serverSlug,omitempty"`
	ToolName         string `json:"toolName,omitempty"`
	ArgsHash         string `json:"argsHash,omitempty"`
	TriggerMessageID string `json:"triggerMessageId,omitempty"`
}

// Open inserts one pending approval and a kind=approval message for a group or
// direct message when that conversation's ask-before rules include the kind. A
// duplicate pending payload is ignored. An existing group pause is left as it
// is. The returned ids are new notification rows for the humans who may decide.
// The caller publishes them after commit.
func Open(ctx context.Context, q *db.Queries, card Card) ([]uuid.UUID, error) {
	conv, askRules, ok, err := askRulesForConversation(ctx, q, card.WorkspaceID, card.ConversationID, card.BotID)
	if err != nil || !ok {
		return nil, err
	}
	if conv.Type == "dm" && card.Kind == kindRaiseBudget {
		return nil, nil
	}
	if !ruleListed(askRules, card.Kind) {
		return nil, nil
	}
	payload, err := json.Marshal(cardPayload{
		Host:             card.Host,
		Branch:           card.Branch,
		Label:            card.Label,
		TriggerMessageID: triggerID(card.TriggerMessageID),
	})
	if err != nil {
		return nil, err
	}
	approvalID, err := q.CreateAskApproval(ctx, db.CreateAskApprovalParams{
		WorkspaceID:    card.WorkspaceID,
		BotID:          card.BotID,
		ConversationID: card.ConversationID,
		Kind:           card.Kind,
		Payload:        payload,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	meta, err := json.Marshal(map[string]string{"kind": card.Kind})
	if err != nil {
		return nil, err
	}
	message, err := q.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ID:             pgID(uuid.New()),
		WorkspaceID:    card.WorkspaceID,
		ConversationID: card.ConversationID,
		SenderType:     "system",
		Kind:           "approval",
		Content:        cardContent(card),
		Metadata:       meta,
	})
	if err != nil {
		return nil, err
	}
	if err = q.AttachApprovalMessage(ctx, db.AttachApprovalMessageParams{
		WorkspaceID: card.WorkspaceID,
		ID:          approvalID,
		MessageID:   message.ID,
	}); err != nil {
		return nil, err
	}
	if conv.Type == "group" {
		if err = q.PauseGroupForApproval(ctx, db.PauseGroupForApprovalParams{
			WorkspaceID: card.WorkspaceID,
			ID:          card.ConversationID,
		}); err != nil {
			return nil, err
		}
		return notify.Approval(ctx, q, card.WorkspaceID, card.ConversationID, message.ID, approvalID)
	}
	if !conv.UserID.Valid {
		return nil, nil
	}
	nid, err := q.InsertNotification(ctx, db.InsertNotificationParams{
		WorkspaceID:    card.WorkspaceID,
		UserID:         conv.UserID,
		Kind:           "approval",
		ConversationID: card.ConversationID,
		MessageID:      message.ID,
		ApprovalID:     approvalID,
	})
	if err != nil {
		return nil, err
	}
	return []uuid.UUID{uuid.UUID(nid.Bytes)}, nil
}

// NoteBlockedHost records a new_host card when a group or DM turn is running
// and the group or bot asked before a new host. An allowed host never reaches
// here. signal runs after commit and may be nil.
func NoteBlockedHost(ctx context.Context, pool *pgxpool.Pool, workspaceID, botID uuid.UUID, host string, signal notify.Signal) error {
	host, ok := normalizeHost(host)
	if !ok {
		return nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	turn, err := q.GetRunningBotTurn(ctx, db.GetRunningBotTurnParams{
		WorkspaceID: pgID(workspaceID),
		BotID:       pgID(botID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	ids, err := Open(ctx, q, Card{
		WorkspaceID:      turn.WorkspaceID,
		ConversationID:   turn.ConversationID,
		BotID:            turn.BotID,
		TriggerMessageID: turn.MessageID,
		Kind:             kindNewHost,
		Host:             host,
	})
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	signal.Send(ctx, workspaceID, uuid.UUID(turn.ConversationID.Bytes), ids)
	return nil
}

// NoteTool records a push_protected card from a tool-call sentence. The card
// is the gate: bot.Box does not push, and a write failure fails the turn.
func NoteTool(ctx context.Context, pool *pgxpool.Pool, turn db.BotTurn, text string, signal notify.Signal) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ids, err := note(ctx, db.New(tx), turn, func(raw []byte) []Card {
		return pushMatchesFor(raw, text)
	})
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	signal.Send(ctx, uuid.UUID(turn.WorkspaceID.Bytes), uuid.UUID(turn.ConversationID.Bytes), ids)
	return nil
}

// NoteReply records a matching custom card in the reply's finalization
// transaction so an approval pause takes effect before planning a handoff.
func NoteReply(ctx context.Context, q *db.Queries, turn db.BotTurn, text string) ([]uuid.UUID, error) {
	return note(ctx, q, turn, func(raw []byte) []Card {
		return customMatchesText(raw, text)
	})
}

func note(ctx context.Context, q *db.Queries, turn db.BotTurn, match func([]byte) []Card) ([]uuid.UUID, error) {
	_, askRules, ok, err := askRulesForConversation(ctx, q, turn.WorkspaceID, turn.ConversationID, turn.BotID)
	if err != nil || !ok {
		return nil, err
	}
	var ids []uuid.UUID
	for _, card := range match(askRules) {
		card.WorkspaceID = turn.WorkspaceID
		card.ConversationID = turn.ConversationID
		card.BotID = turn.BotID
		card.TriggerMessageID = turn.MessageID
		more, err := Open(ctx, q, card)
		if err != nil {
			return nil, err
		}
		ids = append(ids, more...)
	}
	return ids, nil
}

func askRulesForConversation(ctx context.Context, q *db.Queries, workspaceID, conversationID, botID pgtype.UUID) (db.GetApprovalConversationRow, []byte, bool, error) {
	conv, err := q.GetApprovalConversation(ctx, db.GetApprovalConversationParams{
		WorkspaceID: workspaceID,
		ID:          conversationID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.GetApprovalConversationRow{}, nil, false, nil
	}
	if err != nil {
		return db.GetApprovalConversationRow{}, nil, false, err
	}
	switch conv.Type {
	case "group":
		return conv, conv.AskBefore, true, nil
	case "dm":
		if !conv.BotID.Valid || (botID.Valid && conv.BotID != botID) {
			return db.GetApprovalConversationRow{}, nil, false, nil
		}
		bot, err := q.GetBotByWorkspaceAndID(ctx, db.GetBotByWorkspaceAndIDParams{
			WorkspaceID: workspaceID,
			ID:          conv.BotID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return db.GetApprovalConversationRow{}, nil, false, nil
		}
		if err != nil {
			return db.GetApprovalConversationRow{}, nil, false, err
		}
		return conv, bot.AskBefore, true, nil
	default:
		return db.GetApprovalConversationRow{}, nil, false, nil
	}
}

func pushMatchesFor(raw []byte, text string) []Card {
	if !containsToken(text, "push") {
		return nil
	}
	var rules []askRule
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil
	}
	var out []Card
	for _, rule := range rules {
		if rule.Kind != kindPushProtected {
			continue
		}
		label := strings.TrimSpace(rule.Label)
		branch := ""
		if label == "" {
			switch {
			case containsToken(text, "main"):
				branch = "main"
			case containsToken(text, "master"):
				branch = "master"
			default:
				continue
			}
		} else if containsToken(text, label) {
			branch = label
		} else {
			continue
		}
		out = append(out, Card{Kind: kindPushProtected, Branch: branch})
	}
	return out
}

func customMatchesText(raw []byte, text string) []Card {
	var rules []askRule
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil
	}
	var out []Card
	for _, rule := range rules {
		label := strings.TrimSpace(rule.Label)
		if rule.Kind != kindCustom || label == "" || !containsPhrase(text, label) {
			continue
		}
		out = append(out, Card{Kind: kindCustom, Label: label})
	}
	return out
}

func ruleListed(raw []byte, kind string) bool {
	var rules []askRule
	if err := json.Unmarshal(raw, &rules); err != nil {
		return false
	}
	for _, rule := range rules {
		if rule.Kind == kind {
			return true
		}
	}
	return false
}

func cardContent(card Card) string {
	switch card.Kind {
	case kindNewHost:
		return "Allow host " + card.Host
	case kindPushProtected:
		return "Protected push · " + card.Branch
	case kindCustom:
		return card.Label
	case kindMCPTool:
		return "Approve MCP command · " + card.Label
	default:
		return card.Kind
	}
}

// MCPToolRequest describes an ask-gated MCP command invocation awaiting human approval.
type MCPToolRequest struct {
	WorkspaceID uuid.UUID
	BotID       uuid.UUID
	ServerID    uuid.UUID
	ServerSlug  string
	ToolName    string
	ArgsHash    string
	Preview     string
}

// OpenMCPTool records a deduplicated mcp_tool approval card and pending grant in the
// bot's active DM or group conversation, pausing groups until a human decides.
func OpenMCPTool(ctx context.Context, pool *pgxpool.Pool, req MCPToolRequest, signal notify.Signal) (uuid.UUID, bool, error) {
	if pool == nil || req.WorkspaceID == uuid.Nil || req.BotID == uuid.Nil || req.ServerID == uuid.Nil {
		return uuid.Nil, false, errors.New("mcp approval request is incomplete")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	turn, err := q.GetRunningBotTurn(ctx, db.GetRunningBotTurnParams{
		WorkspaceID: pgID(req.WorkspaceID),
		BotID:       pgID(req.BotID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	conv, err := q.GetConversationUserID(ctx, db.GetConversationUserIDParams{
		WorkspaceID: turn.WorkspaceID,
		ID:          turn.ConversationID,
	})
	if err != nil {
		return uuid.Nil, false, err
	}

	label := req.ServerSlug + " / " + req.ToolName
	payload, err := json.Marshal(cardPayload{
		Label:            label,
		ServerSlug:       req.ServerSlug,
		ToolName:         req.ToolName,
		ArgsHash:         req.ArgsHash,
		TriggerMessageID: triggerID(turn.MessageID),
	})
	if err != nil {
		return uuid.Nil, false, err
	}
	approvalID, err := q.CreateAskApproval(ctx, db.CreateAskApprovalParams{
		WorkspaceID:    turn.WorkspaceID,
		BotID:          turn.BotID,
		ConversationID: turn.ConversationID,
		Kind:           kindMCPTool,
		Payload:        payload,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		existingID, lookupErr := q.GetPendingApprovalByPayload(ctx, db.GetPendingApprovalByPayloadParams{
			WorkspaceID:    turn.WorkspaceID,
			ConversationID: turn.ConversationID,
			Kind:           kindMCPTool,
			Payload:        payload,
		})
		if lookupErr != nil {
			return uuid.Nil, false, lookupErr
		}
		return uuid.UUID(existingID.Bytes), false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}

	meta, err := json.Marshal(map[string]string{"kind": kindMCPTool})
	if err != nil {
		return uuid.Nil, false, err
	}
	content := "Approve MCP command · " + label
	if strings.TrimSpace(req.Preview) != "" {
		content += "\n" + strings.TrimSpace(req.Preview)
	}
	message, err := q.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ID:             pgID(uuid.New()),
		WorkspaceID:    turn.WorkspaceID,
		ConversationID: turn.ConversationID,
		SenderType:     "system",
		Kind:           "approval",
		Content:        content,
		Metadata:       meta,
	})
	if err != nil {
		return uuid.Nil, false, err
	}
	if err = q.AttachApprovalMessage(ctx, db.AttachApprovalMessageParams{
		WorkspaceID: turn.WorkspaceID,
		ID:          approvalID,
		MessageID:   message.ID,
	}); err != nil {
		return uuid.Nil, false, err
	}
	if _, err = q.CreateMCPToolGrant(ctx, db.CreateMCPToolGrantParams{
		ID:          pgID(uuid.New()),
		WorkspaceID: turn.WorkspaceID,
		BotID:       turn.BotID,
		ServerID:    pgID(req.ServerID),
		ToolName:    req.ToolName,
		ArgsSha256:  req.ArgsHash,
		ApprovalID:  approvalID,
		Status:      "pending",
	}); err != nil {
		return uuid.Nil, false, err
	}

	var notifyIDs []uuid.UUID
	if conv.Type == "group" {
		if err = q.PauseGroupForApproval(ctx, db.PauseGroupForApprovalParams{
			WorkspaceID: turn.WorkspaceID,
			ID:          turn.ConversationID,
		}); err != nil {
			return uuid.Nil, false, err
		}
		notifyIDs, err = notify.Approval(ctx, q, turn.WorkspaceID, turn.ConversationID, message.ID, approvalID)
		if err != nil {
			return uuid.Nil, false, err
		}
	} else if conv.UserID.Valid {
		nid, nErr := q.InsertNotification(ctx, db.InsertNotificationParams{
			WorkspaceID:    turn.WorkspaceID,
			UserID:         conv.UserID,
			Kind:           "approval",
			ConversationID: turn.ConversationID,
			MessageID:      message.ID,
			ApprovalID:     approvalID,
		})
		if nErr != nil {
			return uuid.Nil, false, nErr
		}
		notifyIDs = []uuid.UUID{uuid.UUID(nid.Bytes)}
	}
	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, false, err
	}
	signal.Send(ctx, req.WorkspaceID, uuid.UUID(turn.ConversationID.Bytes), notifyIDs)
	return uuid.UUID(approvalID.Bytes), true, nil
}

func triggerID(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return uuid.UUID(id.Bytes).String()
}

func pgID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

// normalizeHost keeps a blocked destination that can be stored on a card.
// Spaces and userinfo never belong in an allowlist entry.
func normalizeHost(raw string) (string, bool) {
	host := strings.ToLower(strings.TrimSpace(raw))
	if host == "" || len(host) > 253 || strings.ContainsAny(host, " @/\\") {
		return "", false
	}
	return host, true
}

// containsToken reports a whole word. "main" does not match "remaining".
func containsToken(text, token string) bool {
	return containsPhrase(text, token)
}

// containsPhrase is a case-insensitive match bounded by non-letters.
// A multi-word label such as "deleting data" must appear as that phrase.
func containsPhrase(text, phrase string) bool {
	phrase = strings.TrimSpace(phrase)
	if phrase == "" {
		return false
	}
	text = strings.ToLower(text)
	phrase = strings.ToLower(phrase)
	for i := 0; i+len(phrase) <= len(text); {
		j := strings.Index(text[i:], phrase)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(phrase)
		if wordEdge(text, start, true) && wordEdge(text, end, false) {
			return true
		}
		_, size := utf8.DecodeRuneInString(text[start:])
		if size < 1 {
			size = 1
		}
		i = start + size
	}
	return false
}

func wordEdge(text string, i int, before bool) bool {
	var r rune
	if before {
		if i == 0 {
			return true
		}
		r, _ = utf8.DecodeLastRuneInString(text[:i])
	} else {
		if i >= len(text) {
			return true
		}
		r, _ = utf8.DecodeRuneInString(text[i:])
	}
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}
