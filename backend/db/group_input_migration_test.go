// SPDX-License-Identifier: AGPL-3.0-only

package migrations_test

import (
	"context"
	"errors"
	"testing"

	migrations "github.com/dunkeltech/botbox/backend/db"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/testutil/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestGroupInputUpgradeRepairsEarlyVersion22(t *testing.T) {
	for _, tc := range []struct {
		name   string
		legacy bool
	}{{"early version 22", true}, {"version 22 with required input", false}} {
		t.Run(tc.name, func(t *testing.T) {
			checkGroupInputUpgrade(t, tc.legacy)
		})
	}
}

func checkGroupInputUpgrade(t *testing.T, legacy bool) {
	t.Helper()
	pool := pgtest.OpenAt(t, 22)
	ctx := context.Background()
	ws, user, bot := seedWorkspace(t, ctx, pool, "upgrade")
	group := insertGroup(t, ctx, pool, ws, bot)
	addMember(t, ctx, pool, ws, group, "bot", bot)
	message := insertMessage(t, ctx, pool, ws, group, user)
	var turn uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO bot_turns(workspace_id, bot_id, conversation_id, message_id)
		VALUES ($1, $2, $3, $4) RETURNING id`, ws, bot, group, message).Scan(&turn); err != nil {
		t.Fatal(err)
	}

	pgID := func(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }
	query := db.New(pool)
	claim := db.GroupInputTurnAllowedParams{WorkspaceID: pgID(ws), ConversationID: pgID(group), TurnID: pgID(turn)}
	if legacy {
		// The earlier version 22 supported advisory requests only.
		if _, err := pool.Exec(ctx, `ALTER TABLE group_input_requests DROP COLUMN required, DROP COLUMN resume_turn_id`); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO group_input_requests (workspace_id, conversation_id, message_id, requester_bot_id, deadline)
			VALUES ($1, $2, $3, $4, now())`, ws, group, message, bot); err != nil {
			t.Fatal(err)
		}
		if _, err := query.GroupInputTurnAllowed(ctx, claim); !isCode(err, "42703") {
			t.Fatalf("legacy schema claim: %v", err)
		}
	} else if _, err := pool.Exec(ctx, `INSERT INTO group_input_requests
		(workspace_id, conversation_id, message_id, requester_bot_id, deadline, required)
		VALUES ($1, $2, $3, $4, now(), true)`, ws, group, message, bot); err != nil {
		t.Fatal(err)
	}
	if err := migrations.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := query.GroupInputTurnAllowed(ctx, claim); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("ordinary turn claim after upgrade: %v", err)
	}
	row, err := query.GetWaitingGroupInput(ctx, db.GetWaitingGroupInputParams{
		WorkspaceID: pgID(ws), ConversationID: pgID(group), MessageID: pgID(message),
	})
	if err != nil || row.Required != !legacy || row.ResumeTurnID.Valid {
		t.Fatalf("existing request after upgrade: %+v, %v", row, err)
	}
	queued, err := query.GetBotTurn(ctx, db.GetBotTurnParams{WorkspaceID: pgID(ws), ID: pgID(turn)})
	if err != nil || queued.Status != "queued" || queued.StopRequested {
		t.Fatalf("existing turn after upgrade: %s, %v", queued.Status, err)
	}
}
