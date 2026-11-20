// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"errors"

	"github.com/dunkeltech/botbox/backend/internal/bots"
	"github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/jackc/pgx/v5"
)

func missingEnvironmentModelPrice(ctx context.Context, q *db.Queries, bot db.Bot) (*api.MissingModelPrice, error) {
	_, provider := bots.RunnerForProvider(bot.LlmProvider)
	if provider == "" {
		provider = bot.LlmProvider
	}
	_, err := q.GetModelPrice(ctx, db.GetModelPriceParams{WorkspaceID: bot.WorkspaceID, Provider: provider, Model: bot.LlmModel})
	if errors.Is(err, pgx.ErrNoRows) {
		return &api.MissingModelPrice{Provider: provider, Model: bot.LlmModel}, nil
	}
	return nil, err
}
