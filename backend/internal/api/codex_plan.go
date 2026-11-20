// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"errors"
	"net/http"

	"github.com/dunkeltech/botbox/backend/internal/codexplan"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
)

func (s *server) handleGetCodexPlan(w http.ResponseWriter, r *http.Request) {
	ws, bot, actor, ok := s.loadWorkspaceBotForSecret(w, r, true)
	if !ok {
		return
	}
	status, owned, secretScope, err := s.codexPlan.Status(r.Context(), ws.ID, bot.ID, toPGUUID(actor))
	if err != nil {
		s.chatFailure(w)
		return
	}
	var scopeOut *apigen.SecretScope
	if secretScope != "" {
		sc := apigen.SecretScope(secretScope)
		scopeOut = &sc
	}
	writeJSON(w, s.logger, http.StatusOK, apigen.CodexPlanStatus{
		Status:      apigen.CodexPlanStatusStatus(status),
		OwnedByYou:  owned,
		SecretScope: scopeOut,
	})
}

func (s *server) handleStartCodexPlan(w http.ResponseWriter, r *http.Request) {
	ws, bot, actor, ok := s.loadWorkspaceBotForSecret(w, r, true)
	if !ok {
		return
	}
	secretScope := codexplan.ScopeBot
	var input apigen.StartCodexPlanRequest
	if !decodeOptionalJSON(w, r, s.logger, &input) {
		return
	}
	if input.SecretScope != nil {
		secretScope = string(*input.SecretScope)
	}
	if !s.codexPairRL.Allow("start:" + actor.String()) {
		writeError(w, s.logger, 429, "rate_limited", "too many pairing requests")
		return
	}
	code, err := s.codexPlan.Start(r.Context(), ws.ID, bot.ID, toPGUUID(actor), secretScope)
	if err != nil {
		s.codexPlanError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, s.logger, http.StatusOK, apigen.CodexPlanPairing{Code: code})
}

func (s *server) handleCompleteCodexPlan(w http.ResponseWriter, r *http.Request) {
	if s.pool == nil {
		s.chatFailure(w)
		return
	}
	if !s.codexPairRL.Allow("complete:" + clientIP(r)) {
		writeError(w, s.logger, 429, "rate_limited", "too many pairing requests")
		return
	}
	var input apigen.CompleteCodexPlanRequest
	if !decodeJSON(w, r, s.logger, &input) {
		return
	}
	pair, ok := codexplan.ParsePairing(input.Pairing)
	if !ok {
		writeError(w, s.logger, 400, "validation_error", "invalid ChatGPT plan pairing")
		return
	}
	grant := codexplan.Grant{Code: input.Code, ClientID: input.ClientId, Verifier: input.Verifier, Nonce: input.Nonce, Redirect: input.Redirect}
	if !grant.Valid() {
		writeError(w, s.logger, 400, "validation_error", "invalid ChatGPT authorization")
		return
	}
	if err := s.codexPlan.Complete(r.Context(), pair, grant); err != nil {
		s.codexPlanError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleDisconnectCodexPlan(w http.ResponseWriter, r *http.Request) {
	ws, bot, _, ok := s.loadWorkspaceBotForSecret(w, r, true)
	if !ok {
		return
	}
	revoked, err := s.codexPlan.Disconnect(r.Context(), ws.ID, bot.ID)
	if err != nil {
		s.chatFailure(w)
		return
	}
	writeJSON(w, s.logger, http.StatusOK, apigen.CodexPlanDisconnect{RevocationConfirmed: revoked})
}

func (s *server) codexPlanError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, codexplan.ErrInvalidScope):
		writeError(w, s.logger, 400, "validation_error", "secretScope must be bot or workspace")
	case errors.Is(err, codexplan.ErrPairing):
		writeError(w, s.logger, 409, "codex_plan.pairing_unavailable", "start a new pairing from the bot settings")
	case errors.Is(err, codexplan.ErrForbidden):
		writeError(w, s.logger, 403, "auth.forbidden", "ChatGPT plan connection is unavailable for this user")
	case errors.Is(err, codexplan.ErrReconnect):
		writeError(w, s.logger, 403, "codex_plan.reconnect", "reconnect with permission to use the ChatGPT plan")
	default:
		writeError(w, s.logger, 503, "codex_plan.unavailable", "ChatGPT plan connection is temporarily unavailable")
	}
}
