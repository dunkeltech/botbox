// SPDX-License-Identifier: AGPL-3.0-only

package secrets

import (
	"context"
	"errors"
	"fmt"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RotationReport summarizes how many envelope DEKs were rewrapped (or would be
// rewrapped in dry-run mode) per encrypted table.
type RotationReport struct {
	PrimaryKeyID         string `json:"primaryKeyId"`
	DryRun               bool   `json:"dryRun"`
	WorkspaceSecrets     int    `json:"workspaceSecrets"`
	BotSecrets           int    `json:"botSecrets"`
	SecretLeases         int    `json:"secretLeases"`
	MCPServers           int    `json:"mcpServers"`
	CodexPlanConnections int    `json:"codexPlanConnections"`
}

// Total returns the total number of rotated (or pending) secret envelopes.
func (r RotationReport) Total() int {
	return r.WorkspaceSecrets + r.BotSecrets + r.SecretLeases + r.MCPServers + r.CodexPlanConnections
}

// RotateDatabaseKEKs rewraps all stored envelope DEKs whose kek_id differs from
// ring.PrimaryID() inside a single database transaction without decrypting
// plaintext secret payloads.
func RotateDatabaseKEKs(ctx context.Context, pool *pgxpool.Pool, ring *KeyRing, dryRun bool) (RotationReport, error) {
	if pool == nil {
		return RotationReport{}, errors.New("database pool is required")
	}
	if ring == nil {
		return RotationReport{}, errors.New("secrets key ring is required")
	}

	primaryID := ring.PrimaryID()
	report := RotationReport{
		PrimaryKeyID: primaryID,
		DryRun:       dryRun,
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return RotationReport{}, fmt.Errorf("begin KEK rotation tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	q := db.New(tx)

	wsRows, err := q.ListWorkspaceSecretsForKEKRotation(ctx, primaryID)
	if err != nil {
		return RotationReport{}, fmt.Errorf("list workspace secrets: %w", err)
	}
	for _, row := range wsRows {
		aad := SecretAAD(uuid.UUID(row.WorkspaceID.Bytes), "workspace", uuid.UUID(row.ID.Bytes))
		rewrapped, rErr := ring.RewrapDataKey(EnvelopeCiphertext{
			KeyID:      row.KekID,
			WrappedDEK: row.WrappedDek,
			Ciphertext: row.EncryptedValue,
		}, aad)
		if rErr != nil {
			return RotationReport{}, fmt.Errorf("rewrap workspace secret %s: %w", uuid.UUID(row.ID.Bytes), rErr)
		}
		if !dryRun {
			if uErr := q.UpdateWorkspaceSecretWrappedDEK(ctx, db.UpdateWorkspaceSecretWrappedDEKParams{
				WorkspaceID: row.WorkspaceID,
				ID:          row.ID,
				KekID:       rewrapped.KeyID,
				WrappedDek:  rewrapped.WrappedDEK,
			}); uErr != nil {
				return RotationReport{}, fmt.Errorf("update workspace secret %s: %w", uuid.UUID(row.ID.Bytes), uErr)
			}
		}
		report.WorkspaceSecrets++
	}

	botRows, err := q.ListBotSecretsForKEKRotation(ctx, primaryID)
	if err != nil {
		return RotationReport{}, fmt.Errorf("list bot secrets: %w", err)
	}
	for _, row := range botRows {
		aad := SecretAAD(uuid.UUID(row.WorkspaceID.Bytes), "bot", uuid.UUID(row.ID.Bytes))
		rewrapped, rErr := ring.RewrapDataKey(EnvelopeCiphertext{
			KeyID:      row.KekID,
			WrappedDEK: row.WrappedDek,
			Ciphertext: row.EncryptedValue,
		}, aad)
		if rErr != nil {
			return RotationReport{}, fmt.Errorf("rewrap bot secret %s: %w", uuid.UUID(row.ID.Bytes), rErr)
		}
		if !dryRun {
			if uErr := q.UpdateBotSecretWrappedDEK(ctx, db.UpdateBotSecretWrappedDEKParams{
				WorkspaceID: row.WorkspaceID,
				ID:          row.ID,
				KekID:       rewrapped.KeyID,
				WrappedDek:  rewrapped.WrappedDEK,
			}); uErr != nil {
				return RotationReport{}, fmt.Errorf("update bot secret %s: %w", uuid.UUID(row.ID.Bytes), uErr)
			}
		}
		report.BotSecrets++
	}

	leaseRows, err := q.ListSecretLeasesForKEKRotation(ctx, primaryID)
	if err != nil {
		return RotationReport{}, fmt.Errorf("list secret leases: %w", err)
	}
	for _, row := range leaseRows {
		aad := SecretAAD(uuid.UUID(row.WorkspaceID.Bytes), "lease", uuid.UUID(row.ID.Bytes))
		rewrapped, rErr := ring.RewrapDataKey(EnvelopeCiphertext{
			KeyID:      row.KekID,
			WrappedDEK: row.WrappedDek,
			Ciphertext: row.EncryptedValue,
		}, aad)
		if rErr != nil {
			return RotationReport{}, fmt.Errorf("rewrap secret lease %s: %w", uuid.UUID(row.ID.Bytes), rErr)
		}
		if !dryRun {
			if uErr := q.UpdateSecretLeaseWrappedDEK(ctx, db.UpdateSecretLeaseWrappedDEKParams{
				WorkspaceID: row.WorkspaceID,
				ID:          row.ID,
				KekID:       rewrapped.KeyID,
				WrappedDek:  rewrapped.WrappedDEK,
			}); uErr != nil {
				return RotationReport{}, fmt.Errorf("update secret lease %s: %w", uuid.UUID(row.ID.Bytes), uErr)
			}
		}
		report.SecretLeases++
	}

	mcpRows, err := q.ListMCPServersForKEKRotation(ctx, primaryID)
	if err != nil {
		return RotationReport{}, fmt.Errorf("list mcp servers: %w", err)
	}
	for _, row := range mcpRows {
		aad := SecretAAD(uuid.UUID(row.WorkspaceID.Bytes), "mcp_server", uuid.UUID(row.ID.Bytes))
		rewrapped, rErr := ring.RewrapDataKey(EnvelopeCiphertext{
			KeyID:      row.KekID,
			WrappedDEK: row.WrappedDek,
			Ciphertext: row.EncryptedValue,
		}, aad)
		if rErr != nil {
			return RotationReport{}, fmt.Errorf("rewrap mcp server %s: %w", uuid.UUID(row.ID.Bytes), rErr)
		}
		if !dryRun {
			if uErr := q.UpdateMCPServerWrappedDEK(ctx, db.UpdateMCPServerWrappedDEKParams{
				WorkspaceID: row.WorkspaceID,
				ID:          row.ID,
				KekID:       rewrapped.KeyID,
				WrappedDek:  rewrapped.WrappedDEK,
			}); uErr != nil {
				return RotationReport{}, fmt.Errorf("update mcp server %s: %w", uuid.UUID(row.ID.Bytes), uErr)
			}
		}
		report.MCPServers++
	}

	codexRows, err := q.ListCodexPlanConnectionsForKEKRotation(ctx, pgtype.Text{String: primaryID, Valid: true})
	if err != nil {
		return RotationReport{}, fmt.Errorf("list codex plan connections: %w", err)
	}
	for _, row := range codexRows {
		aad := []byte("codex-plan:" + uuid.UUID(row.WorkspaceID.Bytes).String() + ":" + uuid.UUID(row.BotID.Bytes).String() + ":" + uuid.UUID(row.Revision.Bytes).String())
		rewrapped, rErr := ring.RewrapDataKey(EnvelopeCiphertext{
			KeyID:      row.KekID.String,
			WrappedDEK: row.WrappedDek,
			Ciphertext: row.EncryptedTokens,
		}, aad)
		if rErr != nil {
			return RotationReport{}, fmt.Errorf("rewrap codex plan connection %s: %w", uuid.UUID(row.BotID.Bytes), rErr)
		}
		if !dryRun {
			if uErr := q.UpdateCodexPlanConnectionWrappedDEK(ctx, db.UpdateCodexPlanConnectionWrappedDEKParams{
				WorkspaceID: row.WorkspaceID,
				BotID:       row.BotID,
				Revision:    row.Revision,
				KekID:       pgtype.Text{String: rewrapped.KeyID, Valid: true},
				WrappedDek:  rewrapped.WrappedDEK,
			}); uErr != nil {
				return RotationReport{}, fmt.Errorf("update codex plan connection %s: %w", uuid.UUID(row.BotID.Bytes), uErr)
			}
		}
		report.CodexPlanConnections++
	}

	if !dryRun {
		if err := tx.Commit(ctx); err != nil {
			return RotationReport{}, fmt.Errorf("commit KEK rotation tx: %w", err)
		}
	}
	return report, nil
}
