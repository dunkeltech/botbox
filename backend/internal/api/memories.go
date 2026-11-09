package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/memory"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *server) memoryService() memory.Service {
	return memory.Service{
		Pool: s.pool,
		Ring: s.keyRing,
		Now:  s.now,
	}
}

func (s *server) loadBotForMemory(w http.ResponseWriter, r *http.Request, manage bool) (db.GetWorkspaceByIDForUserRow, uuid.UUID, db.Bot, bool) {
	wsRow, claims, actorRole, ok := s.loadWorkspaceMembership(w, r, "")
	if !ok {
		return db.GetWorkspaceByIDForUserRow{}, uuid.Nil, db.Bot{}, false
	}
	botID, err := uuid.Parse(chi.URLParam(r, "botID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid bot identifier")
		return db.GetWorkspaceByIDForUserRow{}, uuid.Nil, db.Bot{}, false
	}
	botRow, err := s.queries.GetBotByWorkspaceAndID(r.Context(), db.GetBotByWorkspaceAndIDParams{
		WorkspaceID: wsRow.ID,
		ID:          toPGUUID(botID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, s.logger, http.StatusNotFound, "bot.not_found", "bot not found")
		return db.GetWorkspaceByIDForUserRow{}, uuid.Nil, db.Bot{}, false
	}
	if err != nil {
		s.logger.Error("load bot for memory", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load bot")
		return db.GetWorkspaceByIDForUserRow{}, uuid.Nil, db.Bot{}, false
	}
	if manage {
		isBotOwner := botRow.OwnerUserID.Valid && fromPGUUID(botRow.OwnerUserID) == claims.Subject
		if !workspaces.Can(actorRole, workspaces.ActionManageBot, isBotOwner) {
			writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "insufficient permissions to manage this bot's knowledge base")
			return db.GetWorkspaceByIDForUserRow{}, uuid.Nil, db.Bot{}, false
		}
	}
	return wsRow, claims.Subject, botRow, true
}

func (s *server) writeMemoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, memory.ErrNotFound):
		writeError(w, s.logger, http.StatusNotFound, "memory.not_found", "memory entry not found")
	case errors.Is(err, memory.ErrSecretDetected):
		writeError(w, s.logger, http.StatusBadRequest, "memory.secret_rejected", memory.ErrSecretDetected.Error())
	case errors.Is(err, memory.ErrValidation):
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
	case errors.Is(err, memory.ErrAlwaysBudgetExceeded):
		writeError(w, s.logger, http.StatusConflict, "memory.always_budget_exceeded", memory.ErrAlwaysBudgetExceeded.Error())
	case errors.Is(err, memory.ErrLimitReached):
		writeError(w, s.logger, http.StatusConflict, "memory.limit_reached", memory.ErrLimitReached.Error())
	default:
		s.logger.Error("bot memory operation failed", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "memory operation failed")
	}
}

func toAPIBotMemory(row db.BotMemory) apigen.BotMemory {
	tags := row.Tags
	if tags == nil {
		tags = []string{}
	}
	out := apigen.BotMemory{
		Id:            fromPGUUID(row.ID),
		WorkspaceId:   fromPGUUID(row.WorkspaceID),
		BotId:         fromPGUUID(row.BotID),
		Title:         row.Title,
		Content:       row.Content,
		Mode:          apigen.BotMemoryMode(row.Mode),
		Tags:          tags,
		SourceKind:    apigen.BotMemorySourceKind(row.SourceKind),
		SourceLabel:   row.SourceLabel,
		TokenEstimate: int(row.TokenEstimate),
		CreatedAt:     row.CreatedAt.Time.UTC(),
		UpdatedAt:     row.UpdatedAt.Time.UTC(),
	}
	if row.SourceConversationID.Valid {
		convID := fromPGUUID(row.SourceConversationID)
		out.SourceConversationId = &convID
	}
	if row.SourceTurnID.Valid {
		turnID := fromPGUUID(row.SourceTurnID)
		out.SourceTurnId = &turnID
	}
	return out
}

func toAPIMemorySummary(sum memory.Summary) apigen.BotMemorySummary {
	return apigen.BotMemorySummary{
		AutoSave:         sum.AutoSave,
		TotalCount:       sum.TotalCount,
		AlwaysCount:      sum.AlwaysCount,
		OnDemandCount:    sum.OnDemandCount,
		AlwaysTokens:     sum.AlwaysTokens,
		AlwaysTokenLimit: sum.AlwaysTokenLimit,
		MaxEntries:       sum.MaxEntries,
	}
}

