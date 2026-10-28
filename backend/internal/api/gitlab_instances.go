package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/authbroker"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secretrequests"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (s *server) handleListGitLabInstances(w http.ResponseWriter, r *http.Request) {
	ws, _, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	rows, err := s.queries.ListGitLabInstances(r.Context(), ws.ID)
	if err != nil {
		s.chatFailure(w)
		return
	}
	out := apigen.GitLabInstanceList{Instances: make([]apigen.GitLabInstance, 0, len(rows))}
	for _, row := range rows {
		out.Instances = append(out.Instances, apigen.GitLabInstance{Name: row.Name, Origin: row.Origin})
	}
	writeJSON(w, s.logger, http.StatusOK, out)
}

func (s *server) handleCreateGitLabInstance(w http.ResponseWriter, r *http.Request) {
	ws, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageGitLabInstances)
	if !ok {
		return
	}
	var in apigen.GitLabInstance
	if !decodeJSON(w, r, s.logger, &in) {
		return
	}
	origin, err := authbroker.StrictLoginOrigin(in.Origin)
	name := strings.TrimSpace(in.Name)
	if err != nil || origin == "https://gitlab.com:443" || len(name) == 0 || len(name) > 100 || strings.ContainsAny(name, "\r\n\x00") {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid GitLab instance name or HTTPS origin")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.chatFailure(w)
		return
	}
	defer rollbackTx(r.Context(), tx)
	q := s.queries.WithTx(tx)
	// Serialize the per-workspace quota with concurrent registration and removal.
	if _, err = q.LockGitLabWorkspace(r.Context(), ws.ID); err != nil {
		s.chatFailure(w)
		return
	}
	row, err := q.CreateGitLabInstance(r.Context(), db.CreateGitLabInstanceParams{WorkspaceID: ws.ID, Origin: origin, Name: name})
	if isUniqueViolation(err) || errors.Is(err, pgx.ErrNoRows) {
		writeError(w, s.logger, http.StatusConflict, "gitlab.instance_conflict", "instance exists or limit reached")
		return
	}
	if err != nil {
		s.chatFailure(w)
		return
	}
	wsID := fromPGUUID(ws.ID)
	actor := claims.Subject
	if err = recordAudit(r.Context(), q, &wsID, &actor, "gitlab_instance.created", "workspace", &wsID, map[string]any{"origin": origin}); err != nil {
		s.chatFailure(w)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.chatFailure(w)
		return
	}
	writeJSON(w, s.logger, http.StatusCreated, apigen.GitLabInstance{Name: row.Name, Origin: row.Origin})
}

