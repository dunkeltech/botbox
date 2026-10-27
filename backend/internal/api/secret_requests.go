package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/authbroker"
	"github.com/dunkeltech/botbox/backend/internal/compute/vncrelay"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secretrequests"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *server) handleFulfillSecretRequest(w http.ResponseWriter, r *http.Request) {
	if !s.ensureDB(w) {
		return
	}
	ws, claims, role, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	reqID, err := uuid.Parse(chi.URLParam(r, "requestID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid secret request identifier")
		return
	}

	var body apigen.FulfillSecretRequestBody
	if !decodeJSON(w, r, s.logger, &body) {
		return
	}
	// Never log request body or secret material on any branch.

	row, bot, ok := s.loadVisibleSecretRequest(w, r, fromPGUUID(ws.ID), reqID, role, claims.Subject)
	if !ok {
		return
	}

	isBotOwner := bot.OwnerUserID.Valid && fromPGUUID(bot.OwnerUserID) == claims.Subject
	canReplaceBot := workspaces.Can(role, workspaces.ActionManageBotSecrets, isBotOwner)
	canWorkspace := role == workspaces.RoleOwner || role == workspaces.RoleAdmin

	username := ""
	if body.Username != nil {
		username = *body.Username
	}
	replaceExisting := false
	if body.ReplaceExisting != nil {
		replaceExisting = *body.ReplaceExisting
	}

	wsID := fromPGUUID(ws.ID)
	res, err := secretrequests.FulfillRequest(r.Context(), s.pool, s.keyRing, secretrequests.FulfillInput{
		WorkspaceID:     wsID,
		RequestID:       reqID,
		ActorUserID:     claims.Subject,
		SecretValue:     body.SecretValue,
		Username:        username,
		Scope:           string(body.Scope),
		ReplaceExisting: replaceExisting,
		CanReplaceBot:   canReplaceBot,
		CanWorkspace:    canWorkspace,
		Broker:          s.authBroker,
		Now:             s.now(),
	})
	if err != nil {
		s.writeSecretRequestError(w, err)
		return
	}

	_ = recordAudit(r.Context(), s.queries, &wsID, &claims.Subject, "secret_request.fulfilled", "secret_request", &reqID, map[string]any{
		"adapter_id":   res.Request.AdapterID,
		"scope":        string(body.Scope),
		"bot_id":       fromPGUUID(row.BotID).String(),
		"requires_mfa": res.RequiresMFA,
	})

	if !res.RequiresMFA {
		s.notifySecretContinuation(r.Context(), wsID, fromPGUUID(res.Request.BotID), fromPGUUID(res.Request.ConversationID), fromPGUUID(res.Request.TurnID), res.TargetHost)
	}
	out := apigen.SecretRequestResult{
		SecretRequest: presentSecretCard(res.Card),
	}
	if res.RequiresMFA {
		mfa := true
		out.MfaRequired = &mfa
	}
	writeJSON(w, s.logger, http.StatusOK, out)
}

func (s *server) handleDenySecretRequest(w http.ResponseWriter, r *http.Request) {
	if !s.ensureDB(w) {
		return
	}
	ws, claims, role, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	reqID, err := uuid.Parse(chi.URLParam(r, "requestID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid secret request identifier")
		return
	}

	row, _, ok := s.loadVisibleSecretRequest(w, r, fromPGUUID(ws.ID), reqID, role, claims.Subject)
	if !ok {
		return
	}

	wsID := fromPGUUID(ws.ID)
	res, err := secretrequests.DenyRequest(r.Context(), s.pool, secretrequests.DenyInput{
		WorkspaceID: wsID,
		RequestID:   reqID,
		ActorUserID: claims.Subject,
		Broker:      s.authBroker,
		Now:         s.now(),
	})
	if err != nil {
		s.writeSecretRequestError(w, err)
		return
	}

	_ = recordAudit(r.Context(), s.queries, &wsID, &claims.Subject, "secret_request.denied", "secret_request", &reqID, map[string]any{
		"adapter_id": res.Request.AdapterID,
		"bot_id":     fromPGUUID(row.BotID).String(),
	})

	s.notifySecretContinuation(r.Context(), wsID, fromPGUUID(res.Request.BotID), fromPGUUID(res.Request.ConversationID), fromPGUUID(res.Request.TurnID), "")
	writeJSON(w, s.logger, http.StatusOK, apigen.SecretRequestResult{
		SecretRequest: presentSecretCard(res.Card),
	})
}

