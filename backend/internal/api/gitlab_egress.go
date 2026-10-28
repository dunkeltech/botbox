package api

import (
	"context"
	"strings"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"github.com/jackc/pgx/v5/pgtype"
)

type gitlabEgressAccess struct {
	assigned       map[string]bool
	blockedSecrets map[pgtype.UUID]bool
	publicHosts    map[string]bool
}

func loadGitLabEgressAccess(ctx context.Context, q *db.Queries, ws, bot pgtype.UUID) (gitlabEgressAccess, error) {
	access := gitlabEgressAccess{assigned: map[string]bool{}, blockedSecrets: map[pgtype.UUID]bool{}, publicHosts: map[string]bool{}}
	instances, err := q.ListKnownGitLabOrigins(ctx, ws)
	if err != nil {
		return access, err
	}
	for _, instance := range instances {
		_, host, _, parseErr := mitmproxy.CanonicalHTTPSOrigin(instance)
		if parseErr == nil {
			access.publicHosts[host] = true
		}
	}
	assignments, err := q.ListBotGitLabInstances(ctx, db.ListBotGitLabInstancesParams{WorkspaceID: ws, BotID: bot})
	if err != nil {
		return access, err
	}
	for _, assignment := range assignments {
		access.assigned[assignment.Origin] = true
	}
	blocked, err := q.ListBlockedGitLabWorkspaceSecretsForBot(ctx, db.ListBlockedGitLabWorkspaceSecretsForBotParams{WorkspaceID: ws, BotID: bot})
	if err != nil {
		return access, err
	}
	for _, id := range blocked {
		access.blockedSecrets[id] = true
	}
	return access, nil
}

func (a gitlabEgressAccess) bind(rule *mitmproxy.SecretRule) bool {
	if !mitmproxy.IsGitLabAdapter(rule.AdapterID) {
		return true
	}
	origin := "https://" + strings.TrimSpace(rule.DomainMatch) + ":443"
	adapter, err := mitmproxy.ResolveAdapter(rule.AdapterID, origin)
	if err != nil || rule.MatchSubdomains {
		return false
	}
	if adapter.Host != "gitlab.com" {
		a.publicHosts[adapter.Host] = true
		if !a.assigned[adapter.CanonicalOrigin()] {
			return false
		}
	}
	// Persisted template fields never override a reviewed adapter's authentication behavior.
	rule.HeaderName = adapter.HeaderName
	rule.Scheme = adapter.Scheme
	rule.Username = adapter.FixedUsername
	rule.Port = adapter.Port
	return true
}
