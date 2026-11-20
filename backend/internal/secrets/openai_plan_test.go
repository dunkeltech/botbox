// SPDX-License-Identifier: AGPL-3.0-only

package secrets_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
	"github.com/dunkeltech/botbox/backend/internal/testutil/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestExtractSecretMatchValuesForOpenAIPlan(t *testing.T) {
	t.Parallel()

	payload, err := secrets.MarshalOpenAIPlanTokens(secrets.OpenAIPlanTokens{
		Access:   "oa-access-fixture-123456",
		Refresh:  "oa-refresh-fixture-789012",
		Expires:  time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC),
		ClientID: "oaiapp_fixture",
	})
	if err != nil {
		t.Fatalf("MarshalOpenAIPlanTokens: %v", err)
	}

	got := secrets.ExtractSecretMatchValues(secrets.SecretAuthKindOpenAIPlan, payload)
	if !slices.Equal(got, []string{"oa-access-fixture-123456", "oa-refresh-fixture-789012"}) {
		t.Fatalf("ExtractSecretMatchValues(openai_plan) = %v", got)
	}

	plain := secrets.ExtractSecretMatchValues(secrets.SecretAuthKindPlain, []byte("sk-plain-fixture-value"))
	if !slices.Equal(plain, []string{"sk-plain-fixture-value"}) {
		t.Fatalf("ExtractSecretMatchValues(plain) = %v", plain)
	}
}

func TestCLIProviderResolverHandlesOpenAIPlanSecrets(t *testing.T) {
	pool := pgtest.Open(t)
	q := db.New(pool)
	ctx := context.Background()
	ws, bot := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO workspaces(id,name,slug) VALUES($1,'Fixture','fixture')", ws); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO bots(id,workspace_id,name,avatar_seed,llm_mode,llm_provider,llm_model) VALUES($1,$2,'Codex','codex','cli','openai','gpt-5')", bot, ws); err != nil {
		t.Fatal(err)
	}
	ring, err := secrets.NewKeyRing("v1", map[string][]byte{"v1": bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	pg := func(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }

	wsSecretID := uuid.New()
	payload, err := secrets.MarshalOpenAIPlanTokens(secrets.OpenAIPlanTokens{
		Access:   "ws-plan-access-token",
		Refresh:  "ws-plan-refresh-token",
		Expires:  time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC),
		ClientID: "oaiapp_fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	wsEnv, err := ring.Encrypt(payload, secrets.SecretAAD(ws, "workspace", wsSecretID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.UpsertCodexPlanWorkspaceSecret(ctx, db.UpsertCodexPlanWorkspaceSecretParams{
		ID:             pg(wsSecretID),
		WorkspaceID:    pg(ws),
		KekID:          wsEnv.KeyID,
		WrappedDek:     wsEnv.WrappedDEK,
		EncryptedValue: wsEnv.Ciphertext,
		MaskedPreview:  secrets.MaskedPreview("ws-plan-access-token"),
	}); err != nil {
		t.Fatal(err)
	}

	resolve := secrets.CLIProviderResolver(q, ring, ws, bot)
	cfg, err := resolve(ctx, bot.String(), llmproxy.ProviderOpenAI)
	if err != nil {
		t.Fatalf("resolve workspace openai_plan: %v", err)
	}
	if cfg.AuthKind != llmproxy.AuthKindOpenAIPlan || cfg.SecretValue != "ws-plan-access-token" {
		t.Fatalf("unexpected workspace config: %+v", cfg)
	}

	// Bot secret in reconnect state overrides inherited workspace secret and fails closed.
	botSecretID := uuid.New()
	botEnv, err := ring.Encrypt(payload, secrets.SecretAAD(ws, "bot", botSecretID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.UpsertCodexPlanBotSecret(ctx, db.UpsertCodexPlanBotSecretParams{
		ID:             pg(botSecretID),
		WorkspaceID:    pg(ws),
		BotID:          pg(bot),
		KekID:          botEnv.KeyID,
		WrappedDek:     botEnv.WrappedDEK,
		EncryptedValue: botEnv.Ciphertext,
		MaskedPreview:  "reco...oken",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = q.UpdateBotSecretOpenAIPlanTokens(ctx, db.UpdateBotSecretOpenAIPlanTokensParams{
		WorkspaceID:    pg(ws),
		BotID:          pg(bot),
		ID:             pg(botSecretID),
		AuthKind:       secrets.SecretAuthKindOpenAIPlanReconnect,
		KekID:          botEnv.KeyID,
		WrappedDek:     botEnv.WrappedDEK,
		EncryptedValue: botEnv.Ciphertext,
		MaskedPreview:  "reco...oken",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err = resolve(ctx, bot.String(), llmproxy.ProviderOpenAI); !errors.Is(err, llmproxy.ErrPlanReconnect) {
		t.Fatalf("expected ErrPlanReconnect, got %v", err)
	}
}
