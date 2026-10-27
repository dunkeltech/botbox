package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/dunkeltech/botbox/backend/internal/bots"
	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *server) handleListBots(w http.ResponseWriter, r *http.Request) {
	wsRow, _, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}

	rows, err := s.queries.ListBotsByWorkspace(r.Context(), wsRow.ID)
	if err != nil {
		s.logger.Error("list bots", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to list bots")
		return
	}

	envRows, envErr := s.queries.ListEnvironmentsByWorkspace(r.Context(), wsRow.ID)
	envByBot := make(map[uuid.UUID]db.Environment, len(envRows))
	if envErr == nil {
		for _, env := range envRows {
			envByBot[fromPGUUID(env.BotID)] = env
		}
	}

	activities, activityErr := s.queries.ListRunningBotActivity(r.Context(), wsRow.ID)
	if activityErr != nil {
		s.chatFailure(w)
		return
	}
	activityByBot := make(map[uuid.UUID]string, len(activities))
	for _, a := range activities {
		activityByBot[fromPGUUID(a.BotID)] = a.Activity
	}
	out := make([]api.Bot, 0, len(rows))
	for _, b := range rows {
		var envPtr *db.Environment
		if env, exists := envByBot[fromPGUUID(b.ID)]; exists {
			envCopy := env
			envPtr = &envCopy
		}
		converted := toAPIBotWithEnv(b, envPtr)
		if activity := activityByBot[fromPGUUID(b.ID)]; activity != "" {
			converted.Status = api.BotStatus(activity)
			converted.Mood = api.AvatarMood(activity)
		}
		out = append(out, converted)
	}
	writeJSON(w, s.logger, http.StatusOK, api.BotListResponse{Bots: out})
}

