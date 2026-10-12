package budgets

import (
	"context"
	"errors"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// Check blocks exhausted limits and persists one pending approval per budget.
// The caller must serialize requests and usage settlement for the workspace.
func Check(ctx context.Context, q *db.Queries, workspaceID, botID uuid.UUID, now time.Time) error {
	ws := pgtype.UUID{Bytes: workspaceID, Valid: true}
	bot := pgtype.UUID{Bytes: botID, Valid: true}
	rows, err := q.ListBudgetState(ctx, db.ListBudgetStateParams{WorkspaceID: ws, BotID: bot, AsOf: pgtype.Timestamptz{Time: now, Valid: true}})
	if err != nil {
		return err
	}
	for _, b := range rows {
		unknown, err := q.HasUnpricedAPIUsage(ctx, db.HasUnpricedAPIUsageParams{WorkspaceID: ws, BotID: b.BotID, Period: b.Period, AsOf: pgtype.Timestamptz{Time: now, Valid: true}})
		if err != nil {
			return err
		}
		if unknown {
			return errors.New("usage reconciliation required")
		}

		if b.SpentMicros >= b.LimitMicros {
			if err = q.CreateBudgetApproval(ctx, db.CreateBudgetApprovalParams{WorkspaceID: ws, BotID: bot, BudgetID: b.ID}); err != nil {
				return err
			}
			return llmproxy.ErrBudgetExceeded
		}
	}
	return nil
}