func (s *server) loadVisibleSecretRequest(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID, requestID uuid.UUID,
	role workspaces.Role,
	actor uuid.UUID,
) (db.SecretRequest, db.Bot, bool) {
	wsPG := toPGUUID(workspaceID)
	row, err := s.queries.GetSecretRequest(r.Context(), db.GetSecretRequestParams{
		WorkspaceID: wsPG,
		ID:          toPGUUID(requestID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, s.logger, http.StatusNotFound, "secret_request.not_found", "secret request not found")
		return db.SecretRequest{}, db.Bot{}, false
	}
	if err != nil {
		s.chatFailure(w)
		return db.SecretRequest{}, db.Bot{}, false
	}

	// Enforce strict conversation membership before role checks so non-members
	// (including workspace admins outside private DMs or groups) receive 404.
	convType, err := s.queries.GetConversationType(r.Context(), db.GetConversationTypeParams{
		WorkspaceID: wsPG,
		ID:          row.ConversationID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, s.logger, http.StatusNotFound, "secret_request.not_found", "secret request not found")
		return db.SecretRequest{}, db.Bot{}, false
	}
	if err != nil {
		s.chatFailure(w)
		return db.SecretRequest{}, db.Bot{}, false
	}
	if convType == "dm" {
		_, dmErr := s.queries.GetPrivateConversation(r.Context(), db.GetPrivateConversationParams{
			WorkspaceID: wsPG,
			ID:          row.ConversationID,
			UserID:      toPGUUID(actor),
		})
		if errors.Is(dmErr, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusNotFound, "secret_request.not_found", "secret request not found")
			return db.SecretRequest{}, db.Bot{}, false
		}
		if dmErr != nil {
			s.chatFailure(w)
			return db.SecretRequest{}, db.Bot{}, false
		}
	} else {
		_, grpErr := s.queries.GetGroupForMember(r.Context(), db.GetGroupForMemberParams{
			WorkspaceID: wsPG,
			ID:          row.ConversationID,
			MemberID:    toPGUUID(actor),
		})
		if errors.Is(grpErr, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusNotFound, "secret_request.not_found", "secret request not found")
			return db.SecretRequest{}, db.Bot{}, false
		}
		if grpErr != nil {
			s.chatFailure(w)
			return db.SecretRequest{}, db.Bot{}, false
		}
	}

	if !workspaces.Can(role, workspaces.ActionFulfillSecretRequest, false) {
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "viewers cannot fulfill or deny secret requests")
		return db.SecretRequest{}, db.Bot{}, false
	}

	bot, err := s.queries.GetBotByWorkspaceAndID(r.Context(), db.GetBotByWorkspaceAndIDParams{
		WorkspaceID: wsPG,
		ID:          row.BotID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, s.logger, http.StatusNotFound, "secret_request.not_found", "secret request not found")
		return db.SecretRequest{}, db.Bot{}, false
	}
	if err != nil {
		s.chatFailure(w)
		return db.SecretRequest{}, db.Bot{}, false
	}
	return row, bot, true
}

func (s *server) writeSecretRequestError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, secretrequests.ErrUnsupportedCapability):
		writeError(w, s.logger, http.StatusUnprocessableEntity, "secret_request.unsupported_capability", "browser login requires an isolated authentication broker which is not yet enabled")
	case errors.Is(err, secretrequests.ErrInvalidInput):
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid secret request payload")
	case errors.Is(err, secretrequests.ErrForbidden):
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "insufficient role for requested secret scope")
	case errors.Is(err, secretrequests.ErrNotFound):
		writeError(w, s.logger, http.StatusNotFound, "secret_request.not_found", "secret request not found")
	case errors.Is(err, secretrequests.ErrSecretRuleConflict):
		writeError(w, s.logger, http.StatusConflict, "secret_request.rule_conflict", "an existing secret rule already exists for this adapter")
	case errors.Is(err, secretrequests.ErrHostNotAllowed):
		writeError(w, s.logger, http.StatusConflict, "secret_request.host_not_allowed", "destination host must be approved on the bot egress allowlist first")
	case errors.Is(err, secretrequests.ErrConflict):
		writeError(w, s.logger, http.StatusConflict, "secret_request.conflict", "secret request is no longer pending")
	default:
		s.logger.Error("secret request failure", "err", err)
		s.chatFailure(w)
	}
}