func (s *server) handleCreateBot(w http.ResponseWriter, r *http.Request) {
	wsRow, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionCreateBot)
	if !ok {
		return
	}

	var req api.CreateBotRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}

	name, nameOK := validateBotName(req.Name)
	if !nameOK {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "name must be 1..100 characters")
		return
	}
	desc := ""
	if req.Description != nil {
		if len(*req.Description) > 4000 {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "description must be at most 4000 characters")
			return
		}
		desc = strings.TrimSpace(*req.Description)
	}
	if err := bots.ValidateAvatarSeed(req.AvatarSeed); err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	if !req.LlmMode.Valid() {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid llmMode")
		return
	}
	provider, provOK := validateNonEmptyMax(req.LlmProvider, 50)
	if !provOK {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "llmProvider must be 1..50 characters")
		return
	}
	model, modelOK := validateNonEmptyMax(req.LlmModel, 100)
	if !modelOK {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "llmModel must be 1..100 characters")
		return
	}

	driver := api.DriverType(wsRow.DefaultDriver)
	if req.DriverType != nil {
		if !req.DriverType.Valid() {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid driverType")
			return
		}
		driver = *req.DriverType
	}
	replyLang, langOK := validateLocale(req.ReplyLanguage, wsRow.DefaultLocale)
	if !langOK {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "replyLanguage must be at most 10 characters")
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin create bot tx", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to create bot")
		return
	}
	defer rollbackTx(r.Context(), tx)

	txq := s.queries.WithTx(tx)
	botRow, err := txq.CreateBot(r.Context(), db.CreateBotParams{
		WorkspaceID:   wsRow.ID,
		OwnerUserID:   toPGUUID(claims.Subject),
		Name:          name,
		Description:   desc,
		AvatarSeed:    req.AvatarSeed,
		LlmMode:       string(req.LlmMode),
		LlmProvider:   provider,
		LlmModel:      model,
		DriverType:    string(driver),
		ReplyLanguage: replyLang,
	})
	if err != nil {
		s.logger.Error("create bot", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to create bot")
		return
	}

	wsUUID := fromPGUUID(wsRow.ID)
	botUUID := fromPGUUID(botRow.ID)
	actorUUID := claims.Subject
	if err := recordAudit(r.Context(), txq, &wsUUID, &actorUUID, "bot.created", "bot", &botUUID, map[string]any{
		"name": name,
	}); err != nil {
		s.logger.Error("audit bot creation", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to record audit log")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit create bot", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to create bot")
		return
	}

	writeJSON(w, s.logger, http.StatusCreated, toAPIBot(botRow))
}

func (s *server) handleGetBot(w http.ResponseWriter, r *http.Request) {
	wsRow, _, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}

	botID, err := uuid.Parse(chi.URLParam(r, "botID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid bot identifier")
		return
	}

	botRow, err := s.queries.GetBotByWorkspaceAndID(r.Context(), db.GetBotByWorkspaceAndIDParams{
		WorkspaceID: wsRow.ID,
		ID:          toPGUUID(botID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusNotFound, "bot.not_found", "bot not found")
			return
		}
		s.logger.Error("get bot", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load bot")
		return
	}

	var envPtr *db.Environment
	if envRow, envErr := s.queries.GetEnvironmentByWorkspaceAndBot(r.Context(), db.GetEnvironmentByWorkspaceAndBotParams{
		WorkspaceID: wsRow.ID,
		ID:          botRow.ID,
	}); envErr == nil {
		envCopy := db.Environment{
			BotID:               envRow.BotID,
			Driver:              envRow.Driver,
			EnvID:               envRow.EnvID,
			Status:              envRow.Status,
			Image:               envRow.Image,
			CpuMillis:           envRow.CpuMillis,
			MemoryMib:           envRow.MemoryMib,
			VolumeRef:           envRow.VolumeRef,
			ControlHolderUserID: envRow.ControlHolderUserID,
			ControlNote:         envRow.ControlNote,
			StartedAt:           envRow.StartedAt,
			UpdatedAt:           envRow.UpdatedAt,
		}
		envPtr = &envCopy
	}

	converted := toAPIBotWithEnv(botRow, envPtr)
	activities, activityErr := s.queries.ListRunningBotActivity(r.Context(), wsRow.ID)
	if activityErr != nil {
		s.chatFailure(w)
		return
	}
	for _, a := range activities {
		if a.BotID == botRow.ID {
			converted.Status = api.BotStatus(a.Activity)
			converted.Mood = api.AvatarMood(a.Activity)
		}
	}
	writeJSON(w, s.logger, http.StatusOK, converted)
}

func (s *server) handleUpdateBot(w http.ResponseWriter, r *http.Request) {
	wsRow, claims, actorRole, ok := s.loadWorkspaceMembership(w, r, "")
	if !ok {
		return
	}

	botID, err := uuid.Parse(chi.URLParam(r, "botID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid bot identifier")
		return
	}

	var req api.UpdateBotRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin update bot tx", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update bot")
		return
	}
	defer rollbackTx(r.Context(), tx)

	txq := s.queries.WithTx(tx)
	existing, err := txq.GetBotByWorkspaceAndID(r.Context(), db.GetBotByWorkspaceAndIDParams{
		WorkspaceID: wsRow.ID,
		ID:          toPGUUID(botID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusNotFound, "bot.not_found", "bot not found")
			return
		}
		s.logger.Error("load bot for update", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update bot")
		return
	}

	isBotOwner := existing.OwnerUserID.Valid && fromPGUUID(existing.OwnerUserID) == claims.Subject
	if !workspaces.Can(actorRole, workspaces.ActionManageBot, isBotOwner) {
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "insufficient permissions to update this bot")
		return
	}

	params := db.UpdateBotByWorkspaceAndIDParams{
		WorkspaceID:   wsRow.ID,
		ID:            existing.ID,
		Name:          existing.Name,
		Description:   existing.Description,
		AvatarSeed:    existing.AvatarSeed,
		LlmMode:       existing.LlmMode,
		LlmProvider:   existing.LlmProvider,
		LlmModel:      existing.LlmModel,
		DriverType:    existing.DriverType,
		ReplyLanguage: existing.ReplyLanguage,
	}

	if req.Name != nil {
		name, nameOK := validateBotName(*req.Name)
		if !nameOK {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "name must be 1..100 characters")
			return
		}
		params.Name = name
	}
	if req.Description != nil {
		if len(*req.Description) > 4000 {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "description must be at most 4000 characters")
			return
		}
		params.Description = strings.TrimSpace(*req.Description)
	}
	if req.AvatarSeed != nil {
		if err := bots.ValidateAvatarSeed(*req.AvatarSeed); err != nil {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
		params.AvatarSeed = *req.AvatarSeed
	}
	if req.LlmMode != nil {
		if !req.LlmMode.Valid() {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid llmMode")
			return
		}
		params.LlmMode = string(*req.LlmMode)
	}
	if req.LlmProvider != nil {
		prov, provOK := validateNonEmptyMax(*req.LlmProvider, 50)
		if !provOK {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "llmProvider must be 1..50 characters")
			return
		}
		params.LlmProvider = prov
	}
	if req.LlmModel != nil {
		model, modelOK := validateNonEmptyMax(*req.LlmModel, 100)
		if !modelOK {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "llmModel must be 1..100 characters")
			return
		}
		params.LlmModel = model
	}
	if req.DriverType != nil {
		if !req.DriverType.Valid() {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid driverType")
			return
		}
		params.DriverType = string(*req.DriverType)
	}
	if req.ReplyLanguage != nil {
		lang, langOK := validateLocale(req.ReplyLanguage, existing.ReplyLanguage)
		if !langOK || strings.TrimSpace(*req.ReplyLanguage) == "" {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "replyLanguage must be 1..10 characters")
			return
		}
		params.ReplyLanguage = lang
	}

	updated, err := txq.UpdateBotByWorkspaceAndID(r.Context(), params)
	if err != nil {
		s.logger.Error("update bot", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update bot")
		return
	}

	var envPtr *db.Environment
	if envRow, envErr := txq.GetEnvironmentByWorkspaceAndBot(r.Context(), db.GetEnvironmentByWorkspaceAndBotParams{
		WorkspaceID: wsRow.ID,
		ID:          existing.ID,
	}); envErr == nil {
		envCopy := db.Environment{
			BotID:               envRow.BotID,
			Driver:              envRow.Driver,
			EnvID:               envRow.EnvID,
			Status:              envRow.Status,
			Image:               envRow.Image,
			CpuMillis:           envRow.CpuMillis,
			MemoryMib:           envRow.MemoryMib,
			VolumeRef:           envRow.VolumeRef,
			ControlHolderUserID: envRow.ControlHolderUserID,
			ControlNote:         envRow.ControlNote,
			StartedAt:           envRow.StartedAt,
			UpdatedAt:           envRow.UpdatedAt,
		}
		envPtr = &envCopy
	}

	wsUUID := fromPGUUID(wsRow.ID)
	actorUUID := claims.Subject
	if err := recordAudit(r.Context(), txq, &wsUUID, &actorUUID, "bot.updated", "bot", &botID, map[string]any{
		"name": updated.Name,
	}); err != nil {
		s.logger.Error("audit bot update", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to record audit log")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit update bot", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update bot")
		return
	}

	writeJSON(w, s.logger, http.StatusOK, toAPIBotWithEnv(updated, envPtr))
}

func (s *server) handleDeleteBot(w http.ResponseWriter, r *http.Request) {
	wsRow, claims, actorRole, ok := s.loadWorkspaceMembership(w, r, "")
	if !ok {
		return
	}

	botID, err := uuid.Parse(chi.URLParam(r, "botID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid bot identifier")
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin delete bot tx", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to delete bot")
		return
	}
	defer rollbackTx(r.Context(), tx)

	txq := s.queries.WithTx(tx)
	existing, err := txq.GetBotByWorkspaceAndID(r.Context(), db.GetBotByWorkspaceAndIDParams{
		WorkspaceID: wsRow.ID,
		ID:          toPGUUID(botID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusNotFound, "bot.not_found", "bot not found")
			return
		}
		s.logger.Error("load bot for delete", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to delete bot")
		return
	}

	isBotOwner := existing.OwnerUserID.Valid && fromPGUUID(existing.OwnerUserID) == claims.Subject
	if !workspaces.Can(actorRole, workspaces.ActionManageBot, isBotOwner) {
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "insufficient permissions to delete this bot")
		return
	}

	if envRow, envErr := txq.GetEnvironmentByWorkspaceAndBot(r.Context(), db.GetEnvironmentByWorkspaceAndBotParams{
		WorkspaceID: wsRow.ID,
		ID:          existing.ID,
	}); envErr == nil && strings.TrimSpace(envRow.EnvID) != "" && s.compute != nil {
		if destroyErr := s.compute.DestroyEnvironment(r.Context(), compute.EnvID(envRow.EnvID)); destroyErr != nil && !errors.Is(destroyErr, compute.ErrEnvironmentNotFound) {
			s.logger.Warn("failed to destroy sandbox container during bot deletion", "err", destroyErr, "env_id", envRow.EnvID)
		}
	}

	affected, err := txq.DeleteBotByWorkspaceAndID(r.Context(), db.DeleteBotByWorkspaceAndIDParams{
		WorkspaceID: wsRow.ID,
		ID:          existing.ID,
	})
	if err != nil {
		s.logger.Error("delete bot", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to delete bot")
		return
	}
	if affected == 0 {
		writeError(w, s.logger, http.StatusNotFound, "bot.not_found", "bot not found")
		return
	}

	wsUUID := fromPGUUID(wsRow.ID)
	actorUUID := claims.Subject
	if err := recordAudit(r.Context(), txq, &wsUUID, &actorUUID, "bot.deleted", "bot", &botID, map[string]any{
		"name": existing.Name,
	}); err != nil {
		s.logger.Error("audit bot deletion", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to record audit log")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit delete bot", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to delete bot")
		return
	}
	if s.authBroker != nil {
		if bErr := s.authBroker.RevokeBotSessions(r.Context(), wsUUID, botID); bErr != nil {
			s.logger.Warn("failed to revoke isolated broker sessions during bot deletion", "err", bErr, "bot_id", botID.String())
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func validateBotName(raw string) (string, bool) {
	return validateNonEmptyMax(raw, 100)
}

func validateNonEmptyMax(raw string, maxLen int) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 1 || len(trimmed) > maxLen {
		return "", false
	}
	return trimmed, true
}
