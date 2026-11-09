package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/memory"
	"github.com/dunkeltech/botbox/backend/internal/secretrequests"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// TurnQueue durably schedules a bot after its input has been stored in a transaction.
// Workers must retry notifications that arrive before the transaction commits.
type TurnQueue interface {
	Enqueue(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error
}

func (s *server) chatFailure(w http.ResponseWriter) {
	writeError(w, s.logger, http.StatusServiceUnavailable, "chat.unavailable", "chat service unavailable")
}

func (s *server) handleOpenDM(w http.ResponseWriter, r *http.Request) {
	ws, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionCreateBot)
	if !ok {
		return
	}
	botID, err := uuid.Parse(chi.URLParam(r, "botID"))
	if err != nil {
		writeError(w, s.logger, 400, "validation_error", "invalid bot identifier")
		return
	}
	_, err = s.queries.GetBotByWorkspaceAndID(r.Context(), db.GetBotByWorkspaceAndIDParams{WorkspaceID: ws.ID, ID: toPGUUID(botID)})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, s.logger, 404, "bot.not_found", "bot not found")
		return
	}
	if err != nil {
		s.chatFailure(w)
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.chatFailure(w)
		return
	}
	defer func(ctx context.Context) { _ = tx.Rollback(ctx) }(r.Context())
	q := s.queries.WithTx(tx)
	c, err := q.OpenDirectMessage(r.Context(), db.OpenDirectMessageParams{WorkspaceID: ws.ID, UserID: toPGUUID(claims.Subject), BotID: toPGUUID(botID)})
	if err != nil {
		s.chatFailure(w)
		return
	}
	for _, m := range []struct {
		kind string
		id   pgtype.UUID
	}{{"user", toPGUUID(claims.Subject)}, {"bot", toPGUUID(botID)}} {
		if err = q.AddDirectMessageMember(r.Context(), db.AddDirectMessageMemberParams{WorkspaceID: ws.ID, ConversationID: c.ID, MemberType: m.kind, MemberID: m.id}); err != nil {
			s.chatFailure(w)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.chatFailure(w)
		return
	}
	writeJSON(w, s.logger, 200, apigen.ChatConversation{Id: fromPGUUID(c.ID), BotId: botID})
}

func (s *server) handleListDMs(w http.ResponseWriter, r *http.Request) {
	ws, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	rows, err := s.queries.ListDirectMessages(r.Context(), db.ListDirectMessagesParams{WorkspaceID: ws.ID, UserID: toPGUUID(claims.Subject)})
	if err != nil {
		s.chatFailure(w)
		return
	}
	out := make([]apigen.ChatConversation, 0, len(rows))
	for _, c := range rows {
		out = append(out, apigen.ChatConversation{Id: fromPGUUID(c.ID), BotId: fromPGUUID(c.BotID)})
	}
	writeJSON(w, s.logger, 200, out)
}

func (s *server) privateConversation(w http.ResponseWriter, r *http.Request, send bool) (db.Conversation, bool) {
	action := workspaces.ActionViewWorkspace
	if send {
		action = workspaces.ActionCreateBot
	}
	ws, claims, _, ok := s.loadWorkspaceMembership(w, r, action)
	if !ok {
		return db.Conversation{}, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "conversationID"))
	if err != nil {
		writeError(w, s.logger, 400, "validation_error", "invalid conversation identifier")
		return db.Conversation{}, false
	}
	c, err := s.queries.GetPrivateConversation(r.Context(), db.GetPrivateConversationParams{WorkspaceID: ws.ID, ID: toPGUUID(id), UserID: toPGUUID(claims.Subject)})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, s.logger, 404, "chat.not_found", "conversation not found")
		return c, false
	}
	if err != nil {
		s.chatFailure(w)
		return c, false
	}
	return c, true
}

