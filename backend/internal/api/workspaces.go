// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dunkeltech/botbox/backend/internal/auth"
	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const defaultInvitationTTL = 7 * 24 * time.Hour

func (s *server) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	if !s.ensureDB(w) {
		return
	}
	claims, ok := claimsFromContext(r.Context())
	if !ok {
		writeError(w, s.logger, http.StatusUnauthorized, "auth.unauthenticated", "authentication required")
		return
	}

	rows, err := s.queries.ListWorkspacesForUser(r.Context(), toPGUUID(claims.Subject))
	if err != nil {
		s.logger.Error("list workspaces", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to list workspaces")
		return
	}

	writeJSON(w, s.logger, http.StatusOK, api.WorkspaceListResponse{
		Workspaces: toAPIWorkspaceList(rows),
	})
}

func (s *server) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	if !s.ensureDB(w) {
		return
	}
	claims, ok := claimsFromContext(r.Context())
	if !ok {
		writeError(w, s.logger, http.StatusUnauthorized, "auth.unauthenticated", "authentication required")
		return
	}

	var req api.CreateWorkspaceRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}

	wsName, ok := validateWorkspaceName(req.Name)
	if !ok {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "name must be 1..100 characters")
		return
	}
	wsSlug, ok := validateSlug(req.Slug)
	if !ok {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "slug must match ^[a-z0-9-]+$")
		return
	}

	driver := api.Docker
	if req.DefaultDriver != nil {
		if !req.DefaultDriver.Valid() {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid defaultDriver")
			return
		}
		driver = *req.DefaultDriver
	}

	locale, ok := validateLocale(req.DefaultLocale, "en")
	if !ok {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "defaultLocale must be at most 10 characters")
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin create workspace tx", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to create workspace")
		return
	}
	defer rollbackTx(r.Context(), tx)

	txq := s.queries.WithTx(tx)
	wsRow, err := txq.CreateWorkspace(r.Context(), db.CreateWorkspaceParams{
		Name:          wsName,
		Slug:          wsSlug,
		DefaultDriver: string(driver),
		DefaultLocale: locale,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, s.logger, http.StatusConflict, "workspace.slug_taken", "workspace slug is already taken")
			return
		}
		s.logger.Error("create workspace", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to create workspace")
		return
	}

	if _, err := txq.AddWorkspaceMember(r.Context(), db.AddWorkspaceMemberParams{
		WorkspaceID: wsRow.ID,
		UserID:      toPGUUID(claims.Subject),
		Role:        string(workspaces.RoleOwner),
	}); err != nil {
		s.logger.Error("add workspace creator as owner", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to create workspace membership")
		return
	}

	wsUUID := fromPGUUID(wsRow.ID)
	actorUUID := claims.Subject
	if err := recordAudit(r.Context(), txq, &wsUUID, &actorUUID, "workspace.created", "workspace", &wsUUID, map[string]any{
		"slug": wsSlug,
	}); err != nil {
		s.logger.Error("audit workspace creation", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to record audit log")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit create workspace", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to create workspace")
		return
	}

	writeJSON(w, s.logger, http.StatusCreated, toAPIWorkspaceFromRow(wsRow, string(workspaces.RoleOwner)))
}

func (s *server) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	wsRow, _, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	writeJSON(w, s.logger, http.StatusOK, toAPIWorkspaceFromUserRow(wsRow))
}

