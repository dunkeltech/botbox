// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"

	"github.com/dunkeltech/botbox/backend/internal/engine"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// recordMeteredFixture substitutes for proxy settlement in tests of chat and
// group behavior. The token and monetary accounting path has its own tests.
func recordMeteredFixture(ctx context.Context, h *testHarness, s engine.Session, cost int64, known bool) error {
	return h.queries.RecordTurnUsage(ctx, db.RecordTurnUsageParams{
		WorkspaceID: pgtype.UUID{Bytes: s.WorkspaceID, Valid: true},
		BotID:       pgtype.UUID{Bytes: s.BotID, Valid: true},
		TurnID:      pgtype.UUID{Bytes: s.TurnID, Valid: true},
		RequestID:   pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Source:      "proxy_meter",
		BillingKind: "metered",
		Model:       s.Bot.LlmModel,
		CostMicros:  pgtype.Int8{Int64: cost, Valid: known},
		CreatedAt:   pgtype.Timestamptz{Time: h.clock.Now(), Valid: true},
	})
}