func (s *server) notifySecretContinuation(ctx context.Context, workspaceID, botID, conversationID, turnID uuid.UUID, host string) {
	if host != "" {
		if inv, ok := s.turnQueue.(interface {
			PublishEgressInvalidate(context.Context, uuid.UUID, uuid.UUID, string) error
		}); ok {
			if err := inv.PublishEgressInvalidate(ctx, workspaceID, botID, host); err != nil {
				s.logger.Warn("egress invalidation publish failed", "err", err)
			}
		}
	}
	if s.turnQueue != nil {
		if err := s.turnQueue.Enqueue(ctx, workspaceID, botID, turnID); err != nil {
			s.logger.Error("enqueue secret continuation turn failed", "turn_id", turnID.String(), "err", err)
		}
	}
	if pub, ok := s.chatEvents.(interface {
		PublishConversation(context.Context, uuid.UUID, uuid.UUID, []byte) error
	}); ok {
		payload, _ := json.Marshal(map[string]string{"Kind": "status", "Status": "thinking"})
		_ = pub.PublishConversation(ctx, workspaceID, conversationID, payload)
	}
}

func presentSecretCard(c secretrequests.CardPayload) apigen.SecretRequestSummary {
	reqID, _ := uuid.Parse(c.SecretRequestID)
	out := apigen.SecretRequestSummary{
		SecretRequestId: reqID,
		Kind:            apigen.SecretRequestKind(c.Kind),
		Status:          apigen.SecretRequestStatus(c.Status),
		AdapterId:       c.AdapterID,
		TargetOrigin:    c.TargetOrigin,
		Reason:          c.Reason,
	}
	if c.AdapterLabel != "" {
		v := c.AdapterLabel
		out.AdapterLabel = &v
	}
	if c.HeaderScheme != "" {
		v := c.HeaderScheme
		out.HeaderScheme = &v
	}
	if c.EnvPlaceholderName != "" {
		v := c.EnvPlaceholderName
		out.EnvPlaceholderName = &v
	}
	if c.PlaceholderValue != "" {
		v := c.PlaceholderValue
		out.PlaceholderValue = &v
	}
	if c.GrantedScope != "" {
		v := c.GrantedScope
		out.GrantedScope = &v
	}
	if c.ExpiresAt != "" {
		v := c.ExpiresAt
		out.ExpiresAt = &v
	}
	if c.ErrorCode != "" {
		v := c.ErrorCode
		out.ErrorCode = &v
	}
	if c.BrokerEnabled {
		v := true
		out.BrokerEnabled = &v
	}
	if c.BrokerSessionID != "" {
		if sid, err := uuid.Parse(c.BrokerSessionID); err == nil {
			out.BrokerSessionId = &sid
		}
	}
	if c.TakeoverActive {
		v := true
		out.TakeoverActive = &v
	}
	return out
}

const brokerTakeoverTicketTTL = 10 * time.Minute

func brokerTicketScopeID(sessionID uuid.UUID) string {
	return "broker:" + sessionID.String()
}

func (s *server) verifyTakeoverRequestTicket(r *http.Request, wsID, sessID, actorID uuid.UUID, requireTicket bool) bool {
	if s.ticketSigner == nil {
		return true
	}
	rawTicket := strings.TrimSpace(r.URL.Query().Get("ticket"))
	if rawTicket == "" {
		rawTicket = strings.TrimSpace(r.Header.Get("X-Broker-Takeover-Ticket"))
	}
	if rawTicket == "" {
		return !requireTicket
	}
	claims, err := s.ticketSigner.Verify(rawTicket, wsID.String(), brokerTicketScopeID(sessID), s.now())
	if err != nil {
		return false
	}
	return claims.CanControl && claims.UserID == actorID.String()
}

