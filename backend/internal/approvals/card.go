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
	TriggerMessageID string `json:"triggerMessageId,omitempty"`
}

// Open inserts one pending approval and a kind=approval message. A direct
// message, a kind the group did not list, or a duplicate pending payload is
// ignored. An existing pause is left as it is. The returned ids are new
// notification rows for the humans who may decide. The caller publishes them
// after commit.
func Open(ctx context.Context, q *db.Queries, card Card) ([]uuid.UUID, error) {
	group, err := q.GetGroup(ctx, db.GetGroupParams{WorkspaceID: card.WorkspaceID, ID: card.ConversationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !ruleListed(group.AskBefore, card.Kind) {
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
	if err = q.PauseGroupForApproval(ctx, db.PauseGroupForApprovalParams{
		WorkspaceID: card.WorkspaceID,
		ID:          card.ConversationID,
	}); err != nil {
		return nil, err
	}
	return notify.Approval(ctx, q, card.WorkspaceID, card.ConversationID, message.ID, approvalID)
}

// NoteBlockedHost records a new_host card when a group turn is running and
// the group asked before a new host. An allowed host never reaches here.
// signal runs after commit and may be nil.
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
	return note(ctx, pool, turn, signal, func(raw []byte) []Card {
		return pushMatchesFor(raw, text)
	})
}

// NoteReply records a custom card when the stored bot reply contains a listed
// label. It runs before the next bots are queued, so the pause stops the handoff.
func NoteReply(ctx context.Context, pool *pgxpool.Pool, turn db.BotTurn, text string, signal notify.Signal) error {
	return note(ctx, pool, turn, signal, func(raw []byte) []Card {
		return customMatchesText(raw, text)
	})
}

func note(ctx context.Context, pool *pgxpool.Pool, turn db.BotTurn, signal notify.Signal, match func([]byte) []Card) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	group, err := q.GetGroup(ctx, db.GetGroupParams{WorkspaceID: turn.WorkspaceID, ID: turn.ConversationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var ids []uuid.UUID
	for _, card := range match(group.AskBefore) {
		card.WorkspaceID = turn.WorkspaceID
		card.ConversationID = turn.ConversationID
		card.BotID = turn.BotID
		card.TriggerMessageID = turn.MessageID
		more, err := Open(ctx, q, card)
		if err != nil {
			return err
		}
		ids = append(ids, more...)
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	signal.Send(ctx, uuid.UUID(turn.WorkspaceID.Bytes), uuid.UUID(turn.ConversationID.Bytes), ids)
	return nil
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
	default:
		return card.Kind
	}
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
