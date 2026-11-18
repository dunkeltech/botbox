// SPDX-License-Identifier: AGPL-3.0-only

package migrations

import (
	"context"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

var migrateMu sync.Mutex

// MigrateUp applies all embedded SQL migrations to pool using goose.
func MigrateUp(ctx context.Context, pool *pgxpool.Pool) error {
	migrateMu.Lock()
	defer migrateMu.Unlock()

	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() {
		_ = sqlDB.Close()
	}()

	goose.SetBaseFS(Files)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.UpContext(ctx, sqlDB, "migrations")
}
