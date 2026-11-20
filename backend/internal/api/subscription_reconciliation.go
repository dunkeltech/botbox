// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"errors"
	"net/http"

	"github.com/dunkeltech/botbox/backend/internal/budgets"
	"github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (s *server) handleListUnresolvedSubscriptionUsage(w http.ResponseWriter, r *http.Request) {
	ws, _, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageMembers)
	if !ok {
		return
	}
	rows, err := s.queries.ListUnresolvedSubscriptionUsage(r.Context(), ws.ID)
	if err != nil {
		s.chatFailure(w)
		return
	}
	out := make([]api.UnresolvedSubscriptionUsage, 0, len(rows))
	for _, row := range rows {
		out = append(out, api.UnresolvedSubscriptionUsage{
			RequestId: fromPGUUID(row.RequestID), BotName: row.BotName,
			Model: row.Model, CreatedAt: row.CreatedAt.Time.UTC(),
		})
	}
	writeJSON(w, s.logger, http.StatusOK, out)
}

func (s *server) handleReconcileSubscriptionUsage(w http.ResponseWriter, r *http.Request) {
	ws, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageMembers)
	if !ok {
		return
	}
	requestID, err := uuid.Parse(chi.URLParam(r, "requestID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid usage request ID")
		return
	}
	var input struct {
		Provider     api.ReconcileSubscriptionUsageRequestProvider `json:"provider"`
		Basis        api.ReconcileSubscriptionUsageRequestBasis    `json:"basis"`
		InputTokens  *int64                                        `json:"inputTokens"`
		OutputTokens *int64                                        `json:"outputTokens"`
		CacheTokens  *int64                                        `json:"cacheTokens"`
	}
	if !decodeJSON(w, r, s.logger, &input) {
		return
	}
	if !input.Provider.Valid() || !input.Basis.Valid() ||
		input.InputTokens == nil || input.OutputTokens == nil || input.CacheTokens == nil ||
		*input.InputTokens < 0 || *input.OutputTokens < 0 || *input.CacheTokens < 0 || *input.CacheTokens > *input.InputTokens {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid subscription usage adjustment")
		return
	}

	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		s.chatFailure(w)
		return
	}
	defer rollbackTx(ctx, tx)
	q := s.queries.WithTx(tx)
	workspaceID := fromPGUUID(ws.ID)
	// Serialize the adjustment with new admissions and proxy settlements.
	if err := q.LockWorkspaceBudgetCheck(ctx, workspaceID.String()); err != nil {
		s.chatFailure(w)
		return
	}
	model, err := q.GetUnresolvedSubscriptionUsage(ctx, db.GetUnresolvedSubscriptionUsageParams{WorkspaceID: ws.ID, RequestID: toPGUUID(requestID)})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, s.logger, http.StatusNotFound, "budget.usage_not_found", "unresolved subscription request not found")
		return
	}
	if err != nil {
		s.chatFailure(w)
		return
	}
	provider := string(input.Provider)
	price, err := q.GetModelPrice(ctx, db.GetModelPriceParams{WorkspaceID: ws.ID, Provider: provider, Model: model})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, s.logger, http.StatusConflict, "budget.model_price_required", "add the workspace model price before reconciling usage")
		return
	}
	if err != nil {
		s.chatFailure(w)
		return
	}
	charge, err := budgets.PriceMicros(*input.InputTokens, *input.OutputTokens, *input.CacheTokens,
		price.InputMicrosPerMillion, price.OutputMicrosPerMillion, price.CacheMicrosPerMillion)
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid token charge")
		return
	}
	count, err := q.ReconcileSubscriptionUsage(ctx, db.ReconcileSubscriptionUsageParams{
		WorkspaceID: ws.ID, RequestID: toPGUUID(requestID), InputTokens: *input.InputTokens,
		OutputTokens: *input.OutputTokens, CacheTokens: *input.CacheTokens,
		InternalChargeMicros: pgtype.Int8{Int64: charge, Valid: true},
	})
	if err != nil {
		s.chatFailure(w)
		return
	}
	if count != 1 {
		writeError(w, s.logger, http.StatusNotFound, "budget.usage_not_found", "unresolved subscription request not found")
		return
	}
	if err := recordAudit(ctx, q, &workspaceID, &claims.Subject, "subscription_usage.reconciled", "usage_request", &requestID, map[string]any{
		"basis": input.Basis, "provider": provider, "model": model, "input_tokens": *input.InputTokens,
		"output_tokens": *input.OutputTokens, "cache_tokens": *input.CacheTokens, "internal_charge_micros": charge,
	}); err != nil {
		s.chatFailure(w)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		s.chatFailure(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
