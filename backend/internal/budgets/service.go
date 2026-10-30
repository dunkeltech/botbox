package budgets

import (
	"context"
	"errors"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/notify"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Check blocks exhausted limits and persists one pending approval per budget.
// The caller must hold the short workspace admission lock. Reservations use
// capacity but are not billed spend and must not create budget approvals.
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
			approvalID, err := q.CreateBudgetApproval(ctx, db.CreateBudgetApprovalParams{WorkspaceID: ws, BotID: bot, BudgetID: b.ID})
			if errors.Is(err, pgx.ErrNoRows) {
				return llmproxy.ErrBudgetExceeded
			}
			if err != nil {
				return err
			}
			// Only the insert that created the card notifies. A later exhausted
			// check hits the conflict and must not add another row.
			if _, err = notify.WorkspaceApproval(ctx, q, ws, approvalID); err != nil {
				return err
			}
			return llmproxy.ErrBudgetExceeded
		}
		if b.ReservedMicros >= b.LimitMicros-b.SpentMicros {
			return llmproxy.ErrBudgetExceeded
		}
	}
	return nil
}