func (s *server) handleUpdateWorkspace(w http.ResponseWriter, r *http.Request) {
	wsRow, claims, role, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageWorkspace)
	if !ok {
		return
	}
	var req api.UpdateWorkspaceRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}
	if req.Name == nil && req.DefaultDriver == nil && req.DefaultLocale == nil && req.ScreenshotRetentionDays == nil && req.SandboxIdleTimeoutMinutes == nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "at least one workspace field is required")
		return
	}

	var nextName *string
	if req.Name != nil {
		trimmed, nameOK := validateWorkspaceName(*req.Name)
		if !nameOK {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "name must be 1..100 characters")
			return
		}
		nextName = &trimmed
	}
	if req.DefaultDriver != nil && !req.DefaultDriver.Valid() {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid defaultDriver")
		return
	}
	var nextLocale *string
	if req.DefaultLocale != nil {
		trimmed, localeOK := validateLocale(req.DefaultLocale, "")
		if !localeOK || trimmed == "" {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "defaultLocale must be 1..10 characters")
			return
		}
		nextLocale = &trimmed
	}
	var nextRetention *int32
	if req.ScreenshotRetentionDays != nil {
		days := *req.ScreenshotRetentionDays
		if days < 1 || days > 30 {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "screenshot retention must be between 1 and 30 days")
			return
		}
		v := int32(days)
		nextRetention = &v
	}
	var nextIdleTimeout pgtype.Int4
	if req.SandboxIdleTimeoutMinutes != nil {
		var minutes *int32
		if err := json.Unmarshal(req.SandboxIdleTimeoutMinutes, &minutes); err != nil || (minutes != nil && (*minutes < 0 || *minutes > 10080)) {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "sandboxIdleTimeoutMinutes must be null or an integer between 0 and 10080")
			return
		}
		if minutes != nil {
			nextIdleTimeout = pgtype.Int4{Int32: *minutes, Valid: true}
		}
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin update workspace tx", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update workspace")
		return
	}
	defer rollbackTx(r.Context(), tx)

	txq := s.queries.WithTx(tx)
	existing, err := txq.GetWorkspaceForUpdate(r.Context(), wsRow.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusNotFound, "workspace.not_found", "workspace not found")
			return
		}
		s.logger.Error("lock workspace for update", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update workspace")
		return
	}

	params := db.UpdateWorkspaceSettingsParams{
		ID:                        existing.ID,
		Name:                      existing.Name,
		DefaultDriver:             existing.DefaultDriver,
		DefaultLocale:             existing.DefaultLocale,
		ScreenshotRetentionDays:   existing.ScreenshotRetentionDays,
		SandboxIdleTimeoutMinutes: existing.SandboxIdleTimeoutMinutes,
	}
	if nextName != nil {
		params.Name = *nextName
	}
	if req.DefaultDriver != nil {
		params.DefaultDriver = string(*req.DefaultDriver)
	}
	if nextLocale != nil {
		params.DefaultLocale = *nextLocale
	}
	if nextRetention != nil {
		params.ScreenshotRetentionDays = *nextRetention
	}
	if req.SandboxIdleTimeoutMinutes != nil {
		params.SandboxIdleTimeoutMinutes = nextIdleTimeout
	}

	updated, err := txq.UpdateWorkspaceSettings(r.Context(), params)
	if err != nil {
		s.logger.Error("update workspace settings", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update workspace")
		return
	}

	wsUUID := fromPGUUID(wsRow.ID)
	actorUUID := claims.Subject
	if err := recordAudit(r.Context(), txq, &wsUUID, &actorUUID, "workspace.updated", "workspace", &wsUUID, map[string]any{
		"name":                    updated.Name,
		"defaultDriver":           updated.DefaultDriver,
		"defaultLocale":           updated.DefaultLocale,
		"screenshotRetentionDays": updated.ScreenshotRetentionDays,
	}); err != nil {
		s.logger.Error("audit workspace update", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to record audit log")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit workspace update", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update workspace")
		return
	}

	writeJSON(w, s.logger, http.StatusOK, toAPIWorkspaceFromRow(updated, string(role)))
}

func (s *server) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	wsRow, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageWorkspace)
	if !ok {
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin delete workspace tx", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to delete workspace")
		return
	}
	defer rollbackTx(r.Context(), tx)

	txq := s.queries.WithTx(tx)
	existing, err := txq.GetWorkspaceForUpdate(r.Context(), wsRow.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusNotFound, "workspace.not_found", "workspace not found")
			return
		}
		s.logger.Error("lock workspace for delete", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to delete workspace")
		return
	}

	wsCount, err := txq.CountWorkspacesForUser(r.Context(), toPGUUID(claims.Subject))
	if err != nil {
		s.logger.Error("count user workspaces", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to validate workspaces")
		return
	}
	if wsCount <= 1 {
		writeError(w, s.logger, http.StatusBadRequest, "workspace.last_workspace", "cannot delete your last remaining workspace")
		return
	}

	busy, err := txq.HasActiveWorkspaceWork(r.Context(), existing.ID)
	if err != nil {
		s.logger.Error("check active workspace work", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to check workspace activity")
		return
	}
	if busy {
		writeError(w, s.logger, http.StatusConflict, "workspace.busy", "workspace has active bot work or credential sessions")
		return
	}

	bots, err := txq.ListBotsByWorkspace(r.Context(), existing.ID)
	if err != nil {
		s.logger.Error("list workspace bots before delete", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to delete workspace")
		return
	}
	envs, err := txq.ListEnvironmentsByWorkspace(r.Context(), existing.ID)
	if err != nil {
		s.logger.Error("list workspace environments before delete", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to delete workspace")
		return
	}

	if s.compute != nil {
		for _, envRow := range envs {
			if strings.TrimSpace(envRow.EnvID) != "" {
				if destroyErr := s.compute.DestroyEnvironment(r.Context(), compute.EnvID(envRow.EnvID)); destroyErr != nil && !errors.Is(destroyErr, compute.ErrEnvironmentNotFound) {
					s.logger.Warn("failed to destroy sandbox container during workspace deletion", "err", destroyErr, "env_id", envRow.EnvID)
				}
			}
		}
	}

	// Clear conversations first so messages.sender_* FKs do not block cascading
	// deletions of workspace_members and bots when the workspace row is removed.
	if err := txq.DeleteWorkspaceConversations(r.Context(), existing.ID); err != nil {
		s.logger.Error("delete workspace conversations", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to delete workspace")
		return
	}

	affected, err := txq.DeleteWorkspaceByID(r.Context(), existing.ID)
	if err != nil {
		s.logger.Error("delete workspace", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to delete workspace")
		return
	}
	if affected == 0 {
		writeError(w, s.logger, http.StatusNotFound, "workspace.not_found", "workspace not found")
		return
	}

	wsUUID := fromPGUUID(existing.ID)
	actorUUID := claims.Subject
	// Record with nil workspace_id so ON DELETE CASCADE does not remove the audit row.
	if err := recordAudit(r.Context(), txq, nil, &actorUUID, "workspace.deleted", "workspace", &wsUUID, map[string]any{
		"slug": existing.Slug,
		"name": existing.Name,
	}); err != nil {
		s.logger.Error("audit workspace deletion", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to record audit log")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit delete workspace", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to delete workspace")
		return
	}

	if s.authBroker != nil {
		for _, b := range bots {
			if bErr := s.authBroker.RevokeBotSessions(r.Context(), wsUUID, fromPGUUID(b.ID)); bErr != nil {
				s.logger.Warn("failed to revoke isolated broker sessions during workspace deletion", "err", bErr, "bot_id", fromPGUUID(b.ID).String())
			}
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	wsRow, _, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}

	rows, err := s.queries.ListWorkspaceMembers(r.Context(), wsRow.ID)
	if err != nil {
		s.logger.Error("list workspace members", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to list workspace members")
		return
	}

	out := make([]api.WorkspaceMember, 0, len(rows))
	for _, m := range rows {
		out = append(out, toAPIWorkspaceMember(m))
	}
	writeJSON(w, s.logger, http.StatusOK, api.WorkspaceMemberListResponse{Members: out})
}

// lockMemberships serializes authorization and owner-count checks with other
// membership changes in the same workspace.
func (s *server) lockMemberships(w http.ResponseWriter, r *http.Request, q *db.Queries, workspaceID pgtype.UUID) bool {
	_, err := q.GetWorkspaceForUpdate(r.Context(), workspaceID)
	if err == nil {
		return true
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, s.logger, http.StatusNotFound, "workspace.not_found", "workspace not found")
		return false
	}
	s.logger.Error("lock workspace memberships", "err", err)
	writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to lock workspace memberships")
	return false
}

func (s *server) checkLockedActor(w http.ResponseWriter, r *http.Request, q *db.Queries, workspaceID pgtype.UUID, userID uuid.UUID, action workspaces.Action) (workspaces.Role, bool) {
	member, err := q.GetWorkspaceMember(r.Context(), db.GetWorkspaceMemberParams{WorkspaceID: workspaceID, UserID: toPGUUID(userID)})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, s.logger, http.StatusNotFound, "workspace.not_found", "workspace not found")
		return "", false
	}
	if err != nil {
		s.logger.Error("reload workspace actor", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load workspace membership")
		return "", false
	}
	role, valid := workspaces.ParseRole(member.Role)
	if !valid || (action != "" && !workspaces.Can(role, action, false)) {
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "insufficient workspace permissions")
		return "", false
	}
	return role, true
}

func (s *server) handleUpdateMemberRole(w http.ResponseWriter, r *http.Request) {
	wsRow, claims, callerRole, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}

	targetUserID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid user identifier")
		return
	}

	// Enforce caller authorization before body validation so viewers and
	// non-self member targets receive 403 even on malformed or empty payloads.
	if callerRole == workspaces.RoleViewer {
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "viewers cannot modify workspace members")
		return
	}
	if callerRole == workspaces.RoleMember && targetUserID != claims.Subject {
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "members can only update their own role description")
		return
	}

	var req api.UpdateMemberRoleRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}
	if req.Role == nil && req.RoleDescription == nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "at least one of role or roleDescription is required")
		return
	}

	var newRole workspaces.Role
	if req.Role != nil {
		var valid bool
		newRole, valid = workspaces.ParseRole(string(*req.Role))
		if !valid {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid workspace role")
			return
		}
	}

	var roleDesc string
	if req.RoleDescription != nil {
		if strings.ContainsRune(*req.RoleDescription, '\x00') {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "roleDescription cannot contain NUL characters")
			return
		}
		roleDesc = strings.TrimSpace(*req.RoleDescription)
		if utf8.RuneCountInString(roleDesc) > 4000 {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "roleDescription must be at most 4000 characters")
			return
		}
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin update member role tx", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update member role")
		return
	}
	defer rollbackTx(r.Context(), tx)

	txq := s.queries.WithTx(tx)
	if !s.lockMemberships(w, r, txq, wsRow.ID) {
		return
	}
	actorRole, ok := s.checkLockedActor(w, r, txq, wsRow.ID, claims.Subject, "")
	if !ok {
		return
	}
	target, err := txq.GetWorkspaceMember(r.Context(), db.GetWorkspaceMemberParams{
		WorkspaceID: wsRow.ID,
		UserID:      toPGUUID(targetUserID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusNotFound, "workspace.member_not_found", "workspace member not found")
			return
		}
		s.logger.Error("get member before role update", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load workspace member")
		return
	}

	currentRole := workspaces.Role(target.Role)
	isSelf := targetUserID == claims.Subject

	if req.Role != nil {
		if !workspaces.Can(actorRole, workspaces.ActionManageMembers, false) ||
			!workspaces.CanModifyMember(actorRole, currentRole) ||
			!workspaces.CanAssignRole(actorRole, newRole) {
			writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "cannot assign or modify this workspace role")
			return
		}

		if currentRole == workspaces.RoleOwner && newRole != workspaces.RoleOwner {
			owners, countErr := txq.CountWorkspaceOwners(r.Context(), wsRow.ID)
			if countErr != nil {
				s.logger.Error("count workspace owners", "err", countErr)
				writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to validate workspace owners")
				return
			}
			if owners <= 1 {
				writeError(w, s.logger, http.StatusBadRequest, "workspace.last_owner", "workspace must retain at least one owner")
				return
			}
		}
	}

	if req.RoleDescription != nil && !workspaces.CanEditMemberRoleDescription(actorRole, currentRole, isSelf) {
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "cannot modify this member's role description")
		return
	}

	wsUUID := fromPGUUID(wsRow.ID)
	actorUUID := claims.Subject

	if req.Role != nil {
		if _, err := txq.UpdateWorkspaceMemberRole(r.Context(), db.UpdateWorkspaceMemberRoleParams{
			WorkspaceID: wsRow.ID,
			UserID:      toPGUUID(targetUserID),
			Role:        string(newRole),
		}); err != nil {
			s.logger.Error("update member role", "err", err)
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update member role")
			return
		}
		if err := recordAudit(r.Context(), txq, &wsUUID, &actorUUID, "member.role_updated", "user", &targetUserID, map[string]any{
			"fromRole": string(currentRole),
			"toRole":   string(newRole),
		}); err != nil {
			s.logger.Error("audit member role update", "err", err)
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to record audit log")
			return
		}
	}

	if req.RoleDescription != nil {
		if _, err := txq.UpdateWorkspaceMemberRoleDescription(r.Context(), db.UpdateWorkspaceMemberRoleDescriptionParams{
			WorkspaceID:     wsRow.ID,
			UserID:          toPGUUID(targetUserID),
			RoleDescription: roleDesc,
		}); err != nil {
			s.logger.Error("update member role description", "err", err)
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update member role description")
			return
		}
		if err := recordAudit(r.Context(), txq, &wsUUID, &actorUUID, "member.role_description_updated", "user", &targetUserID, map[string]any{
			"hasRole": roleDesc != "",
			"length":  utf8.RuneCountInString(roleDesc),
		}); err != nil {
			s.logger.Error("audit member role description update", "err", err)
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to record audit log")
			return
		}
	}

	updated, err := txq.GetWorkspaceMember(r.Context(), db.GetWorkspaceMemberParams{
		WorkspaceID: wsRow.ID,
		UserID:      toPGUUID(targetUserID),
	})
	if err != nil {
		s.logger.Error("reload updated member", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to reload workspace member")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit member role update", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to update member role")
		return
	}

	writeJSON(w, s.logger, http.StatusOK, toAPIWorkspaceMemberGet(updated))
}