func chatMessage(m db.Message) apigen.ChatMessage {
	// A group has several humans and bots. System rows have neither id.
	out := apigen.ChatMessage{Id: fromPGUUID(m.ID), Position: m.Position, SenderType: apigen.ChatMessageSenderType(m.SenderType), Kind: apigen.ChatMessageKind(m.Kind), Content: m.Content, CreatedAt: m.CreatedAt.Time}
	if m.SenderUserID.Valid {
		id := fromPGUUID(m.SenderUserID)
		out.SenderUserId = &id
	}
	if m.SenderBotID.Valid {
		id := fromPGUUID(m.SenderBotID)
		out.SenderBotId = &id
	}
	if m.Kind == "secret_request" && len(m.Metadata) > 0 {
		var card secretrequests.CardPayload
		if json.Unmarshal(m.Metadata, &card) == nil && card.SecretRequestID != "" {
			summary := presentSecretCard(card)
			out.SecretRequest = &summary
		}
	}
	if m.Kind == "tool" && len(m.Metadata) > 0 {
		out.Tool = parseToolCallMeta(m.Metadata)
	}
	if m.Kind == "memory" && len(m.Metadata) > 0 {
		var card memory.ChatMemoryMetadata
		if json.Unmarshal(m.Metadata, &card) == nil && card.MemoryID != uuid.Nil && apigen.BotMemoryMode(card.Mode).Valid() {
			tags := card.Tags
			if tags == nil {
				tags = []string{}
			}
			out.Memory = &apigen.ChatMessageMemory{
				MemoryId:      card.MemoryID,
				Title:         card.Title,
				Content:       card.Content,
				Mode:          apigen.BotMemoryMode(card.Mode),
				Tags:          tags,
				TokenEstimate: int(card.TokenEstimate),
				SourceLabel:   card.SourceLabel,
				Deleted:       card.Deleted,
			}
		}
	}
	if m.Kind == "progress" || m.Kind == "team_status" {
		var update struct {
			Blocker bool                  `json:"blocker"`
			Status  *apigen.TeamBotStatus `json:"status"`
		}
		if json.Unmarshal(m.Metadata, &update) == nil {
			if m.Kind == "progress" && update.Blocker {
				out.ProgressBlocker = &update.Blocker
			}
			if m.Kind == "team_status" && update.Status != nil && update.Status.BotId != uuid.Nil && update.Status.State.Valid() {
				out.TeamStatus = update.Status
			}
		}
	}
	return out
}

// parseToolCallMeta extracts only pairing and status fields from persisted
// engine metadata so raw tool arguments and sandbox paths never reach API clients.
func parseToolCallMeta(raw []byte) *apigen.ToolCallMeta {
	var meta struct {
		Kind      string `json:"Kind"`
		TurnID    string `json:"TurnID"`
		ToolID    string `json:"ToolID"`
		IsError   bool   `json:"IsError"`
		ErrorCode string `json:"ErrorCode"`
	}
	if json.Unmarshal(raw, &meta) != nil {
		return nil
	}
	var phase apigen.ToolCallPhase
	switch meta.Kind {
	case "tool_call":
		phase = apigen.Call
	case "tool_result":
		phase = apigen.Result
	default:
		return nil
	}
	out := &apigen.ToolCallMeta{
		Phase:   phase,
		IsError: meta.IsError,
	}
	if meta.TurnID != "" {
		out.TurnId = &meta.TurnID
	}
	if meta.ToolID != "" {
		out.ToolCallId = &meta.ToolID
	}
	if meta.ErrorCode != "" {
		out.ErrorCode = &meta.ErrorCode
	}
	return out
}

func (s *server) handleChatHistory(w http.ResponseWriter, r *http.Request) {
	c, ok := s.privateConversation(w, r, false)
	if !ok {
		return
	}
	s.writeConversationHistory(w, r, c.WorkspaceID, c.ID, false)
}

func (s *server) writeConversationHistory(w http.ResponseWriter, r *http.Request, workspaceID, conversationID pgtype.UUID, group bool) {
	before := int64(math.MaxInt64)
	if raw := r.URL.Query().Get("before"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1 {
			writeError(w, s.logger, 400, "validation_error", "invalid history cursor")
			return
		}
		before = n
	}
	rows, err := s.queries.ListChatMessages(r.Context(), db.ListChatMessagesParams{WorkspaceID: workspaceID, ConversationID: conversationID, BeforePosition: before, PageLimit: 50})
	if err != nil {
		s.chatFailure(w)
		return
	}
	turns, err := s.queries.ListConversationTurns(r.Context(), db.ListConversationTurnsParams{WorkspaceID: workspaceID, ConversationID: conversationID})
	if err != nil {
		s.chatFailure(w)
		return
	}
	out := apigen.ChatHistory{Messages: make([]apigen.ChatMessage, 0, len(rows)), Turns: make([]apigen.ChatTurn, 0, len(turns))}
	var progress map[pgtype.UUID]*apigen.GroupInputRequest
	if group {
		progress, err = s.groupInputProgress(r.Context(), workspaceID, conversationID, rows)
		if err != nil {
			s.chatFailure(w)
			return
		}
	}
	for i := len(rows) - 1; i >= 0; i-- {
		message := chatMessage(rows[i])
		message.InputRequest = progress[rows[i].ID]
		out.Messages = append(out.Messages, message)
	}
	for _, t := range turns {
		turn := apigen.ChatTurn{Id: fromPGUUID(t.ID), BotId: botIDPtr(t.BotID), Status: apigen.ChatTurnStatus(t.Status), QueueAhead: t.QueueAhead, ErrorCode: t.ErrorCode}
		if t.Status == "running" && t.StopRequested {
			turn.StopRequested = &t.StopRequested
		}
		out.Turns = append(out.Turns, turn)
	}
	writeJSON(w, s.logger, 200, out)
}

