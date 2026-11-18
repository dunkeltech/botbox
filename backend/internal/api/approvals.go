// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/dunkeltech/botbox/backend/internal/approvals"
	"github.com/dunkeltech/botbox/backend/internal/egresspolicy"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (s *server) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	ws, claims, role, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	var statusText pgtype.Text
	switch status {
	case "":
	case "pending", "approved", "denied":
		statusText = pgtype.Text{String: status, Valid: true}
	default:
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid approval status")
		return
	}
	rows, err := s.queries.ListVisibleApprovals(r.Context(), db.ListVisibleApprovalsParams{
		WorkspaceID:             ws.ID,
		Status:                  statusText,
		MemberID:                toPGUUID(claims.Subject),
		IncludeWorkspaceBudgets: role == workspaces.RoleOwner || role == workspaces.RoleAdmin,
	})
	if err != nil {
		s.logger.Error("list approvals", "err", err)
		s.chatFailure(w)
		return
	}
	out := apigen.ApprovalList{Approvals: make([]apigen.Approval, 0, len(rows))}
	for _, row := range rows {
		out.Approvals = append(out.Approvals, presentApproval(row))
	}
	writeJSON(w, s.logger, http.StatusOK, out)
}

func (s *server) handleDecideApproval(w http.ResponseWriter, r *http.Request) {
	ws, claims, role, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	approvalID, err := uuid.Parse(chi.URLParam(r, "approvalID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid approval identifier")
		return
	}
	var req apigen.ApprovalDecisionRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}
	if !req.Decision.Valid() {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "decision must be approved or denied")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin approval decision", "err", err)
		s.chatFailure(w)
		return
	}
	defer rollbackTx(r.Context(), tx)
	q := s.queries.WithTx(tx)
	row, err := q.GetApprovalForUpdate(r.Context(), db.GetApprovalForUpdateParams{WorkspaceID: ws.ID, ID: toPGUUID(approvalID)})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, s.logger, http.StatusNotFound, "approval.not_found", "approval not found")
		return
	}
	if err != nil {
		s.logger.Error("load approval", "err", err)
		s.chatFailure(w)
		return
	}
	if !s.canDecideApproval(w, r, q, role, claims.Subject, row) {
		return
	}
	var applyOpts approvals.ApplyOptions
	if row.Kind == "new_host" {
		botRow, botErr := q.GetBotByWorkspaceAndID(r.Context(), db.GetBotByWorkspaceAndIDParams{
			WorkspaceID: ws.ID,
			ID:          row.BotID,
		})
		if botErr != nil && !errors.Is(botErr, pgx.ErrNoRows) {
			s.logger.Error("load approval bot", "err", botErr)
			s.chatFailure(w)
			return
		}
		isBotOwner := botErr == nil && botRow.OwnerUserID.Valid && fromPGUUID(botRow.OwnerUserID) == claims.Subject
		applyOpts.CanManageBot = workspaces.Can(role, workspaces.ActionManageBotSecrets, isBotOwner)
	}
	turn, err := approvals.Apply(r.Context(), q, row, string(req.Decision), toPGUUID(claims.Subject), s.now(), applyOpts)
	var wildcardBlocked egresspolicy.WildcardBlockedError
	var valErr egresspolicy.ValidationError
	switch {
	case errors.Is(err, approvals.ErrConflict):
		writeError(w, s.logger, http.StatusConflict, "approval.conflict", "approval was already decided")
		return
	case errors.Is(err, egresspolicy.ErrForbiddenBlockRemoval):
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "bot management permission required to remove explicit block")
		return
	case errors.As(err, &wildcardBlocked):
		writeError(w, s.logger, http.StatusConflict, "approval.wildcard_blocked", fmt.Sprintf("host is blocked by wildcard rule %s; edit that rule in bot egress policy", wildcardBlocked.Pattern))
		return
	case errors.Is(err, approvals.ErrHostNotBlocked):
		writeError(w, s.logger, http.StatusConflict, "approval.host_not_blocked", "host is already allowed")
		return
	case errors.As(err, &valErr):
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", valErr.Message)
		return
	case errors.Is(err, approvals.ErrCapNotRaised):
		writeError(w, s.logger, http.StatusConflict, "approval.cap_not_raised", "budget cap is not above spend")
		return
	case errors.Is(err, approvals.ErrQueueFull):
		writeError(w, s.logger, http.StatusTooManyRequests, "chat.queue_full", "bot queue is full")
		return
	case errors.Is(err, approvals.ErrInvalid):
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "approval cannot be decided")
		return
	case err != nil:
		s.logger.Error("decide approval", "err", err)
		s.chatFailure(w)
		return
	}
	if turn != nil {
		if s.turnQueue == nil {
			s.chatFailure(w)
			return
		}
		if err = s.turnQueue.Enqueue(r.Context(), turn.WorkspaceID, turn.BotID, turn.TurnID); err != nil {
			s.logger.Error("enqueue resumed turn", "err", err)
			s.chatFailure(w)
			return
		}
	}
	var approvedRevision int64
	var notifyEgress bool
	if row.Kind == "new_host" && req.Decision == apigen.ApprovalDecisionRequestDecisionApproved {
		if ruleRow, rErr := q.GetEgressRuleByWorkspaceAndBot(r.Context(), db.GetEgressRuleByWorkspaceAndBotParams{
			WorkspaceID: ws.ID,
			BotID:       row.BotID,
		}); rErr == nil {
			approvedRevision = ruleRow.Revision
			notifyEgress = true
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit approval decision", "err", err)
		s.chatFailure(w)
		return
	}
	if notifyEgress {
		_, _ = s.awaitEgressPolicyAck(r.Context(), fromPGUUID(ws.ID), fromPGUUID(row.BotID), approvedRevision)
	}
	w.WriteHeader(http.StatusNoContent)
}

