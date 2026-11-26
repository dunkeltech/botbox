// SPDX-License-Identifier: AGPL-3.0-only

package migrations_test

import (
	"context"
	"testing"
	"time"

	migrations "github.com/dunkeltech/botbox/backend/db"
	"github.com/dunkeltech/botbox/backend/internal/testutil/pgtest"
	"github.com/google/uuid"
)

func TestMigrationsOnPopulatedDatabase(t *testing.T) {
	pool := pgtest.Open(t)
	ctx := context.Background()

	ws, user, bot := seedWorkspace(t, ctx, pool, "populated-alpha")
	envID := "docker-env-" + uuid.NewString()

	if _, err := pool.Exec(ctx, `
		INSERT INTO environments (bot_id, driver, env_id, status, image, cpu_millis, memory_mib, volume_ref, started_at)
		VALUES ($1, 'docker', $2, 'running', 'botbox/sandbox:dev', 1000, 1024, 'vol-1', $3)
	`, bot, envID, time.Now().UTC().Add(-2*time.Hour)); err != nil {
		t.Fatalf("insert environment: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_log (workspace_id, actor_user_id, action, target_type, target_id, metadata)
		VALUES ($1, $2, 'workspace.created', 'workspace', $1, '{"source":"migration_test"}')
	`, ws, user); err != nil {
		t.Fatalf("insert audit_log: %v", err)
	}

	// Re-running MigrateUp against populated tables must remain idempotent and preserve data.
	if err := migrations.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("re-run MigrateUp on populated database: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM environments WHERE bot_id = $1 AND last_activity_at IS NOT NULL`, bot).Scan(&count); err != nil || count != 1 {
		t.Fatalf("expected 1 populated environment with last_activity_at, got count=%d err=%v", count, err)
	}

	// Verify 00035 and 00036 schema constraints on populated rows.
	if _, err := pool.Exec(ctx, `UPDATE users SET failed_login_attempts = -1 WHERE id = $1`, user); err == nil || !isCode(err, "23514") {
		t.Fatalf("negative failed_login_attempts check err=%v, want 23514", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workspaces SET sandbox_idle_timeout_minutes = -1 WHERE id = $1`, ws); err == nil || !isCode(err, "23514") {
		t.Fatalf("negative sandbox_idle_timeout_minutes check err=%v, want 23514", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workspaces SET sandbox_idle_timeout_minutes = 10081 WHERE id = $1`, ws); err == nil || !isCode(err, "23514") {
		t.Fatalf("excessive sandbox_idle_timeout_minutes check err=%v, want 23514", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workspaces SET sandbox_idle_timeout_minutes = 0 WHERE id = $1`, ws); err != nil {
		t.Fatalf("zero sandbox_idle_timeout_minutes failed: %v", err)
	}
}
