package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (s *server) budgetBot(w http.ResponseWriter, r *http.Request, ws pgtype.UUID, id *uuid.UUID) (pgtype.UUID, bool) {
	if id == nil {
		return pgtype.UUID{}, true
	}
	bot := toPGUUID(*id)
	_, err := s.queries.GetBotByWorkspaceAndID(r.Context(), db.GetBotByWorkspaceAndIDParams{WorkspaceID: ws, ID: bot})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, s.logger, 404, "bot.not_found", "bot not found")
		return bot, false
	}
	if err != nil {
		s.chatFailure(w)
		return bot, false
	}
	return bot, true
}
func (s *server) handleBudgetSummary(w http.ResponseWriter, r *http.Request) {
	ws, _, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	var requested *uuid.UUID
	if raw := r.URL.Query().Get("botId"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, s.logger, 400, "validation_error", "invalid bot id")
			return
		}
		requested = &id
	}
	bot, ok := s.budgetBot(w, r, ws.ID, requested)
	if !ok {
		return
	}
	asOf := pgtype.Timestamptz{Time: s.now(), Valid: true}
	rows, err := s.queries.ListBudgetState(r.Context(), db.ListBudgetStateParams{WorkspaceID: ws.ID, BotID: bot, AsOf: asOf})
	if err != nil {
		s.chatFailure(w)
		return
	}
	spent, err := s.queries.SpendToday(r.Context(), db.SpendTodayParams{WorkspaceID: ws.ID, AsOf: asOf})
	if err != nil {
		s.chatFailure(w)
		return
	}
	out := apigen.BudgetSummary{SpendTodayMicros: spent, Budgets: make([]apigen.BudgetState, 0, len(rows))}
	for _, b := range rows {
		row := apigen.BudgetState{Id: fromPGUUID(b.ID), Period: apigen.BudgetStatePeriod(b.Period), LimitMicros: b.LimitMicros, SpentMicros: b.SpentMicros, Warning: b.SpentMicros >= b.LimitMicros-b.LimitMicros/5, Exceeded: b.SpentMicros >= b.LimitMicros}
		if b.BotID.Valid {
			id := fromPGUUID(b.BotID)
			row.BotId = &id
		}
		out.Budgets = append(out.Budgets, row)
	}
	writeJSON(w, s.logger, 200, out)
}
func (s *server) handleSetBudget(w http.ResponseWriter, r *http.Request) {
	ws, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageMembers)
	if !ok {
		return
	}
	var req apigen.SetBudgetRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}
	if req.LimitMicros < 1 || req.LimitMicros > 1e12 || (req.Period != "day" && req.Period != "month") || (req.BotId == nil && req.Period != "month") {
		writeError(w, s.logger, 400, "validation_error", "invalid budget")
		return
	}
	bot, ok := s.budgetBot(w, r, ws.ID, req.BotId)
	if !ok {
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.chatFailure(w)
		return
	}
	defer func(ctx context.Context) { _ = tx.Rollback(ctx) }(r.Context())
	q := s.queries.WithTx(tx)
	b, err := q.SetBudget(r.Context(), db.SetBudgetParams{WorkspaceID: ws.ID, BotID: bot, Period: string(req.Period), LimitMicros: req.LimitMicros})
	if err != nil {
		s.chatFailure(w)
		return
	}
	states, err := q.ListBudgetState(r.Context(), db.ListBudgetStateParams{WorkspaceID: ws.ID, BotID: bot, AsOf: pgtype.Timestamptz{Time: s.now(), Valid: true}})
	if err != nil {
		s.chatFailure(w)
		return
	}
	for _, state := range states {
		if state.ID == b.ID && state.SpentMicros < state.LimitMicros {
			if err = q.ResolveBudgetApprovals(r.Context(), db.ResolveBudgetApprovalsParams{WorkspaceID: ws.ID, BudgetID: b.ID, DecidedBy: toPGUUID(claims.Subject)}); err != nil {
				s.chatFailure(w)
				return
			}
		}
	}
	wsID, actor, id := fromPGUUID(ws.ID), claims.Subject, fromPGUUID(b.ID)
	if err = recordAudit(r.Context(), q, &wsID, &actor, "budget.updated", "budget", &id, nil); err != nil {
		s.chatFailure(w)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.chatFailure(w)
		return
	}
	writeJSON(w, s.logger, 200, map[string]bool{"saved": true})
}
func (s *server) handleListModelPrices(w http.ResponseWriter, r *http.Request) {
	ws, _, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	rows, err := s.queries.ListModelPrices(r.Context(), ws.ID)
	if err != nil {
		s.chatFailure(w)
		return
	}
	out := make([]apigen.ModelPrice, 0, len(rows))
	for _, row := range rows {
		out = append(out, apigen.ModelPrice{
			Provider:               row.Provider,
			Model:                  row.Model,
			InputMicrosPerMillion:  row.InputMicrosPerMillion,
			OutputMicrosPerMillion: row.OutputMicrosPerMillion,
			CacheMicrosPerMillion:  row.CacheMicrosPerMillion,
		})
	}
	writeJSON(w, s.logger, http.StatusOK, apigen.ModelPriceListResponse{Prices: out})
}

func (s *server) handleSetModelPrice(w http.ResponseWriter, r *http.Request) {
	ws, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionManageMembers)
	if !ok {
		return
	}
	var req apigen.ModelPriceRequest
	if !decodeJSON(w, r, s.logger, &req) {
		return
	}
	if strings.TrimSpace(req.Provider) == "" || len(req.Provider) > 50 || strings.TrimSpace(req.Model) == "" || len(req.Model) > 100 {
		writeError(w, s.logger, 400, "validation_error", "invalid model price")
		return
	}
	for _, rate := range []int64{req.InputMicrosPerMillion, req.OutputMicrosPerMillion, req.CacheMicrosPerMillion} {
		if rate < 0 || rate > 1e12 {
			writeError(w, s.logger, 400, "validation_error", "invalid model price")
			return
		}
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.chatFailure(w)
		return
	}
	defer func(ctx context.Context) { _ = tx.Rollback(ctx) }(r.Context())
	q := s.queries.WithTx(tx)
	_, err = q.SetModelPrice(r.Context(), db.SetModelPriceParams{WorkspaceID: ws.ID, Provider: req.Provider, Model: req.Model, InputMicrosPerMillion: req.InputMicrosPerMillion, OutputMicrosPerMillion: req.OutputMicrosPerMillion, CacheMicrosPerMillion: req.CacheMicrosPerMillion})
	if err != nil {
		s.chatFailure(w)
		return
	}
	wsID, actor := fromPGUUID(ws.ID), claims.Subject
	if err = recordAudit(r.Context(), q, &wsID, &actor, "model_price.updated", "workspace", &wsID, nil); err != nil {
		s.chatFailure(w)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.chatFailure(w)
		return
	}
	writeJSON(w, s.logger, 200, map[string]bool{"saved": true})
}
