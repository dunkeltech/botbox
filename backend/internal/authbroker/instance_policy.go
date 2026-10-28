package authbroker

import (
	"context"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/gitlabaccess"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// GitLabPolicy authorizes custom origins at session creation and before every operation.
// A nonzero sessionID also requires a live durable session, preventing resurrection.
type GitLabPolicy func(ctx context.Context, workspaceID, botID, sessionID uuid.UUID, origin string) error

// ValidateGitLabAccess requires both a workspace registration and the bot egress policy.
func ValidateGitLabAccess(ctx context.Context, q *db.Queries, workspaceID, botID, sessionID uuid.UUID, origin string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ws := pgtype.UUID{Bytes: workspaceID, Valid: true}
	bot := pgtype.UUID{Bytes: botID, Valid: true}
	if gitlabaccess.Check(ctx, q, workspaceID, botID, origin) != nil {
		return ErrNavigationForbidden
	}
	if sessionID != uuid.Nil {
		row, err := q.GetBrokerSession(ctx, db.GetBrokerSessionParams{WorkspaceID: ws, ID: pgtype.UUID{Bytes: sessionID, Valid: true}})
		if err != nil || row.BotID != bot || row.Origin != origin || row.AdapterID != "gitlab_web" || row.RevokedAt.Valid || row.Status == StatusRevoked {
			return ErrNavigationForbidden
		}
	}
	return nil
}

// SetGitLabPolicy enables custom GitLab origins with a mandatory live authorization check.
// Configure once before serving requests. Without a policy custom origins fail closed.
func (s *Service) SetGitLabPolicy(policy GitLabPolicy) { s.gitlabPolicy = policy }

func isCustomGitLab(a LoginAdapter) bool {
	return a.ID == "gitlab_web" && a.Origin != reviewedLoginAdapters["gitlab_web"].Origin
}
