// SPDX-License-Identifier: AGPL-3.0-only

// Package pgtest launches isolated ephemeral PostgreSQL instances for backend integration tests.
package pgtest

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	migrations "github.com/dunkeltech/botbox/backend/db"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Open starts an isolated ephemeral PostgreSQL cluster in a temporary directory,
// runs all embedded SQL migrations, and registers cleanup to stop the server.
func Open(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return open(t, migrations.MigrateUp)
}

// OpenAt migrates an isolated cluster to the given version for upgrade tests.
func OpenAt(t *testing.T, version int64) *pgxpool.Pool {
	t.Helper()
	return open(t, func(ctx context.Context, pool *pgxpool.Pool) error {
		files, err := fs.Sub(migrations.Files, "migrations")
		if err != nil {
			return err
		}
		sqlDB := stdlib.OpenDBFromPool(pool)
		defer func() { _ = sqlDB.Close() }()
		provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, files)
		if err != nil {
			return err
		}
		_, err = provider.UpTo(ctx, version)
		return err
	})
}

func open(t *testing.T, migrate func(context.Context, *pgxpool.Pool) error) *pgxpool.Pool {
	t.Helper()

	if _, err := exec.LookPath("initdb"); err != nil {
		t.Fatalf("initdb not found on PATH (run inside devenv shell): %v", err)
	}
	if _, err := exec.LookPath("pg_ctl"); err != nil {
		t.Fatalf("pg_ctl not found on PATH (run inside devenv shell): %v", err)
	}

	port, err := reserveLocalPort()
	if err != nil {
		t.Fatalf("reserve tcp port: %v", err)
	}

	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	logFile := filepath.Join(tmpDir, "postgres.log")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// #nosec G204 -- Test helper invokes local initdb with an internally created temp directory.
	initCmd := exec.CommandContext(ctx,
		"initdb",
		"-D", dataDir,
		"-U", "postgres",
		"-A", "trust",
		"--encoding=UTF8",
		"--locale=C",
		"--no-sync",
	)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("initdb failed: %v\n%s", err, out)
	}

	pgOpts := fmt.Sprintf("-F -c listen_addresses=127.0.0.1 -c port=%d -c unix_socket_directories= -c fsync=off -c full_page_writes=off", port)
	// #nosec G204 -- Test helper launches local pg_ctl inside an isolated temp directory on 127.0.0.1.
	startCmd := exec.CommandContext(ctx,
		"pg_ctl",
		"-D", dataDir,
		"-l", logFile,
		"-w",
		"-o", pgOpts,
		"start",
	)
	if out, err := startCmd.CombinedOutput(); err != nil {
		// #nosec G304 -- Reads the ephemeral postgres log file created inside t.TempDir().
		logBytes, _ := os.ReadFile(logFile)
		t.Fatalf("pg_ctl start failed: %v\noutput: %s\nlog: %s", err, out, logBytes)
	}

	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		// #nosec G204 -- Test cleanup stops the ephemeral local postgres cluster started above.
		stopCmd := exec.CommandContext(stopCtx, "pg_ctl", "-D", dataDir, "-m", "immediate", "-w", "stop")
		_ = stopCmd.Run()
	})

	dsn := fmt.Sprintf("postgres://postgres@127.0.0.1:%d/postgres?sslmode=disable", port)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("postgres ping: %v", err)
	}
	if err := migrate(ctx, pool); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	return pool
}

func reserveLocalPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = ln.Close()
	}()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected listener addr type %T", ln.Addr())
	}
	return addr.Port, nil
}