func (s *server) handleDeleteGitLabInstance(w http.ResponseWriter, r *http.Request) {
	ws, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageGitLabInstances)
	if !ok {
		return
	}
	var in apigen.GitLabInstanceOrigin
	if !decodeJSON(w, r, s.logger, &in) {
		return
	}
	origin, err := authbroker.StrictLoginOrigin(in.Origin)
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid GitLab origin")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.chatFailure(w)
		return
	}
	defer rollbackTx(r.Context(), tx)
	q := s.queries.WithTx(tx)
	if _, err = q.LockGitLabWorkspace(r.Context(), ws.ID); err != nil {
		s.chatFailure(w)
		return
	}
	assignedBots, err := q.ListGitLabAssignedBots(r.Context(), db.ListGitLabAssignedBotsParams{WorkspaceID: ws.ID, Origin: origin})
	if err != nil {
		s.chatFailure(w)
		return
	}

	n, err := q.DeleteGitLabInstance(r.Context(), db.DeleteGitLabInstanceParams{WorkspaceID: ws.ID, Origin: origin})
	if err != nil {
		s.chatFailure(w)
		return
	}
	if n == 0 {
		writeError(w, s.logger, http.StatusNotFound, "gitlab.instance_not_found", "GitLab instance not found")
		return
	}
	_, host, _, _ := mitmproxy.CanonicalHTTPSOrigin(origin)
	if err = q.DeleteInstanceBotGitLabCredentials(r.Context(), db.DeleteInstanceBotGitLabCredentialsParams{WorkspaceID: ws.ID, DomainMatch: host}); err != nil {
		s.chatFailure(w)
		return
	}
	if err = q.DeleteInstanceWorkspaceGitLabCredentials(r.Context(), db.DeleteInstanceWorkspaceGitLabCredentialsParams{WorkspaceID: ws.ID, DomainMatch: host}); err != nil {
		s.chatFailure(w)
		return
	}
	if err = q.RevokeInstanceGitLabLeases(r.Context(), db.RevokeInstanceGitLabLeasesParams{WorkspaceID: ws.ID, DomainMatch: host}); err != nil {
		s.chatFailure(w)
		return
	}

	sessions, err := q.RevokeGitLabInstanceSessions(r.Context(), db.RevokeGitLabInstanceSessionsParams{WorkspaceID: ws.ID, Origin: origin})
	if err != nil {
		s.chatFailure(w)
		return
	}
	cancelled, err := q.CancelGitLabInstanceRequests(r.Context(), db.CancelGitLabInstanceRequestsParams{WorkspaceID: ws.ID, TargetOrigin: origin})
	if err != nil {
		s.chatFailure(w)
		return
	}
	if err = cancelGitLabCards(r.Context(), q, ws.ID, cancelled); err != nil {
		s.chatFailure(w)
		return
	}
	wsID := fromPGUUID(ws.ID)
	actor := claims.Subject
	if err = recordAudit(r.Context(), q, &wsID, &actor, "gitlab_instance.deleted", "workspace", &wsID, map[string]any{"origin": origin}); err != nil {
		s.chatFailure(w)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.chatFailure(w)
		return
	}
	for _, bot := range assignedBots {
		s.invalidateGitLabHost(r.Context(), wsID, fromPGUUID(bot), host)
	}

	for _, req := range cancelled {
		s.notifySecretContinuation(r.Context(), wsID, fromPGUUID(req.BotID), fromPGUUID(req.ConversationID), fromPGUUID(req.TurnID), "")
	}
	// Durable revocation is authoritative even when the remote broker is unavailable.
	if s.authBroker != nil {
		cleanCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		for _, session := range sessions {
			_ = s.authBroker.RevokeSession(cleanCtx, wsID, fromPGUUID(session))
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func cancelGitLabCards(ctx context.Context, q *db.Queries, wsID pgtype.UUID, requests []db.SecretRequest) error {
	for _, req := range requests {
		card, err := json.Marshal(secretrequests.CardPayload{SecretRequestID: fromPGUUID(req.ID).String(), Kind: req.Kind, Status: req.Status, AdapterID: req.AdapterID, AdapterLabel: gitLabAdapterLabel(req.AdapterID), TargetOrigin: req.TargetOrigin, Reason: req.Reason, ErrorCode: req.ErrorCode})
		if err != nil {
			return err
		}
		if req.MessageID.Valid {
			if err = q.UpdateSecretRequestMessageCard(ctx, db.UpdateSecretRequestMessageCardParams{WorkspaceID: wsID, ID: req.MessageID, Content: req.Reason, Metadata: card}); err != nil {
				return err
			}
		}
		if _, err = q.ClearSecretRequiredPause(ctx, db.ClearSecretRequiredPauseParams{WorkspaceID: wsID, ID: req.ConversationID}); err != nil {
			return err
		}
		if _, err = q.ResumeBotTurnFromSecret(ctx, db.ResumeBotTurnFromSecretParams{WorkspaceID: wsID, ID: req.TurnID, ContinuationPrompt: "GitLab access was removed by a workspace administrator or bot owner. Continue without this credential."}); err != nil {
			return err
		}
	}
	return nil
}

func gitLabAdapterLabel(id string) string {
	if adapter, ok := mitmproxy.LookupAdapter(id); ok {
		return adapter.Label
	}
	return "GitLab Web Sign-In"
}

func (s *server) invalidateGitLabHost(ctx context.Context, workspaceID, botID uuid.UUID, host string) {
	if publisher, ok := s.turnQueue.(interface {
		PublishEgressInvalidate(context.Context, uuid.UUID, uuid.UUID, string) error
	}); ok {
		cleanCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := publisher.PublishEgressInvalidate(cleanCtx, workspaceID, botID, host); err != nil {
			s.logger.Warn("GitLab egress invalidation unavailable", "workspace_id", workspaceID, "bot_id", botID)
		}
	}
}
