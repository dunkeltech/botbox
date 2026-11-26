// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

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
	now     func() time.Time
}

func (s *pgUserStore) GetUserByEmail(ctx context.Context, email string) (*auth.StoredUser, error) {
	u, err := s.queries.GetUserByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	// The local provider verifies its dummy hash for both locked and unknown users.
	if u.LockedUntil.Valid && u.LockedUntil.Time.After(s.now()) {
		return nil, auth.ErrInvalidCredentials
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

// ParseTrustedProxyCIDRs parses a comma-separated list of IP addresses or CIDR
// prefixes whose X-Forwarded-For headers may be trusted.
func ParseTrustedProxyCIDRs(raw string) ([]netip.Prefix, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	parts := strings.Split(trimmed, ",")
	out := make([]netip.Prefix, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		if strings.Contains(item, "/") {
			pfx, err := netip.ParsePrefix(item)
			if err != nil {
				return nil, fmt.Errorf("invalid trusted proxy CIDR %q: %w", item, err)
			}
			out = append(out, pfx.Masked())
			continue
		}
		addr, err := netip.ParseAddr(item)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy IP %q: %w", item, err)
		}
		addr = addr.Unmap()
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
}

func isTrustedProxyAddr(addr netip.Addr, trusted []netip.Prefix) bool {
	if !addr.IsValid() || len(trusted) == 0 {
		return false
	}
	unmapped := addr.Unmap()
	for _, pfx := range trusted {
		if pfx.Contains(unmapped) {
			return true
		}
	}
	return false
}

// clientIP returns the socket peer IP unless the peer belongs to s.trustedProxies,
// in which case the rightmost untrusted X-Forwarded-For entry is returned.
func (s *server) clientIP(r *http.Request) string {
	peer := socketPeerIP(r)
	if s == nil || len(s.trustedProxies) == 0 {
		return peer
	}
	peerAddr, err := netip.ParseAddr(peer)
	if err != nil || !isTrustedProxyAddr(peerAddr, s.trustedProxies) {
		return peer
	}
	xffValues := r.Header.Values("X-Forwarded-For")
	if len(xffValues) == 0 {
		return peer
	}
	var hops []string
	for _, hdr := range xffValues {
		for _, part := range strings.Split(hdr, ",") {
			if clean := strings.TrimSpace(part); clean != "" {
				hops = append(hops, clean)
			}
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		addr, parseErr := netip.ParseAddr(hops[i])
		if parseErr != nil {
			return peer
		}
		unmapped := addr.Unmap()
		if !isTrustedProxyAddr(unmapped, s.trustedProxies) {
			return unmapped.String()
		}
	}
	return peer
}

func socketPeerIP(r *http.Request) string {
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
	return s.workspaceMembership(w, r, s.queries, action)
}

func (s *server) workspaceMembership(w http.ResponseWriter, r *http.Request, q *db.Queries, action workspaces.Action) (db.GetWorkspaceByIDForUserRow, *auth.Claims, workspaces.Role, bool) {
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

	wsRow, err := q.GetWorkspaceByIDForUser(r.Context(), db.GetWorkspaceByIDForUserParams{
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
