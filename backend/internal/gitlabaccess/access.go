// SPDX-License-Identifier: AGPL-3.0-only

// Package gitlabaccess authorizes reviewed GitLab targets consistently across CLI, browser and proxy paths.
package gitlabaccess

import (
	"context"
	"errors"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"time"
)

// Public errors expose recovery categories, never database errors or credentials.
var (
	ErrNotRegistered = errors.New("secret_request.gitlab_not_registered")
	ErrNotAssigned   = errors.New("secret_request.gitlab_not_assigned")
	ErrHostBlocked   = errors.New("secret_request.host_not_allowed")
	ErrUnavailable   = errors.New("secret_request.policy_unavailable")
)

// Check requires a registered origin, explicit bot assignment, and independent egress permission.
// With transactional queries the assignment lock remains held until commit or rollback.
func Check(ctx context.Context, q *db.Queries, workspaceID, botID uuid.UUID, origin string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ws, bot := pgtype.UUID{Bytes: workspaceID, Valid: true}, pgtype.UUID{Bytes: botID, Valid: true}
	if _, err := q.GetGitLabInstance(ctx, db.GetGitLabInstanceParams{WorkspaceID: ws, Origin: origin}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotRegistered
		}
		return ErrUnavailable
	}
	if _, err := q.LockBotGitLabInstance(ctx, db.LockBotGitLabInstanceParams{WorkspaceID: ws, BotID: bot, Origin: origin}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotAssigned
		}
		return ErrUnavailable
	}
	_, host, _, err := mitmproxy.CanonicalHTTPSOrigin(origin)
	if err != nil {
		return ErrNotRegistered
	}
	row, err := q.GetEgressRuleByWorkspaceAndBot(ctx, db.GetEgressRuleByWorkspaceAndBotParams{WorkspaceID: ws, BotID: bot})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrHostBlocked
	}
	if err != nil {
		return ErrUnavailable
	}
	allowed, _ := mitmproxy.EvaluateHostAccess(mitmproxy.EgressPolicy(row.Policy), row.AllowedHosts, row.BlockedHosts, host)
	if !allowed {
		return ErrHostBlocked
	}
	return nil
}
