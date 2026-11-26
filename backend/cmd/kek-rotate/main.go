// SPDX-License-Identifier: AGPL-3.0-only

// Command kek-rotate rewraps stored secret DEKs in PostgreSQL under the primary
// KEK configured in BOTBOX_SECRETS_KEK or BOTBOX_SECRETS_KEK_FILE.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/observability"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(observability.NewScrubbingHandler(slog.NewJSONHandler(os.Stdout, nil)))
	dryRun := flag.Bool("dry-run", false, "report envelopes needing rotation without updating the database")
	flag.Parse()

	if err := run(logger, *dryRun); err != nil {
		logger.Error("kek-rotate failed", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger, dryRun bool) error {
	dbURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dbURL == "" {
		return errors.New("DATABASE_URL is required")
	}

	keySpec := strings.TrimSpace(os.Getenv("BOTBOX_SECRETS_KEK"))
	if keySpec == "" {
		kekFile := strings.TrimSpace(os.Getenv("BOTBOX_SECRETS_KEK_FILE"))
		if kekFile == "" {
			return errors.New("BOTBOX_SECRETS_KEK or BOTBOX_SECRETS_KEK_FILE is required")
		}
		data, err := os.ReadFile(kekFile) // #nosec G304 G703 -- operator-configured KEK file path
		if err != nil {
			return err
		}
		keySpec = strings.TrimSpace(string(data))
	}

	ring, err := secrets.ParseKeyRing(keySpec)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	report, err := secrets.RotateDatabaseKEKs(ctx, pool, ring, dryRun)
	if err != nil {
		return err
	}

	logger.Info("kek rotation complete",
		"primary_key_id", report.PrimaryKeyID,
		"dry_run", report.DryRun,
		"workspace_secrets", report.WorkspaceSecrets,
		"bot_secrets", report.BotSecrets,
		"secret_leases", report.SecretLeases,
		"mcp_servers", report.MCPServers,
		"codex_plan_connections", report.CodexPlanConnections,
		"total", report.Total(),
	)
	return nil
}
