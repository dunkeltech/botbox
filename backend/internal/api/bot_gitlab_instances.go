// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/authbroker"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"github.com/jackc/pgx/v5/pgtype"
)

func (s *server) handleListBotGitLabInstances(w http.ResponseWriter, r *http.Request) {
	ws, bot, _, ok := s.loadWorkspaceBotForSecret(w, r, false)
	if !ok {
		return
	}
	rows, err := s.queries.ListBotGitLabInstances(r.Context(), db.ListBotGitLabInstancesParams{WorkspaceID: ws.ID, BotID: bot.ID})
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

func (s *server) handleSetBotGitLabInstances(w http.ResponseWriter, r *http.Request) {
	ws, bot, actor, ok := s.loadWorkspaceBotForSecret(w, r, true)
	if !ok {
		return
	}
	var in apigen.BotGitLabInstances
	if !decodeJSON(w, r, s.logger, &in) {
		return
	}
	if in.Origins == nil || len(in.Origins) > 20 {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "origins must be an array of at most 20 registered origins")
		return
	}
	desired := map[string]bool{}
	for _, raw := range in.Origins {
		origin, err := authbroker.StrictLoginOrigin(raw)
		if err != nil || desired[origin] {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid or duplicate origin")
			return
		}
		desired[origin] = true
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
	for origin := range desired {
		if _, err = q.GetGitLabInstance(r.Context(), db.GetGitLabInstanceParams{WorkspaceID: ws.ID, Origin: origin}); err != nil {
			writeError(w, s.logger, http.StatusBadRequest, "validation_error", "origin must be registered in this workspace")
			return
		}
	}
	current, err := q.ListBotGitLabInstances(r.Context(), db.ListBotGitLabInstancesParams{WorkspaceID: ws.ID, BotID: bot.ID})
	if err != nil {
		s.chatFailure(w)
		return
	}
	existing := map[string]bool{}
	sessions := []pgtype.UUID{}
	cancelled := []db.SecretRequest{}
	for _, row := range current {
		existing[row.Origin] = true
		if desired[row.Origin] {
			continue
		}
		if err = q.RemoveBotGitLabInstance(r.Context(), db.RemoveBotGitLabInstanceParams{WorkspaceID: ws.ID, BotID: bot.ID, Origin: row.Origin}); err != nil {
			s.chatFailure(w)
			return
		}
		_, host, _, _ := mitmproxy.CanonicalHTTPSOrigin(row.Origin)
		if err = q.BlockGitLabWorkspaceSecretsForBot(r.Context(), db.BlockGitLabWorkspaceSecretsForBotParams{WorkspaceID: ws.ID, BotID: bot.ID, DomainMatch: host}); err != nil {
			s.chatFailure(w)
			return
		}
		if err = q.DeleteBotGitLabCredentials(r.Context(), db.DeleteBotGitLabCredentialsParams{WorkspaceID: ws.ID, BotID: bot.ID, DomainMatch: host}); err != nil {
			s.chatFailure(w)
			return
		}
		if err = q.RevokeBotGitLabLeases(r.Context(), db.RevokeBotGitLabLeasesParams{WorkspaceID: ws.ID, BotID: bot.ID, DomainMatch: host}); err != nil {
			s.chatFailure(w)
			return
		}

		revoked, revokeErr := q.RevokeBotGitLabInstanceSessions(r.Context(), db.RevokeBotGitLabInstanceSessionsParams{WorkspaceID: ws.ID, BotID: bot.ID, Origin: row.Origin})
		if revokeErr != nil {
			s.chatFailure(w)
			return
		}
		sessions = append(sessions, revoked...)
		requests, cancelErr := q.CancelBotGitLabInstanceRequests(r.Context(), db.CancelBotGitLabInstanceRequestsParams{WorkspaceID: ws.ID, BotID: bot.ID, TargetOrigin: row.Origin})
		if cancelErr != nil {
			s.chatFailure(w)
			return
		}
		cancelled = append(cancelled, requests...)
	}
	for origin := range desired {
		if !existing[origin] {
			if err = q.AssignBotGitLabInstance(r.Context(), db.AssignBotGitLabInstanceParams{WorkspaceID: ws.ID, BotID: bot.ID, Origin: origin}); err != nil {
				s.chatFailure(w)
				return
			}
		}
	}
	if err = cancelGitLabCards(r.Context(), q, ws.ID, cancelled); err != nil {
		s.chatFailure(w)
		return
	}
	wsID, botID := fromPGUUID(ws.ID), fromPGUUID(bot.ID)
	if err = recordAudit(r.Context(), q, &wsID, &actor, "bot.gitlab_instances_updated", "bot", &botID, map[string]any{"origins": in.Origins}); err != nil {
		s.chatFailure(w)
		return
	}
	rows, err := q.ListBotGitLabInstances(r.Context(), db.ListBotGitLabInstancesParams{WorkspaceID: ws.ID, BotID: bot.ID})
	if err != nil {
		s.chatFailure(w)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.chatFailure(w)
		return
	}
	for _, row := range current {
		if !desired[row.Origin] {
			_, host, _, _ := mitmproxy.CanonicalHTTPSOrigin(row.Origin)
			s.invalidateGitLabHost(r.Context(), wsID, botID, host)
		}
	}

	for _, req := range cancelled {
		s.notifySecretContinuation(r.Context(), wsID, botID, fromPGUUID(req.ConversationID), fromPGUUID(req.TurnID), "")
	}
	if s.authBroker != nil {
		cleanCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		for _, session := range sessions {
			_ = s.authBroker.RevokeSession(cleanCtx, wsID, fromPGUUID(session))
		}
	}
	out := apigen.GitLabInstanceList{Instances: make([]apigen.GitLabInstance, 0, len(rows))}
	for _, row := range rows {
		out.Instances = append(out.Instances, apigen.GitLabInstance{Name: row.Name, Origin: row.Origin})
	}
	writeJSON(w, s.logger, http.StatusOK, out)
}
