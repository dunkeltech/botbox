// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"bytes"
	"errors"
	"net/http"
	"regexp"

	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// toolResultImagePattern matches the file reference Claude Code writes when a
// desktop tool returns an image. The directory and the file name are fixed so
// a tool line cannot be used to read the rest of the bot home.
var toolResultImagePattern = regexp.MustCompile(`\[Image: source: (/home/bot/\.claude/projects/-home-bot/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/tool-results/[A-Za-z0-9._-]{1,128}\.(?:png|jpe?g|webp|gif))\]`)

// toolResultImagePath returns the one sandbox image named by a tool message.
func toolResultImagePath(content string) (string, bool) {
	match := toolResultImagePattern.FindStringSubmatch(content)
	if match == nil {
		return "", false
	}
	return match[1], true
}

// imageContentType accepts only real image payloads. A renamed text file in
// the tool-results directory must not be returned as a screenshot.
func imageContentType(data []byte) (string, bool) {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png", true
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg", true
	case len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp", true
	case len(data) >= 6 && (bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a"))):
		return "image/gif", true
	default:
		return "", false
	}
}

func (s *server) handleChatToolImage(w http.ResponseWriter, r *http.Request) {
	conversation, ok := s.privateConversation(w, r, false)
	if !ok {
		return
	}
	s.serveToolImage(w, r, conversation.WorkspaceID, conversation.ID, func(m db.Message) (pgtype.UUID, bool) {
		return conversation.BotID, true
	})
}

func (s *server) handleGroupToolImage(w http.ResponseWriter, r *http.Request) {
	group, ok := s.groupForMember(w, r, false)
	if !ok {
		return
	}
	s.serveToolImage(w, r, group.WorkspaceID, group.ID, func(m db.Message) (pgtype.UUID, bool) {
		if !m.SenderBotID.Valid {
			return pgtype.UUID{}, false
		}
		return m.SenderBotID, true
	})
}

func (s *server) serveToolImage(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID, conversationID pgtype.UUID,
	botForMessage func(db.Message) (pgtype.UUID, bool),
) {
	messageID, err := uuid.Parse(chi.URLParam(r, "messageID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid message identifier")
		return
	}
	message, err := s.queries.GetTurnMessage(r.Context(), db.GetTurnMessageParams{
		WorkspaceID: workspaceID,
		ID:          pgtype.UUID{Bytes: messageID, Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusNotFound, "chat.not_found", "conversation not found")
			return
		}
		s.chatFailure(w)
		return
	}
	// The message id is not scoped by the query. Another conversation in the
	// same workspace, or a non-tool message, must not yield a file.
	botID, hasBot := botForMessage(message)
	if message.ConversationID != conversationID || message.Kind != "tool" || !hasBot {
		writeError(w, s.logger, http.StatusNotFound, "chat.not_found", "conversation not found")
		return
	}
	filePath, ok := toolResultImagePath(message.Content)
	if !ok {
		writeError(w, s.logger, http.StatusNotFound, "tool_image.not_found", "screenshot is not available")
		return
	}
	env, err := s.queries.GetEnvironmentByWorkspaceAndBot(r.Context(), db.GetEnvironmentByWorkspaceAndBotParams{
		WorkspaceID: workspaceID,
		ID:          botID,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.logger.Error("load bot environment", "bot_id", uuid.UUID(botID.Bytes).String())
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load sandbox environment")
		return
	}
	if err != nil || env.Status != string(compute.StatusRunning) || env.EnvID == "" {
		writeError(w, s.logger, http.StatusConflict, "environment.not_running", "sandbox is not running")
		return
	}
	if s.compute == nil {
		writeError(w, s.logger, http.StatusBadGateway, "sandbox_unavailable", "sandbox is unavailable")
		return
	}
	content, err := s.compute.ReadFile(r.Context(), compute.EnvID(env.EnvID), filePath)
	if err != nil {
		s.writeComputeError(w, err, "failed to read screenshot")
		return
	}
	mime, ok := imageContentType(content)
	if !ok {
		writeError(w, s.logger, http.StatusNotFound, "tool_image.not_found", "screenshot is not available")
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=0")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}
