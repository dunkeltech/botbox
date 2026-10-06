package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/dunkeltech/botbox/backend/internal/auth"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const csrfHeaderName = "X-BotBox-CSRF"

type contextKey int

const (
	contextKeyClaims contextKey = iota + 1
)

type pgUserStore struct {
	queries *db.Queries
}

func (s *pgUserStore) GetUserByEmail(ctx context.Context, email string) (*auth.StoredUser, error) {
	u, err := s.queries.GetUserByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	return &auth.StoredUser{
		User: auth.User{
			ID:          fromPGUUID(u.ID),
			Email:       u.Email,
			DisplayName: u.DisplayName,
			Locale:      u.Locale,
			CreatedAt:   u.CreatedAt.Time.UTC(),
		},
		PasswordHash: u.PasswordHash,
	}, nil
}

func (s *pgUserStore) UpdatePasswordHash(ctx context.Context, userID uuid.UUID, passwordHash string) error {
	return s.queries.UpdateUserPasswordHash(ctx, db.UpdateUserPasswordHashParams{
		ID:           toPGUUID(userID),
		PasswordHash: passwordHash,
	})
}

func (s *server) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			if r.Header.Get(csrfHeaderName) != "1" {
				writeError(w, s.logger, http.StatusForbidden, "auth.csrf_required", "missing or invalid X-BotBox-CSRF header")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := extractBearerToken(r.Header.Get("Authorization"))
		if !ok || s.authProvider == nil {
			writeError(w, s.logger, http.StatusUnauthorized, "auth.unauthenticated", "authentication required")
			return
		}

		claims, err := s.authProvider.ValidateToken(r.Context(), token)
		if err != nil {
			code := "auth.unauthenticated"
			if errors.Is(err, auth.ErrExpiredToken) {
				code = "auth.token_expired"
			}
			writeError(w, s.logger, http.StatusUnauthorized, code, "invalid or expired access token")
			return
		}

		ctx := context.WithValue(r.Context(), contextKeyClaims, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func claimsFromContext(ctx context.Context) (*auth.Claims, bool) {
	claims, ok := ctx.Value(contextKeyClaims).(*auth.Claims)
	return claims, ok && claims != nil
}

func extractBearerToken(header string) (string, bool) {
	if header == "" {
		return "", false
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	tok := strings.TrimSpace(parts[1])
	return tok, tok != ""
}

// clientIP reads only the socket RemoteAddr so clients cannot spoof X-Forwarded-For
// headers to evade per-IP rate limits.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	if r.RemoteAddr != "" {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return "unknown"
}

func (s *server) loadWorkspaceMembership(w http.ResponseWriter, r *http.Request, action workspaces.Action) (db.GetWorkspaceByIDForUserRow, *auth.Claims, workspaces.Role, bool) {
	var zero db.GetWorkspaceByIDForUserRow
	if !s.ensureDB(w) {
		return zero, nil, "", false
	}

	claims, ok := claimsFromContext(r.Context())
	if !ok {
		writeError(w, s.logger, http.StatusUnauthorized, "auth.unauthenticated", "authentication required")
		return zero, nil, "", false
	}

	wsID, err := uuid.Parse(chi.URLParam(r, "workspaceID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid workspace identifier")
		return zero, nil, "", false
	}

	wsRow, err := s.queries.GetWorkspaceByIDForUser(r.Context(), db.GetWorkspaceByIDForUserParams{
		ID:     toPGUUID(wsID),
		UserID: toPGUUID(claims.Subject),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Return 404 for non-members so callers cannot probe workspace IDs.
			writeError(w, s.logger, http.StatusNotFound, "workspace.not_found", "workspace not found")
			return zero, nil, "", false
		}
		s.logger.Error("load workspace membership", "err", err)
		writeError(w, s.logger, http.StatusInternalServerError, "internal_error", "failed to load workspace")
		return zero, nil, "", false
	}

	role, valid := workspaces.ParseRole(wsRow.Role)
	if !valid {
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "insufficient workspace permissions")
		return zero, nil, "", false
	}

	if action != "" && !workspaces.Can(role, action, false) {
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "insufficient workspace permissions")
		return zero, nil, "", false
	}

	return wsRow, claims, role, true
}
