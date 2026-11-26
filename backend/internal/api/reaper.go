// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Runtime exposes internal background maintenance operations backed by an API
// server instance without enlarging the public HTTP surface.
type Runtime struct {
	srv *server
}

// ReapIdleEnvironments stops running sandboxes whose last recorded activity
// exceeds the effective workspace or server idle timeout and returns the
// number of stopped sandboxes.
func (rt *Runtime) ReapIdleEnvironments(ctx context.Context) (int, error) {
	if rt == nil || rt.srv == nil {
		return 0, nil
	}
	return rt.srv.reapIdleEnvironments(ctx)
}

// ReconcileEnvironments reconciles persisted environment states against the
// backing compute driver.
func (rt *Runtime) ReconcileEnvironments(ctx context.Context) error {
	if rt == nil || rt.srv == nil {
		return nil
	}
	return ReconcileEnvironments(ctx, rt.srv.pool, rt.srv.compute, rt.srv.logger)
}

func (s *server) reapIdleEnvironments(ctx context.Context) (int, error) {
	if s.pool == nil || s.queries == nil || s.compute == nil {
		return 0, nil
	}

	candidates, err := s.queries.ListIdleRunningEnvironments(ctx)
	if err != nil {
		return 0, err
	}

	now := s.now().UTC()
	stopped := 0
	for _, cand := range candidates {
		effectiveTimeout := s.defaultSandboxIdleTimeout
		if cand.SandboxIdleTimeoutMinutes.Valid {
			effectiveTimeout = time.Duration(cand.SandboxIdleTimeoutMinutes.Int32) * time.Minute
		}
		if effectiveTimeout <= 0 {
			continue
		}

		botID := fromPGUUID(cand.BotID)
		if s.hasActiveWatchers(botID) {
			continue
		}

		lastActivity := cand.LastActivityAt.Time
		if !cand.LastActivityAt.Valid && cand.StartedAt.Valid {
			lastActivity = cand.StartedAt.Time
		}
		if !cand.LastActivityAt.Valid && !cand.StartedAt.Valid {
			continue
		}
		if now.Sub(lastActivity) < effectiveTimeout {
			continue
		}

		reaped, reapErr := s.reapSingleIdleEnvironment(ctx, cand.WorkspaceID, cand.BotID)
		if reapErr != nil {
			if s.logger != nil {
				s.logger.Warn("idle sandbox auto-stop failed",
					slog.String("bot_id", botID.String()),
					slog.Any("error", reapErr),
				)
			}
			continue
		}
		if reaped {
			stopped++
		}
	}
	return stopped, nil
}

func (s *server) reapSingleIdleEnvironment(ctx context.Context, wsPG, botPG pgtype.UUID) (bool, error) {
	botID := fromPGUUID(botPG)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackTx(ctx, tx)

	txq := s.queries.WithTx(tx)
	botRow, err := txq.LockBotByWorkspaceAndID(ctx, db.LockBotByWorkspaceAndIDParams{
		WorkspaceID: wsPG,
		ID:          botPG,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Keep turn creation and claim serialized with the idle decision.
	if err := txq.LockBotQueue(ctx, fromPGUUID(wsPG).String()+"/"+botID.String()); err != nil {
		return false, err
	}

	if s.hasActiveWatchers(botID) {
		return false, nil
	}

	// Recheck on the write so a competing control claim or activity update wins
	// if it obtains the environment row first.
	stopped, err := txq.MarkIdleEnvironmentStopped(ctx, db.MarkIdleEnvironmentStoppedParams{
		WorkspaceID:    wsPG,
		BotID:          botPG,
		AsOf:           toPGTimestamptz(s.now()),
		DefaultTimeout: pgtype.Interval{Microseconds: s.defaultSandboxIdleTimeout.Microseconds(), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := finishEnvironmentStop(ctx, txq, s.compute, botRow, stopped.EnvID, nil, "environment.auto_stopped_idle"); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
