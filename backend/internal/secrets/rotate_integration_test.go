// SPDX-License-Identifier: AGPL-3.0-only

package secrets_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/dunkeltech/botbox/backend/internal/testutil/pgtest"
	"github.com/google/uuid"
)

func TestRotateDatabaseKEKsAcrossAllEncryptedTables(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.Open(t)

	k1 := randomKey(t)
	k2 := randomKey(t)
	ringV1, err := secrets.NewKeyRing("v1", map[string][]byte{"v1": k1})
	if err != nil {
		t.Fatalf("NewKeyRing(v1): %v", err)
	}
	ringRotate, err := secrets.NewKeyRing("v2", map[string][]byte{"v1": k1, "v2": k2})
	if err != nil {
		t.Fatalf("NewKeyRing(rotate): %v", err)
	}
	ringV2Only, err := secrets.NewKeyRing("v2", map[string][]byte{"v2": k2})
	if err != nil {
		t.Fatalf("NewKeyRing(v2): %v", err)
	}

	var userID, wsID, botID, convID, msgID, turnID, reqID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, display_name) VALUES ('rot@example.com', 'hash', 'Rot') RETURNING id`,
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspaces (name, slug) VALUES ('RotWS', 'rot-ws') RETURNING id`,
	).Scan(&wsID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`,
		wsID, userID,
	); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO bots (workspace_id, owner_user_id, name, avatar_seed, llm_mode, llm_provider, llm_model)
		 VALUES ($1, $2, 'Atlas', 'atlas-rot', 'cli', 'anthropic', 'sonnet') RETURNING id`,
		wsID, userID,
	).Scan(&botID); err != nil {
		t.Fatalf("seed bot: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO conversations (workspace_id, type, bot_id, user_id) VALUES ($1, 'dm', $2, $3) RETURNING id`,
		wsID, botID, userID,
	).Scan(&convID); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO messages (workspace_id, conversation_id, sender_type, sender_user_id, kind, content)
		 VALUES ($1, $2, 'user', $3, 'text', 'hello') RETURNING id`,
		wsID, convID, userID,
	).Scan(&msgID); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO bot_turns (workspace_id, conversation_id, bot_id, message_id, status) VALUES ($1, $2, $3, $4, 'completed') RETURNING id`,
		wsID, convID, botID, msgID,
	).Scan(&turnID); err != nil {
		t.Fatalf("seed turn: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO secret_requests (workspace_id, conversation_id, bot_id, turn_id, kind, adapter_id, target_origin, reason, status, expires_at)
		 VALUES ($1, $2, $3, $4, 'cli_egress', 'github_api', 'https://api.github.com:443', 'test rotation', 'fulfilled', now() + interval '1 hour') RETURNING id`,
		wsID, convID, botID, turnID,
	).Scan(&reqID); err != nil {
		t.Fatalf("seed secret request: %v", err)
	}

	wsSecID := uuid.New()
	wsPlain := []byte("ws-secret-value-1")
	wsEnv, err := ringV1.Encrypt(wsPlain, secrets.SecretAAD(wsID, "workspace", wsSecID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO workspace_secrets (id, workspace_id, name, domain_match, auth_scheme, header_name, kek_id, wrapped_dek, encrypted_value, masked_preview)
		 VALUES ($1, $2, 'WS_KEY', 'api.example.com', 'bearer', 'Authorization', $3, $4, $5, 'ws-***')`,
		wsSecID, wsID, wsEnv.KeyID, wsEnv.WrappedDEK, wsEnv.Ciphertext,
	); err != nil {
		t.Fatalf("seed workspace_secrets: %v", err)
	}

	botSecID := uuid.New()
	botPlain := []byte("bot-secret-value-2")
	botEnv, err := ringV1.Encrypt(botPlain, secrets.SecretAAD(wsID, "bot", botSecID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO bot_secrets (id, workspace_id, bot_id, name, domain_match, auth_scheme, header_name, kek_id, wrapped_dek, encrypted_value, masked_preview)
		 VALUES ($1, $2, $3, 'BOT_KEY', 'api.example.com', 'bearer', 'Authorization', $4, $5, $6, 'bot-***')`,
		botSecID, wsID, botID, botEnv.KeyID, botEnv.WrappedDEK, botEnv.Ciphertext,
	); err != nil {
		t.Fatalf("seed bot_secrets: %v", err)
	}

	leaseID := uuid.New()
	leasePlain := []byte("lease-secret-value-3")
	leaseEnv, err := ringV1.Encrypt(leasePlain, secrets.SecretAAD(wsID, "lease", leaseID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO secret_leases (id, workspace_id, bot_id, secret_request_id, turn_id, conversation_id, adapter_id, domain_match, auth_scheme, header_name, kek_id, wrapped_dek, encrypted_value, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, 'github_api', 'api.github.com', 'bearer', 'Authorization', $7, $8, $9, now() + interval '1 hour')`,
		leaseID, wsID, botID, reqID, turnID, convID, leaseEnv.KeyID, leaseEnv.WrappedDEK, leaseEnv.Ciphertext,
	); err != nil {
		t.Fatalf("seed secret_leases: %v", err)
	}

	mcpID := uuid.New()
	mcpPlain := []byte("mcp-secret-value-4")
	mcpEnv, err := ringV1.Encrypt(mcpPlain, secrets.SecretAAD(wsID, "mcp_server", mcpID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO mcp_servers (id, workspace_id, slug, name, endpoint_url, auth_scheme, auth_header_name, kek_id, wrapped_dek, encrypted_value, masked_preview)
		 VALUES ($1, $2, 'my-mcp', 'My MCP', 'https://mcp.example.com/mcp', 'bearer', 'Authorization', $3, $4, $5, 'mcp-***')`,
		mcpID, wsID, mcpEnv.KeyID, mcpEnv.WrappedDEK, mcpEnv.Ciphertext,
	); err != nil {
		t.Fatalf("seed mcp_servers: %v", err)
	}

	codexRev := uuid.New()
	codexPlain := []byte(`{"access":"tok","refresh":"ref"}`)
	codexAAD := []byte("codex-plan:" + wsID.String() + ":" + botID.String() + ":" + codexRev.String())
	codexEnv, err := ringV1.Encrypt(codexPlain, codexAAD)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO codex_plan_connections (workspace_id, bot_id, owner_user_id, client_id, subject, revision, kek_id, wrapped_dek, encrypted_tokens)
		 VALUES ($1, $2, $3, 'client', 'sub', $4, $5, $6, $7)`,
		wsID, botID, userID, codexRev, codexEnv.KeyID, codexEnv.WrappedDEK, codexEnv.Ciphertext,
	); err != nil {
		t.Fatalf("seed codex_plan_connections: %v", err)
	}

	// Negative test: missing old KEK must fail and keep DB untouched.
	if _, err := secrets.RotateDatabaseKEKs(ctx, pool, ringV2Only, false); err == nil {
		t.Fatal("expected rotation to fail when old KEK v1 is absent")
	}

	// Dry run reports all 5 rows without mutating kek_id.
	dry, err := secrets.RotateDatabaseKEKs(ctx, pool, ringRotate, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dry.Total() != 5 || !dry.DryRun {
		t.Fatalf("unexpected dry run report: %+v", dry)
	}
	var stillV1 string
	if err := pool.QueryRow(ctx, `SELECT kek_id FROM workspace_secrets WHERE id = $1`, wsSecID).Scan(&stillV1); err != nil || stillV1 != "v1" {
		t.Fatalf("dry run mutated kek_id=%q err=%v", stillV1, err)
	}

	// Live rotation rewraps all 5 tables to v2.
	rep, err := secrets.RotateDatabaseKEKs(ctx, pool, ringRotate, false)
	if err != nil {
		t.Fatalf("RotateDatabaseKEKs: %v", err)
	}
	if rep.WorkspaceSecrets != 1 || rep.BotSecrets != 1 || rep.SecretLeases != 1 || rep.MCPServers != 1 || rep.CodexPlanConnections != 1 {
		t.Fatalf("unexpected rotation report: %+v", rep)
	}

	// Idempotent rerun rotates 0 rows.
	rep2, err := secrets.RotateDatabaseKEKs(ctx, pool, ringRotate, false)
	if err != nil {
		t.Fatalf("second RotateDatabaseKEKs: %v", err)
	}
	if rep2.Total() != 0 {
		t.Fatalf("expected 0 rows on second rotation, got %+v", rep2)
	}

	// Verify all 5 rows decrypt with v2-only key ring (v1 dropped).
	checkTable := func(query string, arg any, aad []byte, want []byte) {
		t.Helper()
		var keyID string
		var wrapped, ct []byte
		if err := pool.QueryRow(ctx, query, arg).Scan(&keyID, &wrapped, &ct); err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		if keyID != "v2" {
			t.Fatalf("kek_id = %q, want v2", keyID)
		}
		got, err := ringV2Only.Decrypt(secrets.EnvelopeCiphertext{KeyID: keyID, WrappedDEK: wrapped, Ciphertext: ct}, aad)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("decrypt mismatch: got=%q want=%q err=%v", got, want, err)
		}
	}
	checkTable(`SELECT kek_id, wrapped_dek, encrypted_value FROM workspace_secrets WHERE id = $1`, wsSecID, secrets.SecretAAD(wsID, "workspace", wsSecID), wsPlain)
	checkTable(`SELECT kek_id, wrapped_dek, encrypted_value FROM bot_secrets WHERE id = $1`, botSecID, secrets.SecretAAD(wsID, "bot", botSecID), botPlain)
	checkTable(`SELECT kek_id, wrapped_dek, encrypted_value FROM secret_leases WHERE id = $1`, leaseID, secrets.SecretAAD(wsID, "lease", leaseID), leasePlain)
	checkTable(`SELECT kek_id, wrapped_dek, encrypted_value FROM mcp_servers WHERE id = $1`, mcpID, secrets.SecretAAD(wsID, "mcp_server", mcpID), mcpPlain)
	checkTable(`SELECT kek_id, wrapped_dek, encrypted_tokens FROM codex_plan_connections WHERE bot_id = $1`, botID, codexAAD, codexPlain)
}
