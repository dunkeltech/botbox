// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/dunkeltech/botbox/backend/internal/auth"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type invitationAcceptance struct {
	user         db.User
	workspaces   []db.ListWorkspacesForUserRow
	workspaceID  uuid.UUID
	accessToken  string
	refreshToken string
}

type invitationAcceptError struct {
	code    string
	message string
	cause   error
}

func (e *invitationAcceptError) Error() string { return e.message }
func (e *invitationAcceptError) Unwrap() error { return e.cause }

func invitationDBError(operation, message string, err error) *invitationAcceptError {
	return &invitationAcceptError{
		code:    "internal_error",
		message: message,
		cause:   fmt.Errorf("%s: %w", operation, err),
	}
}

func (s *server) acceptInvitation(
	ctx context.Context,
	workspaceID pgtype.UUID,
	digest []byte,
	existingUser *uuid.UUID,
	signup db.CreateUserParams,
) (invitationAcceptance, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return invitationAcceptance{}, invitationDBError("begin accept invitation tx", "failed to accept invitation", err)
	}
	defer rollbackTx(ctx, tx)

	txq := s.queries.WithTx(tx)
	if _, err := txq.GetWorkspaceForUpdate(ctx, workspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return invitationAcceptance{}, &invitationAcceptError{code: "workspace.not_found", message: "workspace not found"}
		}
		return invitationAcceptance{}, invitationDBError("lock workspace memberships", "failed to lock workspace memberships", err)
	}
	inv, err := txq.GetInvitationByTokenHashForUpdate(ctx, digest)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return invitationAcceptance{}, &invitationAcceptError{code: "invitation.not_found", message: "invitation not found or expired"}
		}
		return invitationAcceptance{}, invitationDBError("lock invitation on accept", "failed to accept invitation", err)
	}

	now := s.now().UTC()
	if inv.AcceptedAt.Valid || !inv.ExpiresAt.Time.After(now) {
		return invitationAcceptance{}, &invitationAcceptError{code: "invitation.not_found", message: "invitation not found or expired"}
	}

	var userRow db.User
	if existingUser != nil {
		userRow, err = txq.GetUserByID(ctx, toPGUUID(*existingUser))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return invitationAcceptance{}, &invitationAcceptError{code: "auth.unauthenticated", message: "user account not found"}
			}
			return invitationAcceptance{}, invitationDBError("load signed-in user on invitation accept", "failed to load user", err)
		}
		if inv.Email.Valid && auth.NormalizeEmail(userRow.Email) != inv.Email.String {
			return invitationAcceptance{}, &invitationAcceptError{code: "invitation.email_mismatch", message: "signed-in account email does not match invitation"}
		}
		if _, memberErr := txq.GetWorkspaceMember(ctx, db.GetWorkspaceMemberParams{
			WorkspaceID: inv.WorkspaceID,
			UserID:      userRow.ID,
		}); memberErr == nil {
			return invitationAcceptance{}, &invitationAcceptError{code: "workspace.already_member", message: "user is already a member of this workspace"}
		} else if !errors.Is(memberErr, pgx.ErrNoRows) {
			return invitationAcceptance{}, invitationDBError("check existing membership on accept", "failed to check workspace membership", memberErr)
		}
	} else {
		userRow, err = txq.CreateUser(ctx, signup)
		if err != nil {
			if isUniqueViolation(err) {
				return invitationAcceptance{}, &invitationAcceptError{code: "auth.account_exists", message: "an account with this email already exists; sign in first to accept this invitation"}
			}
			return invitationAcceptance{}, invitationDBError("create user on invitation accept", "failed to create user account", err)
		}
	}

	if _, err := txq.AddWorkspaceMember(ctx, db.AddWorkspaceMemberParams{
		WorkspaceID: inv.WorkspaceID,
		UserID:      userRow.ID,
		Role:        inv.Role,
	}); err != nil {
		if isUniqueViolation(err) {
			return invitationAcceptance{}, &invitationAcceptError{code: "workspace.already_member", message: "user is already a member of this workspace"}
		}
		return invitationAcceptance{}, invitationDBError("add workspace member on accept", "failed to add workspace member", err)
	}

	if _, err := txq.MarkInvitationAccepted(ctx, db.MarkInvitationAcceptedParams{
		ID:         inv.ID,
		AcceptedAt: toPGTimestamptz(now),
	}); err != nil {
		return invitationAcceptance{}, invitationDBError("mark invitation accepted", "failed to finalize invitation", err)
	}

	// Accepting an invitation with an access token must preserve that session's
	// expiry and refresh family. Only signup authenticates a new session here.
	var accessToken, plainRefresh string
	if existingUser == nil {
		plainRefresh, _, err = s.issueRefreshTokenTx(ctx, txq, fromPGUUID(userRow.ID), nil)
		if err != nil {
			return invitationAcceptance{}, invitationDBError("issue refresh token on signup", "failed to create session", err)
		}
		accessToken, err = s.issueAccessToken(userRow)
		if err != nil {
			return invitationAcceptance{}, invitationDBError("issue access token on signup", "failed to issue access token", err)
		}
	}

	wsRows, err := txq.ListWorkspacesForUser(ctx, userRow.ID)
	if err != nil {
		return invitationAcceptance{}, invitationDBError("list workspaces after accept", "failed to load workspaces", err)
	}

	wsUUID := fromPGUUID(inv.WorkspaceID)
	userUUID := fromPGUUID(userRow.ID)
	invUUID := fromPGUUID(inv.ID)
	if err := recordAudit(ctx, txq, &wsUUID, &userUUID, "invitation.accepted", "invitation", &invUUID, map[string]any{
		"role": inv.Role,
	}); err != nil {
		return invitationAcceptance{}, invitationDBError("audit accept invitation", "failed to record audit log", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return invitationAcceptance{}, invitationDBError("commit accept invitation", "failed to accept invitation", err)
	}

	return invitationAcceptance{
		user: userRow, workspaces: wsRows, workspaceID: wsUUID,
		accessToken: accessToken, refreshToken: plainRefresh,
	}, nil
}
