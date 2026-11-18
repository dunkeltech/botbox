// SPDX-License-Identifier: AGPL-3.0-only

package memory

import (
	"context"
	"regexp"
	"strings"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

const minStoredSecretMatchLen = 8

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`),
	regexp.MustCompile(`\bsk-(?:ant|proj)-[A-Za-z0-9_-]{16,}\b`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`),
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{16,}\b`),
	regexp.MustCompile(`\bxox[bpoas]-[A-Za-z0-9-]{16,}\b`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{20,}\b`),
	regexp.MustCompile(`\bnpm_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bpypi-[A-Za-z0-9_-]{20,}\b`),
	regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/-]{20,}=*\b`),
	regexp.MustCompile(`(?i)\bBasic\s+[A-Za-z0-9+/]{16,}={0,2}\b`),
	regexp.MustCompile(`https?://[^/\s:@]+:[^/\s@]+@[^\s/]+`),
}

// CheckZeroSecret ensures no credential patterns or decrypted workspace/bot secrets
// enter the bot knowledge base (AGENTS.md §7). Error values never echo matched text.
func CheckZeroSecret(ctx context.Context, q *db.Queries, ring *secrets.KeyRing, workspaceID, botID uuid.UUID, fields ...string) error {
	for _, field := range fields {
		if field == "" {
			continue
		}
		for _, re := range secretPatterns {
			if re.MatchString(field) {
				return ErrSecretDetected
			}
		}
	}
	if q == nil || ring == nil || workspaceID == uuid.Nil {
		return nil
	}
	stored, err := collectStoredSecrets(ctx, q, ring, workspaceID, botID)
	if err != nil {
		return err
	}
	for _, secretVal := range stored {
		if len(secretVal) < minStoredSecretMatchLen {
			continue
		}
		for _, field := range fields {
			if strings.Contains(field, secretVal) {
				return ErrSecretDetected
			}
		}
	}
	return nil
}

func collectStoredSecrets(ctx context.Context, q *db.Queries, ring *secrets.KeyRing, workspaceID, botID uuid.UUID) ([]string, error) {
	wsPG := pgtype.UUID{Bytes: workspaceID, Valid: true}
	var values []string

	if botID != uuid.Nil {
		botSecrets, err := q.ListBotSecretsByWorkspaceAndBot(ctx, db.ListBotSecretsByWorkspaceAndBotParams{
			WorkspaceID: wsPG,
			BotID:       pgtype.UUID{Bytes: botID, Valid: true},
		})
		if err != nil {
			return nil, err
		}
		for _, s := range botSecrets {
			plain, decErr := ring.Decrypt(secrets.EnvelopeCiphertext{
				KeyID:      s.KekID,
				WrappedDEK: s.WrappedDek,
				Ciphertext: s.EncryptedValue,
			}, secrets.SecretAAD(workspaceID, "bot", uuid.UUID(s.ID.Bytes)))
			if decErr == nil && len(plain) > 0 {
				values = append(values, string(plain))
			}
		}
	}

	wsSecrets, err := q.ListWorkspaceSecretsByWorkspace(ctx, wsPG)
	if err != nil {
		return nil, err
	}
	for _, s := range wsSecrets {
		plain, decErr := ring.Decrypt(secrets.EnvelopeCiphertext{
			KeyID:      s.KekID,
			WrappedDEK: s.WrappedDek,
			Ciphertext: s.EncryptedValue,
		}, secrets.SecretAAD(workspaceID, "workspace", uuid.UUID(s.ID.Bytes)))
		if decErr == nil && len(plain) > 0 {
			values = append(values, string(plain))
		}
	}

	mcpServers, err := q.ListMCPServers(ctx, wsPG)
	if err != nil {
		return nil, err
	}
	for _, srv := range mcpServers {
		if len(srv.EncryptedValue) == 0 || srv.KekID == "" {
			continue
		}
		plain, decErr := ring.Decrypt(secrets.EnvelopeCiphertext{
			KeyID:      srv.KekID,
			WrappedDEK: srv.WrappedDek,
			Ciphertext: srv.EncryptedValue,
		}, secrets.SecretAAD(workspaceID, "mcp_server", uuid.UUID(srv.ID.Bytes)))
		if decErr == nil && len(plain) > 0 {
			values = append(values, string(plain))
		}
	}

	return values, nil
}
