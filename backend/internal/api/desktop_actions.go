// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode"

	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	"github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (s *server) handleListDesktopActions(w http.ResponseWriter, r *http.Request) {
	wsRow, _, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	botRow, ok := s.loadWorkspaceBot(w, r, wsRow.ID)
	if !ok {
		return
	}
	s.pullDesktopJournal(r.Context(), wsRow.ID, botRow.ID)
	s.expireDesktopScreenshots(r.Context(), wsRow.ID, wsRow.ScreenshotRetentionDays)
	rows, err := s.queries.ListDesktopActions(r.Context(), db.ListDesktopActionsParams{
		WorkspaceID: wsRow.ID,
		BotID:       botRow.ID,
		PageLimit:   100,
	})
	if err != nil {
		s.logger.Error("list desktop actions", "bot_id", fromPGUUID(botRow.ID).String())
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to list desktop actions")
		return
	}
	actions := make([]api.DesktopAction, 0, len(rows))
	for _, row := range rows {
		actions = append(actions, toAPIDesktopAction(row))
	}
	days := int(wsRow.ScreenshotRetentionDays)
	if days < 1 {
		days = 7
	}
	writeJSON(w, s.logger, http.StatusOK, api.DesktopActionListResponse{Actions: actions, ScreenshotRetentionDays: days})
}

func (s *server) handleGetDesktopActionScreenshot(w http.ResponseWriter, r *http.Request) {
	wsRow, _, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	botRow, ok := s.loadWorkspaceBot(w, r, wsRow.ID)
	if !ok {
		return
	}
	actionID, err := uuid.Parse(chi.URLParam(r, "actionID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid desktop action identifier")
		return
	}
	s.expireDesktopScreenshots(r.Context(), wsRow.ID, wsRow.ScreenshotRetentionDays)
	row, err := s.queries.GetDesktopActionScreenshot(r.Context(), db.GetDesktopActionScreenshotParams{
		WorkspaceID: wsRow.ID,
		BotID:       botRow.ID,
		ID:          toPGUUID(actionID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusNotFound, "desktop_action.not_found", "desktop action not found")
			return
		}
		s.logger.Error("load desktop screenshot", "bot_id", fromPGUUID(botRow.ID).String())
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load screenshot")
		return
	}
	if len(row.Screenshot) == 0 {
		writeError(w, s.logger, http.StatusNotFound, "screenshot.expired", "screenshot has expired")
		return
	}
	mime := row.ScreenshotMime
	if mime != "image/png" && mime != "image/jpeg" {
		mime = "image/png"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=0")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(row.Screenshot)
}

func (s *server) pullDesktopJournal(ctx context.Context, wsID, botID pgtype.UUID) {
	if s.queries == nil || s.compute == nil {
		return
	}
	env, err := s.queries.GetEnvironmentByWorkspaceAndBot(ctx, db.GetEnvironmentByWorkspaceAndBotParams{WorkspaceID: wsID, ID: botID})
	if err != nil || env.Status != string(compute.StatusRunning) || env.EnvID == "" {
		return
	}
	bridge, ok := s.compute.(compute.DesktopBridge)
	if !ok {
		return
	}
	records, err := bridge.PullDesktopActions(ctx, compute.EnvID(env.EnvID))
	if err != nil || len(records) == 0 {
		return
	}
	session, through, err := engine.PersistDesktopActions(ctx, s.queries, fromPGUUID(wsID), fromPGUUID(botID), records)
	if err != nil || session == "" {
		s.logger.Error("store desktop actions", "bot_id", fromPGUUID(botID).String())
		return
	}
	_ = bridge.AckDesktopActions(ctx, compute.EnvID(env.EnvID), session, through)
}

func (s *server) expireDesktopScreenshots(ctx context.Context, wsID pgtype.UUID, days int32) {
	if days < 1 {
		days = 7
	}
	if err := s.queries.ExpireDesktopScreenshots(ctx, db.ExpireDesktopScreenshotsParams{
		WorkspaceID:   wsID,
		AsOf:          pgtype.Timestamptz{Time: s.now().UTC(), Valid: true},
		RetentionDays: days,
	}); err != nil {
		s.logger.Error("expire desktop screenshots", "err", err)
	}
}

func (s *server) pushDesktopLock(ctx context.Context, envRow db.Environment, human bool) error {
	if envRow.Status != string(compute.StatusRunning) || envRow.EnvID == "" {
		return nil
	}
	bridge, ok := s.compute.(compute.DesktopBridge)
	if !ok {
		return nil
	}
	return bridge.SetDesktopLock(ctx, compute.EnvID(envRow.EnvID), human)
}

func (s *server) queueHandback(ctx context.Context, q *db.Queries, wsID, botID pgtype.UUID, note string, controlWasHeld bool) (uuid.UUID, error) {
	turn, err := q.LatestBotTurn(ctx, db.LatestBotTurnParams{WorkspaceID: wsID, BotID: botID})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, err
	}
	latest, err := q.LatestConversationMessage(ctx, db.LatestConversationMessageParams{WorkspaceID: wsID, ConversationID: turn.ConversationID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}
	// A fresh takeover lets the user recover from the previous failed hand-back.
	retry := controlWasHeld && turn.Status == "failed" && turn.MessageID == latest.ID
	if err == nil && isHandbackMessage(latest.Content, latest.Metadata) && !retry {
		return uuid.Nil, nil
	}
	content := engine.HandbackNote
	if cleaned := stripControls(note); cleaned != "" {
		content += "\n" + cleaned
	}
	msg, err := q.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ID:             toPGUUID(uuid.New()),
		WorkspaceID:    wsID,
		ConversationID: turn.ConversationID,
		SenderType:     "system",
		Kind:           "text",
		Content:        content,
		Metadata:       []byte(engine.HandbackMetadata),
	})
	if err != nil {
		return uuid.Nil, err
	}
	created, err := q.CreateBotTurn(ctx, db.CreateBotTurnParams{
		WorkspaceID:    wsID,
		BotID:          botID,
		ConversationID: turn.ConversationID,
		MessageID:      msg.ID,
	})
	if err != nil {
		return uuid.Nil, err
	}
	return fromPGUUID(created.ID), nil
}

func isHandbackMessage(content string, metadata []byte) bool {
	if content == engine.HandbackNote || strings.HasPrefix(content, engine.HandbackNote+"\n") {
		return true
	}
	return strings.Contains(string(metadata), `"desktop_handback"`)
}

func stripControls(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
}

func toAPIDesktopAction(row db.ListDesktopActionsRow) api.DesktopAction {
	action := api.DesktopAction{
		Id:            fromPGUUID(row.ID),
		BotId:         fromPGUUID(row.BotID),
		Kind:          row.Kind,
		Result:        row.Result,
		ErrorCode:     row.ErrorCode,
		HasScreenshot: row.HasScreenshot,
		HasCoord:      row.HasCoord,
		CreatedAt:     row.CreatedAt.Time.UTC(),
	}
	seq := row.AgentSeq
	action.Seq = &seq
	session := row.SessionID
	action.SessionId = &session
	if row.TurnID.Valid {
		id := fromPGUUID(row.TurnID)
		action.TurnId = &id
	}
	if row.HasCoord {
		x, y := int(row.CoordX), int(row.CoordY)
		action.CoordX = &x
		action.CoordY = &y
	}
	if row.ScreenshotMime != "" {
		mime := row.ScreenshotMime
		action.ScreenshotMime = &mime
	}
	var params map[string]interface{}
	if json.Unmarshal(row.Params, &params) == nil && len(params) > 0 {
		action.Params = &params
	}
	return action
}
