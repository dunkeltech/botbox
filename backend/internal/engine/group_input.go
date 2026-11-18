// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/groups"
	"github.com/jackc/pgx/v5/pgtype"
)

// CollectGroupInputs advances ready and expired rounds without relying on a live requester.
// Every ready round is checked again under its conversation lock before publishing.
func (w *Worker) CollectGroupInputs(ctx context.Context) error {
	if w.Pool == nil || w.Enqueue == nil {
		return errors.New("group input worker is not configured")
	}
	q := db.New(w.Pool)
	workspaces, err := q.ListGroupInputWorkspaces(ctx)
	if err != nil {
		return err
	}
	now := pgtype.Timestamptz{Time: w.now(), Valid: true}
	var failures []error
	for _, workspace := range workspaces {
		ready, err := q.ListReadyGroupInputs(ctx, db.ListReadyGroupInputsParams{WorkspaceID: workspace, AsOf: now})
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, item := range ready {
			key := db.GetWaitingGroupInputParams{WorkspaceID: workspace, ConversationID: item.ConversationID, MessageID: item.MessageID}
			if err := w.collectGroupInput(ctx, key); err != nil && !errors.Is(err, groups.ErrQueueFull) {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

func (w *Worker) collectGroupInput(ctx context.Context, key db.GetWaitingGroupInputParams) error {
	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	next, err := groups.CollectInput(ctx, db.New(tx), key, w.now())
	if err != nil {
		return err
	}
	if next != nil {
		if err := w.Enqueue(ctx, next.WorkspaceID, next.BotID, next.TurnID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
