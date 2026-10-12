package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
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
	return apigen.ChatMessage{Id: fromPGUUID(m.ID), Position: m.Position, SenderType: apigen.ChatMessageSenderType(m.SenderType), Kind: apigen.ChatMessageKind(m.Kind), Content: m.Content, CreatedAt: m.CreatedAt.Time}
}

func (s *server) handleChatHistory(w http.ResponseWriter, r *http.Request) {
	c, ok := s.privateConversation(w, r, false)
	if !ok {
		return
	}
	before := int64(math.MaxInt64)
	if raw := r.URL.Query().Get("before"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1 {
			writeError(w, s.logger, 400, "validation_error", "invalid history cursor")
			return
		}
		before = n
	}
	rows, err := s.queries.ListChatMessages(r.Context(), db.ListChatMessagesParams{WorkspaceID: c.WorkspaceID, ConversationID: c.ID, BeforePosition: before, PageLimit: 50})
	if err != nil {
		s.chatFailure(w)
		return
	}
	turns, err := s.queries.ListConversationTurns(r.Context(), db.ListConversationTurnsParams{WorkspaceID: c.WorkspaceID, ConversationID: c.ID})
	if err != nil {
		s.chatFailure(w)
		return
	}
	out := apigen.ChatHistory{Messages: make([]apigen.ChatMessage, 0, len(rows)), Turns: make([]apigen.ChatTurn, 0, len(turns))}
	for i := len(rows) - 1; i >= 0; i-- {
		out.Messages = append(out.Messages, chatMessage(rows[i]))
	}
	for _, t := range turns {
		out.Turns = append(out.Turns, apigen.ChatTurn{Id: fromPGUUID(t.ID), Status: apigen.ChatTurnStatus(t.Status), QueueAhead: t.QueueAhead, ErrorCode: t.ErrorCode})
	}
	writeJSON(w, s.logger, 200, out)
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