func (s *server) handleStopChatTurn(w http.ResponseWriter, r *http.Request) {
	c, ok := s.privateConversation(w, r, true)
	if !ok {
		return
	}
	s.stopConversationTurn(w, r, c.WorkspaceID, c.ID)
}

func (s *server) stopConversationTurn(w http.ResponseWriter, r *http.Request, workspaceID, conversationID pgtype.UUID) {
	turnID, err := uuid.Parse(chi.URLParam(r, "turnID"))
	if err != nil || turnID == uuid.Nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid turn identifier")
		return
	}
	ctx := r.Context()
	turn, err := s.queries.GetBotTurn(ctx, db.GetBotTurnParams{WorkspaceID: workspaceID, ID: toPGUUID(turnID)})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && turn.ConversationID != conversationID) {
		writeError(w, s.logger, http.StatusNotFound, "chat.not_found", "turn not found")
		return
	}
	if err != nil {
		s.chatFailure(w)
		return
	}
	if turn.Status == "running" {
		if err = s.queries.RequestBotTurnStop(ctx, db.RequestBotTurnStopParams{WorkspaceID: workspaceID, ConversationID: conversationID, ID: turn.ID}); err != nil {
			s.chatFailure(w)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleSendChat(w http.ResponseWriter, r *http.Request) {
	c, ok := s.privateConversation(w, r, true)
	if !ok {
		return
	}
	var req apigen.SendChatMessageRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}
	if req.Id == uuid.Nil || strings.TrimSpace(req.Content) == "" || len(req.Content) > 8192 {
		writeError(w, s.logger, 400, "validation_error", "message id and 1..8192 bytes of content are required")
		return
	}
	previous, err := s.queries.GetTurnMessage(r.Context(), db.GetTurnMessageParams{WorkspaceID: c.WorkspaceID, ID: toPGUUID(req.Id)})
	if err == nil {
		if previous.ConversationID != c.ID || previous.SenderUserID != c.UserID || previous.Content != req.Content {
			writeError(w, s.logger, 409, "chat.id_conflict", "message id already used")
			return
		}
		writeJSON(w, s.logger, 202, chatMessage(previous))
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) || s.turnQueue == nil {
		s.chatFailure(w)
		return
	}
	if !s.chatRL.Allow(fromPGUUID(c.WorkspaceID).String() + "/" + fromPGUUID(c.UserID).String()) {
		writeError(w, s.logger, http.StatusTooManyRequests, "chat.rate_limited", "message rate limit exceeded")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.chatFailure(w)
		return
	}
	defer func(ctx context.Context) { _ = tx.Rollback(ctx) }(r.Context())
	q := s.queries.WithTx(tx)
	if err = q.LockBotQueue(r.Context(), fromPGUUID(c.WorkspaceID).String()+"/"+fromPGUUID(c.BotID).String()); err != nil {
		s.chatFailure(w)
		return
	}
	pending, err := q.CountPendingBotTurns(r.Context(), db.CountPendingBotTurnsParams{WorkspaceID: c.WorkspaceID, BotID: c.BotID})
	if err != nil {
		s.chatFailure(w)
		return
	}
	if pending >= 100 {
		writeError(w, s.logger, http.StatusTooManyRequests, "chat.queue_full", "bot queue is full")
		return
	}

	msg, err := q.CreateChatMessage(r.Context(), db.CreateChatMessageParams{ID: toPGUUID(req.Id), WorkspaceID: c.WorkspaceID, ConversationID: c.ID, SenderType: "user", SenderUserID: c.UserID, Kind: "text", Content: req.Content, Metadata: []byte("{}")})
	if err != nil {
		s.chatFailure(w)
		return
	}
	turn, err := q.CreateBotTurn(r.Context(), db.CreateBotTurnParams{WorkspaceID: c.WorkspaceID, BotID: c.BotID, ConversationID: c.ID, MessageID: msg.ID})
	if err != nil {
		s.chatFailure(w)
		return
	}
	// Publish before commit: a failed publish rolls back input rather than losing work.
	if err = s.turnQueue.Enqueue(r.Context(), fromPGUUID(c.WorkspaceID), fromPGUUID(c.BotID), fromPGUUID(turn.ID)); err != nil {
		s.chatFailure(w)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.chatFailure(w)
		return
	}
	writeJSON(w, s.logger, 202, chatMessage(msg))
}

func botIDPtr(id pgtype.UUID) *uuid.UUID {
	value := fromPGUUID(id)
	return &value
}