func (s *server) handleStartBrokerTakeover(w http.ResponseWriter, r *http.Request) {
	if !s.ensureDB(w) {
		return
	}
	ws, claims, role, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	reqID, err := uuid.Parse(chi.URLParam(r, "requestID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid secret request identifier")
		return
	}
	wsID := fromPGUUID(ws.ID)
	row, _, ok := s.loadVisibleSecretRequest(w, r, wsID, reqID, role, claims.Subject)
	if !ok {
		return
	}

	state, err := secretrequests.StartBrokerTakeover(r.Context(), s.pool, s.authBroker, secretrequests.BrokerTakeoverInput{
		WorkspaceID: wsID,
		RequestID:   reqID,
		ActorUserID: claims.Subject,
		Now:         s.now(),
	})
	if err != nil {
		s.writeSecretRequestError(w, err)
		return
	}

	_ = recordAudit(r.Context(), s.queries, &wsID, &claims.Subject, "broker_takeover.started", "secret_request", &reqID, map[string]any{
		"bot_id":            fromPGUUID(row.BotID).String(),
		"broker_session_id": state.Session.SessionID.String(),
	})

	var ticketPtr *string
	if s.ticketSigner != nil {
		if signed, sErr := s.ticketSigner.Sign(vncrelay.TicketClaims{
			WorkspaceID: wsID.String(),
			BotID:       brokerTicketScopeID(state.Session.SessionID),
			UserID:      claims.Subject.String(),
			CanControl:  true,
			ExpiresAt:   s.now().Add(brokerTakeoverTicketTTL),
		}); sErr == nil {
			ticketPtr = &signed
		}
	}

	writeJSON(w, s.logger, http.StatusOK, presentTakeoverState(state.Card, state.Session, ticketPtr))
}

func (s *server) handleBrokerTakeoverFrame(w http.ResponseWriter, r *http.Request) {
	if !s.ensureDB(w) {
		return
	}
	if s.authBroker == nil {
		s.writeSecretRequestError(w, secretrequests.ErrUnsupportedCapability)
		return
	}
	ws, claims, role, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	reqID, err := uuid.Parse(chi.URLParam(r, "requestID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid secret request identifier")
		return
	}
	wsID := fromPGUUID(ws.ID)
	row, _, ok := s.loadVisibleSecretRequest(w, r, wsID, reqID, role, claims.Subject)
	if !ok {
		return
	}
	if row.Kind != secretrequests.KindBrowserLogin || !row.BrokerSessionID.Valid {
		s.writeSecretRequestError(w, secretrequests.ErrUnsupportedCapability)
		return
	}
	sessID := uuid.UUID(row.BrokerSessionID.Bytes)
	if !s.verifyTakeoverRequestTicket(r, wsID, sessID, claims.Subject, true) {
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "invalid broker takeover ticket")
		return
	}

	pngBytes, _, err := s.authBroker.TakeoverFrame(r.Context(), wsID, sessID, claims.Subject)
	if err != nil {
		s.writeBrokerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pngBytes)
}

