package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/dunkeltech/botbox/backend/internal/auth"
	"github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	if !s.ensureDB(w) {
		return
	}

	count, err := s.queries.CountUsers(r.Context())
	if err != nil {
		s.logger.Error("count users during setup status", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to inspect setup status")
		return
	}

	// Setup permanently disappears once at least one user exists or if the server
	// was not started with an active SetupManager.
	if count > 0 || s.setup == nil || !s.setup.Active() {
		writeError(w, s.logger, http.StatusNotFound, "setup.already_completed", "initial setup has already been completed")
		return
	}

	writeJSON(w, s.logger, http.StatusOK, api.SetupStatusResponse{
		SetupRequired: true,
	})
}

func (s *server) handleSetupComplete(w http.ResponseWriter, r *http.Request) {
	if !s.ensureDB(w) {
		return
	}

	if !s.setupRL.Allow(clientIP(r)) {
		writeError(w, s.logger, http.StatusTooManyRequests, "rate_limited", "too many setup attempts; try again later")
		return
	}

	// Fast-path check before decoding/hashing so completed instances always lock out with 404.
	count, err := s.queries.CountUsers(r.Context())
	if err != nil {
		s.logger.Error("count users during setup complete", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to inspect setup status")
		return
	}
	if count > 0 || s.setup == nil || !s.setup.Active() {
		writeError(w, s.logger, http.StatusNotFound, "setup.already_completed", "initial setup has already been completed")
		return
	}

	var req api.SetupCompleteRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}

	if req.SetupSecret == "" {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "setupSecret is required")
		return
	}
	if !s.setup.VerifySecret(req.SetupSecret) {
		writeError(w, s.logger, http.StatusForbidden, "setup.invalid_secret", "invalid setup secret")
		return
	}

	displayName, ok := validateDisplayName(req.DisplayName)
	if !ok {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "displayName must be 1..100 characters")
		return
	}
	email, ok := validateEmail(string(req.Email))
	if !ok {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid email address")
		return
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	wsName, ok := validateWorkspaceName(req.WorkspaceName)
	if !ok {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "workspaceName must be 1..100 characters")
		return
	}
	wsSlug, ok := validateSlug(req.WorkspaceSlug)
	if !ok {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "workspaceSlug must match ^[a-z0-9-]+$")
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

	// Hash outside the advisory lock so CPU-bound Argon2id does not stall concurrent setup checks.
	passwordHash, err := auth.HashPassword(req.Password, s.argon2)
	if err != nil {
		s.logger.Error("hash password during setup", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to hash password")
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin setup transaction", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to complete setup")
		return
	}
	defer rollbackTx(r.Context(), tx)

	txq := s.queries.WithTx(tx)
	if err := txq.AcquireSetupLock(r.Context()); err != nil {
		s.logger.Error("acquire setup lock", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to lock setup")
		return
	}

	txCount, err := txq.CountUsers(r.Context())
	if err != nil {
		s.logger.Error("recount users inside setup lock", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to verify setup state")
		return
	}
	if txCount > 0 {
		writeError(w, s.logger, http.StatusNotFound, "setup.already_completed", "initial setup has already been completed")
		return
	}

	userRow, err := txq.CreateUser(r.Context(), db.CreateUserParams{
		Email:        email,
		PasswordHash: passwordHash,
		DisplayName:  displayName,
		Locale:       locale,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, s.logger, http.StatusConflict, "auth.account_exists", "user already exists")
			return
		}
		s.logger.Error("create setup user", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to create owner user")
		return
	}

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
		s.logger.Error("create setup workspace", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to create workspace")
		return
	}

	if _, err := txq.AddWorkspaceMember(r.Context(), db.AddWorkspaceMemberParams{
		WorkspaceID: wsRow.ID,
		UserID:      userRow.ID,
		Role:        string(workspaces.RoleOwner),
	}); err != nil {
		s.logger.Error("add setup workspace owner", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to assign workspace owner")
		return
	}

	plainRefresh, _, err := s.issueRefreshTokenTx(r.Context(), txq, fromPGUUID(userRow.ID), nil)
	if err != nil {
		s.logger.Error("issue setup refresh token", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to create session")
		return
	}

	wsUUID := fromPGUUID(wsRow.ID)
	userUUID := fromPGUUID(userRow.ID)
	if err := recordAudit(r.Context(), txq, &wsUUID, &userUUID, "setup.completed", "workspace", &wsUUID, map[string]any{
		"slug": wsSlug,
	}); err != nil {
		s.logger.Error("record setup audit log", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to record setup audit log")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		if errors.Is(err, pgx.ErrTxClosed) {
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to commit setup")
			return
		}
		s.logger.Error("commit setup transaction", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to commit setup")
		return
	}

	// Zero the one-time setup secret digest immediately once the initial owner is committed.
	s.setup.Clear()

	wsSummary := toAPIWorkspaceFromRow(wsRow, string(workspaces.RoleOwner))
	accessToken, err := s.issueAccessToken(userRow)
	if err != nil {
		s.logger.Error("issue setup access token", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to issue access token")
		return
	}

	s.setRefreshCookie(w, plainRefresh)
	writeJSON(w, s.logger, http.StatusCreated, api.AuthSessionResponse{
		User:            toAPIUser(userRow),
		Workspaces:      []api.WorkspaceSummary{wsSummary},
		ActiveWorkspace: &wsSummary,
		AccessToken:     accessToken,
	})
}

func (s *server) issueAccessToken(u db.User) (string, error) {
	return s.jwt.IssueAccessToken(auth.User{
		ID:          fromPGUUID(u.ID),
		Email:       u.Email,
		DisplayName: u.DisplayName,
		Locale:      u.Locale,
		CreatedAt:   u.CreatedAt.Time.UTC(),
	})
}

func (s *server) issueRefreshTokenTx(ctx context.Context, txq *db.Queries, userID uuid.UUID, familyID *uuid.UUID) (string, db.RefreshToken, error) {
	plain, digest, err := auth.GenerateOpaqueToken("bbx_rt_", 32)
	if err != nil {
		return "", db.RefreshToken{}, err
	}
	var fam uuid.UUID
	if familyID == nil {
		fam = uuid.New()
		if err := txq.CreateRefreshTokenFamily(ctx, toPGUUID(fam)); err != nil {
			return "", db.RefreshToken{}, err
		}
	} else {
		fam = *familyID
	}
	exp := s.now().UTC().Add(auth.DefaultRefreshTokenTTL)
	row, err := txq.CreateRefreshToken(ctx, db.CreateRefreshTokenParams{
		UserID:    toPGUUID(userID),
		FamilyID:  toPGUUID(fam),
		TokenHash: digest,
		ExpiresAt: toPGTimestamptz(exp),
	})
	if err != nil {
		return "", db.RefreshToken{}, err
	}
	return plain, row, nil
}
