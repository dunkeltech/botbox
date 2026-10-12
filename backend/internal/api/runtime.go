package api

import (
	"context"
	"net/http"

	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
	"github.com/google/uuid"
)

func (s *server) handleBotRuntime(w http.ResponseWriter, r *http.Request) {
	ws, claims, role, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	bot, ok := s.loadWorkspaceBot(w, r, ws.ID)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		state, err := s.queries.GetBotRuntime(r.Context(), db.GetBotRuntimeParams{WorkspaceID: ws.ID, ID: bot.ID})
		if err != nil {
			s.chatFailure(w)
			return
		}
		writeJSON(w, s.logger, 200, apigen.BotRuntime{Paused: state.Paused})
		return
	}
	if !s.requireManageBot(w, role, claims.Subject, bot, "pause or resume bots") {
		return
	}
	var req apigen.SetBotRuntimeJSONRequestBody
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}
	if req.Paused == nil {
		writeError(w, s.logger, 400, "validation_error", "paused is required")
		return
	}
	queue, ok := s.turnQueue.(interface {
		Interrupt(context.Context, uuid.UUID, uuid.UUID) error
	})
	if !ok {
		s.chatFailure(w)
		return
	}
	// Check transport before changing state. A committed pause still gates claims
	// if cancellation publication subsequently fails and the client retries.
	if health, ok := s.turnQueue.(interface{ Healthy() error }); ok && health.Healthy() != nil {
		s.chatFailure(w)
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.chatFailure(w)
		return
	}
	defer rollbackTx(r.Context(), tx)
	q := s.queries.WithTx(tx)
	wsID, botID := fromPGUUID(ws.ID), fromPGUUID(bot.ID)
	if err = q.LockBotQueue(r.Context(), wsID.String()+"/"+botID.String()); err != nil {
		s.chatFailure(w)
		return
	}
	if err = q.SetBotPaused(r.Context(), db.SetBotPausedParams{WorkspaceID: ws.ID, BotID: bot.ID, Paused: *req.Paused}); err != nil {
		s.chatFailure(w)
		return
	}
	if err = recordAudit(r.Context(), q, &wsID, &claims.Subject, "bot.pause_changed", "bot", &botID, map[string]any{"paused": *req.Paused}); err != nil {
		s.chatFailure(w)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.chatFailure(w)
		return
	}
	if *req.Paused {
		if err = queue.Interrupt(r.Context(), wsID, botID); err != nil {
			s.chatFailure(w)
			return
		}
	}
	writeJSON(w, s.logger, 200, apigen.BotRuntime{Paused: *req.Paused})
}
