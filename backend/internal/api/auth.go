// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/dunkeltech/botbox/backend/internal/auth"
	"github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

const refreshCookieName = "botbox_refresh"

func (s *server) newRefreshCookie(value string, maxAge int) *http.Cookie {
	// #nosec G124 -- Secure matches deployment scheme so local HTTP development still receives cookies.
	return &http.Cookie{
		Name:     refreshCookieName,
		Value:    value,
		Path:     "/api",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

func (s *server) setRefreshCookie(w http.ResponseWriter, plainToken string) {
	http.SetCookie(w, s.newRefreshCookie(plainToken, int(auth.DefaultRefreshTokenTTL.Seconds())))
}

func (s *server) clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, s.newRefreshCookie("", -1))
}

func readRefreshToken(r *http.Request, w http.ResponseWriter, logger *slog.Logger) (string, bool) {
	if c, err := r.Cookie(refreshCookieName); err == nil && c.Value != "" {
		return c.Value, true
	}
	var body api.RefreshRequest
	if !decodeOptionalJSON(w, r, logger, &body) {
		return "", false
	}
	if body.RefreshToken != nil && *body.RefreshToken != "" {
		return *body.RefreshToken, true
	}
	return "", true
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.ensureDB(w) {
		return
	}

	ip := clientIP(r)
	if !s.loginIPRL.Allow(ip) {
		writeError(w, s.logger, http.StatusTooManyRequests, "rate_limited", "too many login attempts; try again later")
		return
	}

	var req api.LoginRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}

	email := auth.NormalizeEmail(string(req.Email))
	if email == "" || req.Password == "" {
		writeError(w, s.logger, http.StatusUnauthorized, "auth.invalid_credentials", "invalid email or password")
		return
	}

	if !s.loginEmailRL.Allow(email) {
		writeError(w, s.logger, http.StatusTooManyRequests, "rate_limited", "too many login attempts for this account; try again later")
		return
	}

	user, err := s.authProvider.Authenticate(r.Context(), auth.Credentials{
		Email:    email,
		Password: req.Password,
	})
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeError(w, s.logger, http.StatusUnauthorized, "auth.invalid_credentials", "invalid email or password")
			return
		}
		s.logger.Error("authenticate user", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "authentication failed")
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin login tx", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to create session")
		return
	}
	defer rollbackTx(r.Context(), tx)

	txq := s.queries.WithTx(tx)
	plainRefresh, _, err := s.issueRefreshTokenTx(r.Context(), txq, user.ID, nil)
	if err != nil {
		s.logger.Error("issue refresh token", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to issue refresh token")
		return
	}

	wsRows, err := txq.ListWorkspacesForUser(r.Context(), toPGUUID(user.ID))
	if err != nil {
		s.logger.Error("list workspaces on login", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load workspaces")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit login tx", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to create session")
		return
	}

	accessToken, err := s.jwt.IssueAccessToken(*user)
	if err != nil {
		s.logger.Error("issue jwt on login", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to issue access token")
		return
	}

	workspacesList := toAPIWorkspaceList(wsRows)
	var activeWS *api.WorkspaceSummary
	if len(workspacesList) > 0 {
		first := workspacesList[0]
		activeWS = &first
	}

	s.setRefreshCookie(w, plainRefresh)
	writeJSON(w, s.logger, http.StatusOK, api.AuthSessionResponse{
		User: api.User{
			Id:          user.ID,
			Email:       openapi_types.Email(user.Email),
			DisplayName: user.DisplayName,
			Locale:      user.Locale,
			CreatedAt:   user.CreatedAt,
		},
		Workspaces:      workspacesList,
		ActiveWorkspace: activeWS,
		AccessToken:     accessToken,
	})
}

func (s *server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if !s.ensureDB(w) {
		return
	}

	if !s.refreshRL.Allow(clientIP(r)) {
		writeError(w, s.logger, http.StatusTooManyRequests, "rate_limited", "too many refresh attempts; try again later")
		return
	}

	plainToken, ok := readRefreshToken(r, w, s.logger)
	if !ok {
		return
	}
	if plainToken == "" {
		writeError(w, s.logger, http.StatusUnauthorized, "auth.invalid_refresh_token", "refresh token missing or invalid")
		return
	}

	digest := auth.HashToken(plainToken)
	now := s.now().UTC()
	pgNow := toPGTimestamptz(now)

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin refresh tx", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to refresh session")
		return
	}
	defer rollbackTx(r.Context(), tx)

	txq := s.queries.WithTx(tx)
	family, err := txq.LockRefreshTokenFamilyByHash(r.Context(), digest)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusUnauthorized, "auth.invalid_refresh_token", "refresh token missing or invalid")
			return
		}
		s.logger.Error("lock refresh token family", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to refresh session")
		return
	}

	if family.RevokedAt.Valid {
		s.clearRefreshCookie(w)
		writeError(w, s.logger, http.StatusUnauthorized, "auth.refresh_token_reused", "session was revoked")
		return
	}
	// Read the token after acquiring the family lock so a completed rotation
	// is visible even when this request waited for another transaction.
	tok, err := txq.GetRefreshTokenByHash(r.Context(), digest)
	if err != nil {
		s.logger.Error("load refresh token", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to refresh session")
		return
	}

	// Re-presenting an already rotated or revoked token indicates token theft or replay;
	// revoke the entire token family immediately and commit before responding.
	if tok.RevokedAt.Valid || tok.ReplacedBy.Valid {
		if revErr := txq.RevokeRefreshTokenFamily(r.Context(), db.RevokeRefreshTokenFamilyParams{
			ID:        family.ID,
			RevokedAt: pgNow,
		}); revErr != nil {
			s.logger.Error("revoke compromised refresh token family", "err", revErr)
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to revoke compromised session")
			return
		}
		if commitErr := tx.Commit(r.Context()); commitErr != nil {
			s.logger.Error("commit refresh family revocation", "err", commitErr)
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to revoke compromised session")
			return
		}
		s.logger.Warn("refresh token reuse detected; revoked session family",
			"family_id", fromPGUUID(tok.FamilyID).String(),
			"user_id", fromPGUUID(tok.UserID).String(),
		)
		s.clearRefreshCookie(w)
		writeError(w, s.logger, http.StatusUnauthorized, "auth.refresh_token_reused", "refresh token was already used; session revoked")
		return
	}

	if !tok.ExpiresAt.Time.After(now) {
		if revErr := txq.RevokeRefreshTokenByHash(r.Context(), db.RevokeRefreshTokenByHashParams{
			TokenHash: digest,
			RevokedAt: pgNow,
		}); revErr == nil {
			_ = tx.Commit(r.Context())
		}
		s.clearRefreshCookie(w)
		writeError(w, s.logger, http.StatusUnauthorized, "auth.refresh_token_expired", "refresh token has expired")
		return
	}

	userRow, err := txq.GetUserByID(r.Context(), tok.UserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusUnauthorized, "auth.invalid_refresh_token", "refresh token missing or invalid")
			return
		}
		s.logger.Error("load user for refresh", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to refresh session")
		return
	}

	famUUID := fromPGUUID(tok.FamilyID)
	userUUID := fromPGUUID(userRow.ID)
	newPlain, newRow, err := s.issueRefreshTokenTx(r.Context(), txq, userUUID, &famUUID)
	if err != nil {
		s.logger.Error("rotate refresh token", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to rotate refresh token")
		return
	}

	if err := txq.ReplaceRefreshToken(r.Context(), db.ReplaceRefreshTokenParams{
		ID:         tok.ID,
		ReplacedBy: newRow.ID,
		RevokedAt:  pgNow,
	}); err != nil {
		s.logger.Error("mark previous refresh token replaced", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to rotate refresh token")
		return
	}

	wsRows, err := txq.ListWorkspacesForUser(r.Context(), userRow.ID)
	if err != nil {
		s.logger.Error("list workspaces during refresh", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load workspaces")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit refresh rotation", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to commit refresh session")
		return
	}

	accessToken, err := s.issueAccessToken(userRow)
	if err != nil {
		s.logger.Error("issue jwt on refresh", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to issue access token")
		return
	}

	wsList := toAPIWorkspaceList(wsRows)
	var activeWS *api.WorkspaceSummary
	if len(wsList) > 0 {
		first := wsList[0]
		activeWS = &first
	}

	s.setRefreshCookie(w, newPlain)
	writeJSON(w, s.logger, http.StatusOK, api.AuthSessionResponse{
		User:            toAPIUser(userRow),
		Workspaces:      wsList,
		ActiveWorkspace: activeWS,
		AccessToken:     accessToken,
	})
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	plainToken, ok := readRefreshToken(r, w, s.logger)
	if !ok {
		return
	}

	if plainToken != "" {
		if !s.ensureDB(w) {
			return
		}
		if err := s.logoutSession(r.Context(), auth.HashToken(plainToken)); err != nil {
			s.logger.Error("revoke refresh session on logout", "err", err)
			writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to revoke session")
			return
		}
	}

	s.clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) logoutSession(ctx context.Context, digest []byte) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackTx(ctx, tx)
	q := s.queries.WithTx(tx)
	family, err := q.LockRefreshTokenFamilyByHash(ctx, digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := q.RevokeRefreshTokenFamily(ctx, db.RevokeRefreshTokenFamilyParams{
		ID: family.ID, RevokedAt: toPGTimestamptz(s.now().UTC()),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *server) handleGetMe(w http.ResponseWriter, r *http.Request) {
	if !s.ensureDB(w) {
		return
	}

	claims, ok := claimsFromContext(r.Context())
	if !ok {
		writeError(w, s.logger, http.StatusUnauthorized, "auth.unauthenticated", "authentication required")
		return
	}

	userRow, err := s.queries.GetUserByID(r.Context(), toPGUUID(claims.Subject))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusUnauthorized, "auth.unauthenticated", "user account no longer exists")
			return
		}
		s.logger.Error("get current user", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load current user")
		return
	}

	wsRows, err := s.queries.ListWorkspacesForUser(r.Context(), userRow.ID)
	if err != nil {
		s.logger.Error("list workspaces for current user", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load workspaces")
		return
	}

	writeJSON(w, s.logger, http.StatusOK, api.MeResponse{
		User:       toAPIUser(userRow),
		Workspaces: toAPIWorkspaceList(wsRows),
	})
}