func (s *server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	wsRow, claims, _, ok := s.loadWorkspaceMembership(w, r, "")
	if !ok {
		return
	}

	targetUserID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid user identifier")
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin remove member tx", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to remove member")
		return
	}
	defer rollbackTx(r.Context(), tx)

	txq := s.queries.WithTx(tx)
	if !s.lockMemberships(w, r, txq, wsRow.ID) {
		return
	}
	actorRole, ok := s.checkLockedActor(w, r, txq, wsRow.ID, claims.Subject, "")
	if !ok {
		return
	}
	target, err := txq.GetWorkspaceMember(r.Context(), db.GetWorkspaceMemberParams{
		WorkspaceID: wsRow.ID,
		UserID:      toPGUUID(targetUserID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusNotFound, "workspace.member_not_found", "workspace member not found")
			return
		}
		s.logger.Error("load member for removal", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load workspace member")
		return
	}

	isSelf := targetUserID == claims.Subject
	if !isSelf {
		if !workspaces.Can(actorRole, workspaces.ActionManageMembers, false) ||
			!workspaces.CanModifyMember(actorRole, workspaces.Role(target.Role)) {
			writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "insufficient permissions to remove this member")
			return
		}
	}

	if workspaces.Role(target.Role) == workspaces.RoleOwner {
		owners, countErr := txq.CountWorkspaceOwners(r.Context(), wsRow.ID)
		if countErr != nil {
			s.logger.Error("count owners on removal", "err", countErr)
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to validate workspace owners")
			return
		}
		if owners <= 1 {
			writeError(w, s.logger, http.StatusBadRequest, "workspace.last_owner", "workspace must retain at least one owner")
			return
		}
	}

	affected, err := txq.RemoveWorkspaceMember(r.Context(), db.RemoveWorkspaceMemberParams{
		WorkspaceID: wsRow.ID,
		UserID:      toPGUUID(targetUserID),
	})
	if err != nil {
		s.logger.Error("remove workspace member", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to remove member")
		return
	}
	if affected == 0 {
		writeError(w, s.logger, http.StatusNotFound, "workspace.member_not_found", "workspace member not found")
		return
	}

	wsUUID := fromPGUUID(wsRow.ID)
	actorUUID := claims.Subject
	if err := recordAudit(r.Context(), txq, &wsUUID, &actorUUID, "member.removed", "user", &targetUserID, map[string]any{
		"role": target.Role,
	}); err != nil {
		s.logger.Error("audit member removal", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to record audit log")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit remove member", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to remove member")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleListInvitations(w http.ResponseWriter, r *http.Request) {
	wsRow, _, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageInvitations)
	if !ok {
		return
	}

	rows, err := s.queries.ListPendingInvitationsByWorkspace(r.Context(), wsRow.ID)
	if err != nil {
		s.logger.Error("list invitations", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to list invitations")
		return
	}

	now := s.now().UTC()
	out := make([]api.InvitationSummary, 0, len(rows))
	for _, inv := range rows {
		if !inv.ExpiresAt.Time.After(now) {
			continue
		}
		out = append(out, toAPIInvitationSummary(inv))
	}

	writeJSON(w, s.logger, http.StatusOK, api.InvitationListResponse{Invitations: out})
}

func (s *server) handleCreateInvitation(w http.ResponseWriter, r *http.Request) {
	wsRow, claims, actorRole, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageInvitations)
	if !ok {
		return
	}

	var req api.CreateInvitationRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}

	role, valid := workspaces.ParseRole(string(req.Role))
	if !valid {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid invitation role")
		return
	}
	if !workspaces.CanAssignRole(actorRole, role) {
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "cannot invite a user above your role tier")
		return
	}

	var pgEmail pgtype.Text
	if req.Email != nil {
		normalized, emailOK := validateEmail(string(*req.Email))
		if !emailOK {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid invitation email")
			return
		}
		pgEmail = pgtype.Text{String: normalized, Valid: true}
	}

	plainToken, digest, err := auth.GenerateOpaqueToken("bbx_inv_", 32)
	if err != nil {
		s.logger.Error("generate invitation token", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to create invitation")
		return
	}

	exp := s.now().UTC().Add(defaultInvitationTTL)
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin create invitation tx", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to create invitation")
		return
	}
	defer rollbackTx(r.Context(), tx)

	txq := s.queries.WithTx(tx)
	if !s.lockMemberships(w, r, txq, wsRow.ID) {
		return
	}
	actorRole, ok = s.checkLockedActor(w, r, txq, wsRow.ID, claims.Subject, workspaces.ActionManageInvitations)
	if !ok {
		return
	}
	if !workspaces.CanAssignRole(actorRole, role) {
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "cannot invite a user above your role tier")
		return
	}
	inv, err := txq.CreateInvitation(r.Context(), db.CreateInvitationParams{
		WorkspaceID: wsRow.ID,
		Email:       pgEmail,
		Role:        string(role),
		TokenHash:   digest,
		CreatedBy:   toPGUUID(claims.Subject),
		ExpiresAt:   toPGTimestamptz(exp),
	})
	if err != nil {
		s.logger.Error("create invitation", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to create invitation")
		return
	}

	wsUUID := fromPGUUID(wsRow.ID)
	invUUID := fromPGUUID(inv.ID)
	actorUUID := claims.Subject
	if err := recordAudit(r.Context(), txq, &wsUUID, &actorUUID, "invitation.created", "invitation", &invUUID, map[string]any{
		"role": string(role),
	}); err != nil {
		s.logger.Error("audit create invitation", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to record audit log")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit create invitation", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to create invitation")
		return
	}

	inviteURL := "/invite/" + plainToken
	if base := strings.TrimRight(strings.TrimSpace(s.publicBaseURL), "/"); base != "" {
		inviteURL = base + inviteURL
	}

	writeJSON(w, s.logger, http.StatusCreated, api.CreatedInvitationResponse{
		Invitation: toAPIInvitationSummary(inv),
		Token:      plainToken,
		InviteUrl:  inviteURL,
	})
}

func (s *server) handleRevokeInvitation(w http.ResponseWriter, r *http.Request) {
	wsRow, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageInvitations)
	if !ok {
		return
	}

	invID, err := uuid.Parse(chi.URLParam(r, "invitationID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid invitation identifier")
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin revoke invitation tx", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to revoke invitation")
		return
	}
	defer rollbackTx(r.Context(), tx)

	txq := s.queries.WithTx(tx)
	affected, err := txq.DeleteInvitationByWorkspaceAndID(r.Context(), db.DeleteInvitationByWorkspaceAndIDParams{
		WorkspaceID: wsRow.ID,
		ID:          toPGUUID(invID),
	})
	if err != nil {
		s.logger.Error("delete invitation", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to revoke invitation")
		return
	}
	if affected == 0 {
		writeError(w, s.logger, http.StatusNotFound, "invitation.not_found", "invitation not found")
		return
	}

	wsUUID := fromPGUUID(wsRow.ID)
	actorUUID := claims.Subject
	if err := recordAudit(r.Context(), txq, &wsUUID, &actorUUID, "invitation.revoked", "invitation", &invID, nil); err != nil {
		s.logger.Error("audit revoke invitation", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to record audit log")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit revoke invitation", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to revoke invitation")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handlePreviewInvitation(w http.ResponseWriter, r *http.Request) {
	if !s.ensureDB(w) {
		return
	}

	plainToken := strings.TrimSpace(chi.URLParam(r, "token"))
	if plainToken == "" {
		writeError(w, s.logger, http.StatusNotFound, "invitation.not_found", "invitation not found or expired")
		return
	}

	inv, err := s.queries.GetInvitationByTokenHash(r.Context(), auth.HashToken(plainToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusNotFound, "invitation.not_found", "invitation not found or expired")
			return
		}
		s.logger.Error("preview invitation", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load invitation")
		return
	}

	if inv.AcceptedAt.Valid || !inv.ExpiresAt.Time.After(s.now().UTC()) {
		writeError(w, s.logger, http.StatusNotFound, "invitation.not_found", "invitation not found or expired")
		return
	}

	writeJSON(w, s.logger, http.StatusOK, api.InvitationPreviewResponse{
		Id:            fromPGUUID(inv.ID),
		WorkspaceId:   fromPGUUID(inv.WorkspaceID),
		WorkspaceName: inv.WorkspaceName,
		WorkspaceSlug: inv.WorkspaceSlug,
		Email:         toAPIEmailPtr(inv.Email),
		Role:          api.WorkspaceRole(inv.Role),
		ExpiresAt:     inv.ExpiresAt.Time.UTC(),
	})
}

func (s *server) handleAcceptInvitation(w http.ResponseWriter, r *http.Request) {
	if !s.ensureDB(w) {
		return
	}

	plainToken := strings.TrimSpace(chi.URLParam(r, "token"))
	if plainToken == "" {
		writeError(w, s.logger, http.StatusNotFound, "invitation.not_found", "invitation not found or expired")
		return
	}

	// If the caller supplied an Authorization header, validate it strictly rather than
	// silently creating a duplicate unauthenticated signup flow.
	var signedInClaims *auth.Claims
	var existingAccessToken string
	if authHeader := r.Header.Get("Authorization"); authHeader != "" {
		tok, ok := extractBearerToken(authHeader)
		if !ok || s.authProvider == nil {
			writeError(w, s.logger, http.StatusUnauthorized, "auth.unauthenticated", "invalid authorization header")
			return
		}
		claims, err := s.authProvider.ValidateToken(r.Context(), tok)
		if err != nil {
			writeError(w, s.logger, http.StatusUnauthorized, "auth.unauthenticated", "invalid or expired access token")
			return
		}
		signedInClaims = claims
		existingAccessToken = tok
	}

	var req api.AcceptInvitationRequest
	if !decodeOptionalJSON(w, r, s.logger, &req) {
		return
	}

	now := s.now().UTC()
	digest := auth.HashToken(plainToken)

	// Pre-validate before running Argon2id on new signups so bad requests or
	// already-registered emails fail fast without wasting CPU.
	preview, err := s.queries.GetInvitationByTokenHash(r.Context(), digest)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusNotFound, "invitation.not_found", "invitation not found or expired")
			return
		}
		s.logger.Error("lookup invitation before accept", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to accept invitation")
		return
	}
	if preview.AcceptedAt.Valid || !preview.ExpiresAt.Time.After(now) {
		writeError(w, s.logger, http.StatusNotFound, "invitation.not_found", "invitation not found or expired")
		return
	}

	var passwordHash string
	var signupEmail, signupDisplayName string
	if signedInClaims == nil {
		if preview.Email.Valid {
			signupEmail = preview.Email.String
			if req.Email != nil {
				reqEmail, ok := validateEmail(string(*req.Email))
				if !ok {
					writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid email address")
					return
				}
				if reqEmail != preview.Email.String {
					writeError(w, s.logger, http.StatusForbidden, "invitation.email_mismatch", "invitation belongs to a different email address")
					return
				}
			}
		} else {
			if req.Email == nil {
				writeError(w, s.logger, http.StatusBadRequest, "validation_error", "email is required")
				return
			}
			var ok bool
			signupEmail, ok = validateEmail(string(*req.Email))
			if !ok {
				writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid email address")
				return
			}
		}

		if existingUser, lookupErr := s.queries.GetUserByEmail(r.Context(), signupEmail); lookupErr == nil && existingUser.ID.Valid {
			writeError(w, s.logger, http.StatusConflict, "auth.account_exists", "an account with this email already exists; sign in first to accept this invitation")
			return
		} else if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
			s.logger.Error("check existing user on invitation accept", "err", lookupErr)
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to verify account status")
			return
		}

		if req.DisplayName == nil {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "displayName is required")
			return
		}
		var nameOK bool
		signupDisplayName, nameOK = validateDisplayName(*req.DisplayName)
		if !nameOK {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "displayName must be 1..100 characters")
			return
		}
		if req.Password == nil {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "password is required")
			return
		}
		if err := auth.ValidatePassword(*req.Password); err != nil {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
			return
		}

		passwordHash, err = auth.HashPassword(*req.Password, s.argon2)
		if err != nil {
			s.logger.Error("hash password on invitation accept", "err", err)
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to process password")
			return
		}
	}

	var existingUser *uuid.UUID
	if signedInClaims != nil {
		existingUser = &signedInClaims.Subject
	}
	accepted, err := s.acceptInvitation(r.Context(), preview.WorkspaceID, digest, existingUser, db.CreateUserParams{
		Email: signupEmail, DisplayName: signupDisplayName, PasswordHash: passwordHash, Locale: "en",
	})
	if err != nil {
		var failure *invitationAcceptError
		if !errors.As(err, &failure) {
			s.logger.Error("accept invitation", "err", err)
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to accept invitation")
			return
		}
		if failure.cause != nil {
			s.logger.Error("accept invitation", "err", failure.cause)
		}
		writeError(w, s.logger, invitationErrorStatus(failure.code), failure.code, failure.message)
		return
	}

	wsList := toAPIWorkspaceList(accepted.workspaces)
	var activeWS *api.WorkspaceSummary
	for i := range wsList {
		if wsList[i].Id == accepted.workspaceID {
			wsCopy := wsList[i]
			activeWS = &wsCopy
			break
		}
	}

	// Only signup opens a new session. Existing JWTs keep their expiry and refresh family.
	accessToken := existingAccessToken
	if accepted.refreshToken != "" {
		s.setRefreshCookie(w, accepted.refreshToken)
		accessToken = accepted.accessToken
	}
	writeJSON(w, s.logger, http.StatusOK, api.AuthSessionResponse{
		User:            toAPIUser(accepted.user),
		Workspaces:      wsList,
		ActiveWorkspace: activeWS,
		AccessToken:     accessToken,
	})
}

func invitationErrorStatus(code string) int {
	switch code {
	case "workspace.not_found", "invitation.not_found":
		return http.StatusNotFound
	case "auth.unauthenticated":
		return http.StatusUnauthorized
	case "invitation.email_mismatch":
		return http.StatusForbidden
	case "auth.account_exists", "workspace.already_member":
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}