func (s *server) handleListBotMemories(w http.ResponseWriter, r *http.Request) {
	wsRow, _, botRow, ok := s.loadBotForMemory(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	query := strings.TrimSpace(q.Get("q"))
	if len(query) > 200 {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "search query must be at most 200 characters")
		return
	}
	modeFilter := strings.TrimSpace(q.Get("mode"))
	if modeFilter != "" && modeFilter != memory.ModeAlways && modeFilter != memory.ModeOnDemand {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid mode filter")
		return
	}
	sourceFilter := strings.TrimSpace(q.Get("source"))
	if sourceFilter != "" && sourceFilter != memory.SourceChat && sourceFilter != memory.SourceManual {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid source filter")
		return
	}
	tagFilter := ""
	if rawTag := strings.TrimSpace(q.Get("tag")); rawTag != "" {
		normalized, err := memory.NormalizeTags([]string{rawTag})
		if err != nil || len(normalized) != 1 {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid tag filter")
			return
		}
		tagFilter = normalized[0]
	}
	limit := int32(20)
	if rawLimit := strings.TrimSpace(q.Get("limit")); rawLimit != "" {
		parsed, err := strconv.ParseInt(rawLimit, 10, 32)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "limit must be between 1 and 100")
			return
		}
		limit = int32(parsed)
	}
	offset := int32(0)
	if rawOffset := strings.TrimSpace(q.Get("offset")); rawOffset != "" {
		parsed, err := strconv.ParseInt(rawOffset, 10, 32)
		if err != nil || parsed < 0 || parsed > 100000 {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "offset must be >= 0")
			return
		}
		offset = int32(parsed)
	}

	res, err := s.memoryService().List(r.Context(), fromPGUUID(wsRow.ID), fromPGUUID(botRow.ID), memory.ListFilter{
		Query:      query,
		Mode:       modeFilter,
		SourceKind: sourceFilter,
		Tag:        tagFilter,
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		s.writeMemoryError(w, err)
		return
	}

	entries := make([]apigen.BotMemory, 0, len(res.Entries))
	for _, entry := range res.Entries {
		entries = append(entries, toAPIBotMemory(entry))
	}
	writeJSON(w, s.logger, http.StatusOK, apigen.BotMemoryListResponse{
		Summary:       toAPIMemorySummary(res.Summary),
		Entries:       entries,
		FilteredCount: res.FilteredCount,
		Tags:          res.Tags,
	})
}

func (s *server) handleCreateBotMemory(w http.ResponseWriter, r *http.Request) {
	wsRow, userID, botRow, ok := s.loadBotForMemory(w, r, true)
	if !ok {
		return
	}
	var req apigen.CreateBotMemoryRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}
	var tags []string
	if req.Tags != nil {
		tags = *req.Tags
	}
	actorName := ""
	if userRow, err := s.queries.GetUserByID(r.Context(), toPGUUID(userID)); err == nil {
		actorName = userRow.DisplayName
	}
	created, err := s.memoryService().CreateManual(
		r.Context(),
		fromPGUUID(wsRow.ID),
		fromPGUUID(botRow.ID),
		userID,
		actorName,
		memory.WriteInput{
			Title:   req.Title,
			Content: req.Content,
			Mode:    string(req.Mode),
			Tags:    tags,
		},
	)
	if err != nil {
		s.writeMemoryError(w, err)
		return
	}
	writeJSON(w, s.logger, http.StatusCreated, toAPIBotMemory(created))
}

func (s *server) handleUpdateBotMemory(w http.ResponseWriter, r *http.Request) {
	wsRow, userID, botRow, ok := s.loadBotForMemory(w, r, true)
	if !ok {
		return
	}
	memoryID, err := uuid.Parse(chi.URLParam(r, "memoryID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid memory identifier")
		return
	}
	var req apigen.UpdateBotMemoryRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}
	var modePtr *string
	if req.Mode != nil {
		m := string(*req.Mode)
		modePtr = &m
	}
	updated, err := s.memoryService().Update(
		r.Context(),
		fromPGUUID(wsRow.ID),
		fromPGUUID(botRow.ID),
		memoryID,
		userID,
		false,
		memory.UpdateInput{
			Title:   req.Title,
			Content: req.Content,
			Mode:    modePtr,
			Tags:    req.Tags,
		},
	)
	if err != nil {
		s.writeMemoryError(w, err)
		return
	}
	writeJSON(w, s.logger, http.StatusOK, toAPIBotMemory(updated))
}

func (s *server) handleDeleteBotMemory(w http.ResponseWriter, r *http.Request) {
	wsRow, userID, botRow, ok := s.loadBotForMemory(w, r, true)
	if !ok {
		return
	}
	memoryID, err := uuid.Parse(chi.URLParam(r, "memoryID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid memory identifier")
		return
	}
	if err := s.memoryService().Delete(r.Context(), fromPGUUID(wsRow.ID), fromPGUUID(botRow.ID), memoryID, userID, false); err != nil {
		s.writeMemoryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleClearBotMemories(w http.ResponseWriter, r *http.Request) {
	wsRow, userID, botRow, ok := s.loadBotForMemory(w, r, true)
	if !ok {
		return
	}
	if _, err := s.memoryService().Clear(r.Context(), fromPGUUID(wsRow.ID), fromPGUUID(botRow.ID), userID); err != nil {
		s.writeMemoryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleUpdateBotMemoryPolicy(w http.ResponseWriter, r *http.Request) {
	wsRow, userID, botRow, ok := s.loadBotForMemory(w, r, true)
	if !ok {
		return
	}
	var req apigen.UpdateBotMemoryPolicyRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}
	sum, err := s.memoryService().UpdatePolicy(r.Context(), fromPGUUID(wsRow.ID), fromPGUUID(botRow.ID), userID, req.AutoSave)
	if err != nil {
		s.writeMemoryError(w, err)
		return
	}
	writeJSON(w, s.logger, http.StatusOK, toAPIMemorySummary(sum))
}