// canDecideApproval allows a workspace owner or admin, or a non-viewer member
// of the group. A workspace budget approval has no group, so only an owner or
// admin can decide it. Anyone else gets 404, including a member of another group.
func (s *server) canDecideApproval(w http.ResponseWriter, r *http.Request, q *db.Queries, role workspaces.Role, actor uuid.UUID, row db.Approval) bool {
	if !row.ConversationID.Valid {
		if role == workspaces.RoleOwner || role == workspaces.RoleAdmin {
			return true
		}
		writeError(w, s.logger, http.StatusNotFound, "approval.not_found", "approval not found")
		return false
	}
	convType, err := q.GetConversationType(r.Context(), db.GetConversationTypeParams{
		WorkspaceID: row.WorkspaceID,
		ID:          row.ConversationID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, s.logger, http.StatusNotFound, "approval.not_found", "approval not found")
		return false
	}
	if err != nil {
		s.logger.Error("load approval conversation type", "err", err)
		s.chatFailure(w)
		return false
	}
	if convType == "dm" {
		_, dmErr := q.GetPrivateConversation(r.Context(), db.GetPrivateConversationParams{
			WorkspaceID: row.WorkspaceID,
			ID:          row.ConversationID,
			UserID:      toPGUUID(actor),
		})
		if errors.Is(dmErr, pgx.ErrNoRows) {
			writeError(w, s.logger, http.StatusNotFound, "approval.not_found", "approval not found")
			return false
		}
		if dmErr != nil {
			s.logger.Error("load approval dm", "err", dmErr)
			s.chatFailure(w)
			return false
		}
		if role == workspaces.RoleViewer {
			writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "viewers cannot decide an approval")
			return false
		}
		return true
	}
	if role == workspaces.RoleOwner || role == workspaces.RoleAdmin {
		return true
	}
	_, err = q.GetGroupForMember(r.Context(), db.GetGroupForMemberParams{
		WorkspaceID: row.WorkspaceID,
		ID:          row.ConversationID,
		MemberID:    toPGUUID(actor),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, s.logger, http.StatusNotFound, "approval.not_found", "approval not found")
		return false
	}
	if err != nil {
		s.logger.Error("load approval group", "err", err)
		s.chatFailure(w)
		return false
	}
	if role == workspaces.RoleViewer {
		writeError(w, s.logger, http.StatusForbidden, "auth.forbidden", "viewers cannot decide an approval")
		return false
	}
	return true
}

// presentApproval exposes the public card fields. triggerMessageId stays in the
// database so a resumed turn can find its text, and it is not an API field.
func presentApproval(row db.Approval) apigen.Approval {
	out := apigen.Approval{
		Id:        fromPGUUID(row.ID),
		BotId:     fromPGUUID(row.BotID),
		Kind:      apigen.ApprovalKind(row.Kind),
		Status:    apigen.ApprovalStatus(row.Status),
		CreatedAt: row.CreatedAt.Time,
	}
	if row.ConversationID.Valid {
		id := fromPGUUID(row.ConversationID)
		out.ConversationId = &id
	}
	if row.BudgetID.Valid {
		id := fromPGUUID(row.BudgetID)
		out.BudgetId = &id
	}
	if row.MessageID.Valid {
		id := fromPGUUID(row.MessageID)
		out.MessageId = &id
	}
	if row.DecidedAt.Valid {
		decided := row.DecidedAt.Time
		out.DecidedAt = &decided
	}
	var payload struct {
		Host   string `json:"host"`
		Branch string `json:"branch"`
		Label  string `json:"label"`
	}
	if err := json.Unmarshal(row.Payload, &payload); err == nil {
		if payload.Host != "" {
			out.Host = &payload.Host
		}
		if payload.Branch != "" {
			out.Branch = &payload.Branch
		}
		if payload.Label != "" {
			out.Label = &payload.Label
		}
	}
	return out
}