func (s *server) handleBrokerTakeoverInput(w http.ResponseWriter, r *http.Request) {
	if !s.ensureDB(w) {
		return
	}
	if s.authBroker == nil {
		s.writeSecretRequestError(w, secretrequests.ErrUnsupportedCapability)
		return
	}
	ws, claims, role, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	reqID, err := uuid.Parse(chi.URLParam(r, "requestID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid secret request identifier")
		return
	}
	var body apigen.BrokerTakeoverInputBody
	if !decodeJSON(w, r, s.logger, &body) {
		return
	}
	wsID := fromPGUUID(ws.ID)
	row, _, ok := s.loadVisibleSecretRequest(w, r, wsID, reqID, role, claims.Subject)
	if !ok {
		return
	}
	if row.Kind != secretrequests.KindBrowserLogin || !row.BrokerSessionID.Valid {
		s.writeSecretRequestError(w, secretrequests.ErrUnsupportedCapability)
		return
	}
	sessID := uuid.UUID(row.BrokerSessionID.Bytes)
	if !s.verifyTakeoverRequestTicket(r, wsID, sessID, claims.Subject, false) {
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "invalid broker takeover ticket")
		return
	}

	input := authbroker.TakeoverInput{Kind: string(body.Kind)}
	if body.X != nil {
		input.X = *body.X
	}
	if body.Y != nil {
		input.Y = *body.Y
	}
	if body.Text != nil {
		input.Text = *body.Text
	}
	if body.Key != nil {
		input.Key = *body.Key
	}

	sessInfo, err := s.authBroker.TakeoverInput(r.Context(), wsID, sessID, claims.Subject, input)
	if err != nil {
		s.writeBrokerError(w, err)
		return
	}
	card := secretrequests.CardPayload{
		SecretRequestID: uuid.UUID(row.ID.Bytes).String(),
		Kind:            row.Kind,
		Status:          row.Status,
		AdapterID:       row.AdapterID,
		TargetOrigin:    row.TargetOrigin,
		Reason:          row.Reason,
		BrokerEnabled:   true,
		BrokerSessionID: sessID.String(),
		TakeoverActive:  sessInfo.TakeoverActive,
	}
	writeJSON(w, s.logger, http.StatusOK, presentTakeoverState(card, sessInfo, nil))
}

func (s *server) handleCompleteBrokerTakeover(w http.ResponseWriter, r *http.Request) {
	if !s.ensureDB(w) {
		return
	}
	ws, claims, role, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	reqID, err := uuid.Parse(chi.URLParam(r, "requestID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid secret request identifier")
		return
	}
	wsID := fromPGUUID(ws.ID)
	row, _, ok := s.loadVisibleSecretRequest(w, r, wsID, reqID, role, claims.Subject)
	if !ok {
		return
	}
	if row.BrokerSessionID.Valid && !s.verifyTakeoverRequestTicket(r, wsID, uuid.UUID(row.BrokerSessionID.Bytes), claims.Subject, false) {
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "invalid broker takeover ticket")
		return
	}

	res, err := secretrequests.CompleteBrokerTakeover(r.Context(), s.pool, s.authBroker, secretrequests.BrokerTakeoverInput{
		WorkspaceID: wsID,
		RequestID:   reqID,
		ActorUserID: claims.Subject,
		Now:         s.now(),
	})
	if err != nil {
		s.writeSecretRequestError(w, err)
		return
	}

	_ = recordAudit(r.Context(), s.queries, &wsID, &claims.Subject, "broker_takeover.completed", "secret_request", &reqID, map[string]any{
		"bot_id": fromPGUUID(row.BotID).String(),
	})

	s.notifySecretContinuation(r.Context(), wsID, fromPGUUID(res.Request.BotID), fromPGUUID(res.Request.ConversationID), fromPGUUID(res.Request.TurnID), "")
	writeJSON(w, s.logger, http.StatusOK, apigen.SecretRequestResult{
		SecretRequest: presentSecretCard(res.Card),
	})
}

func (s *server) writeBrokerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, authbroker.ErrTakeoverForbidden):
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "takeover belongs to another user")
	case errors.Is(err, authbroker.ErrSessionNotFound):
		writeError(w, s.logger, http.StatusNotFound, "secret_request.not_found", "broker session not found")
	case errors.Is(err, authbroker.ErrTakeoverNotActive), errors.Is(err, authbroker.ErrSessionExpired):
		writeError(w, s.logger, http.StatusConflict, "secret_request.conflict", "broker takeover is not active")
	case errors.Is(err, authbroker.ErrActionForbidden):
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid takeover input")
	default:
		s.logger.Error("broker takeover error", "err", err)
		s.chatFailure(w)
	}
}

func presentTakeoverState(card secretrequests.CardPayload, sess authbroker.SessionInfo, ticket *string) apigen.BrokerTakeoverState {
	pageURL := sess.Binding.PageURL
	capSup := sess.CaptureSuppressed
	return apigen.BrokerTakeoverState{
		SecretRequest:     presentSecretCard(card),
		BrokerSessionId:   sess.SessionID,
		Origin:            sess.Origin,
		PageUrl:           &pageURL,
		TakeoverActive:    sess.TakeoverActive,
		CaptureSuppressed: &capSup,
		TakeoverTicket:    ticket,
	}
}
