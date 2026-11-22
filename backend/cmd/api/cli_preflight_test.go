// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/dunkeltech/botbox/backend/internal/testutil/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"time"
)

type preflightComputer struct {
	compute.Driver
	proxyErr     error
	runErr       error
	calls        int
	subscription bool
}

func (d *preflightComputer) CLIProxyReady(context.Context, compute.EnvID) error { return d.proxyErr }
func (d *preflightComputer) RunCLI(_ context.Context, _ compute.EnvID, cli, prompt, _, model string, emit func(engine.Event) error) error {
	d.calls++
	if (cli != "claude" && cli != "codex" && cli != "agy") || strings.Contains(prompt, "fixture-private") {
		return errors.New("unsafe CLI input")
	}
	if cli == "claude" && model != "sonnet" {
		return errors.New("unsafe claude model")
	}
	if d.runErr != nil {
		return d.runErr
	}
	return emit(engine.Event{Kind: engine.EventKindMessage, Text: "fixture reply"})
}

func (d *preflightComputer) RunCLIWithSubscription(ctx context.Context, id compute.EnvID, cli, prompt, resume, model string, subscription bool, emit func(engine.Event) error) error {
	d.subscription = subscription
	return d.RunCLI(ctx, id, cli, prompt, resume, model, emit)
}

func TestClaudeSonnetPreflightAndPrivateCredentials(t *testing.T) {
	pool := pgtest.Open(t)
	q := db.New(pool)
	ctx := context.Background()
	ws, bot := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO workspaces(id,name,slug) VALUES($1,'Fixture','fixture')", ws); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO bots(id,workspace_id,name,avatar_seed,llm_mode,llm_provider,llm_model) VALUES($1,$2,'Atlas','atlas','cli','anthropic','sonnet')", bot, ws); err != nil {
		t.Fatal(err)
	}
	pg := func(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }
	config, err := q.GetBotByWorkspaceAndID(ctx, db.GetBotByWorkspaceAndIDParams{WorkspaceID: pg(ws), ID: pg(bot)})
	if err != nil {
		t.Fatal(err)
	}
	ring, err := secrets.NewKeyRing("v1", map[string][]byte{"v1": bytes.Repeat([]byte{1}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	computer := &preflightComputer{}
	session := engine.Session{WorkspaceID: ws, BotID: bot, ConversationID: uuid.New(), Bot: config, Messages: []db.Message{{Content: "hi"}}}
	run := func(want string) {
		t.Helper()
		err := runCLITurn(ctx, pool, q, ring, computer, session, func(engine.Event) error { return nil })
		if want == "" {
			if err != nil {
				t.Fatalf("configured CLI rejected: %v", err)
			}
			return
		}
		if engine.TurnFailureCode(err) != want {
			t.Fatalf("failure=%s want=%s", engine.TurnFailureCode(err), want)
		}
		if strings.Contains(err.Error(), "fixture-private") {
			t.Fatal("private cause exposed")
		}
	}
	run(engine.FailureComputerNotRunning)
	if _, err = pool.Exec(ctx, "INSERT INTO environments(bot_id,driver,env_id,status) SELECT id,'fake','fake-id','running' FROM bots WHERE workspace_id=$1 AND id=$2", ws, bot); err != nil {
		t.Fatal(err)
	}
	run(engine.FailureComputerRestart)
	if _, err = q.UpdateEnvironmentStatus(ctx, db.UpdateEnvironmentStatusParams{BotID: pg(bot), Status: "stopped"}); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "UPDATE environments SET driver='docker' WHERE bot_id IN (SELECT id FROM bots WHERE workspace_id=$1 AND id=$2)", ws, bot); err != nil {
		t.Fatal(err)
	}
	run(engine.FailureComputerNotRunning)
	if _, err = q.UpdateEnvironmentStatus(ctx, db.UpdateEnvironmentStatusParams{BotID: pg(bot), Status: "running"}); err != nil {
		t.Fatal(err)
	}
	computer.proxyErr = errors.New("fixture-private-proxy-config")
	run(engine.FailureProxyUnavailable)
	computer.proxyErr = nil
	run(engine.FailureCredentialsMissing)
	if computer.calls != 0 {
		t.Fatal("CLI started before preflight passed")
	}
	id := uuid.New()
	ciphertext, err := ring.Encrypt([]byte("fixture-private-provider-key"), fmt.Appendf(nil, "workspace:%s/scope:bot/id:%s", ws, id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.CreateBotSecret(ctx, db.CreateBotSecretParams{ID: pg(id), WorkspaceID: pg(ws), BotID: pg(bot), Name: "llm-anthropic", DomainMatch: "api.anthropic.com", AuthScheme: "header", HeaderName: "x-api-key", KekID: ciphertext.KeyID, WrappedDek: ciphertext.WrappedDEK, EncryptedValue: ciphertext.Ciphertext, MaskedPreview: "masked"}); err != nil {
		t.Fatal(err)
	}
	run("")
	if computer.calls != 1 {
		t.Fatalf("CLI calls=%d", computer.calls)
	}
	oauthID := uuid.New()
	oauthCipher, err := ring.Encrypt([]byte("fixture-private-subscription-token"), fmt.Appendf(nil, "workspace:%s/scope:bot/id:%s", ws, oauthID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.CreateBotSecret(ctx, db.CreateBotSecretParams{ID: pg(oauthID), WorkspaceID: pg(ws), BotID: pg(bot), Name: "llm-anthropic-oauth", DomainMatch: "api.anthropic.com", AuthScheme: "bearer", HeaderName: "Authorization", KekID: oauthCipher.KeyID, WrappedDek: oauthCipher.WrappedDEK, EncryptedValue: oauthCipher.Ciphertext, MaskedPreview: "masked"}); err != nil {
		t.Fatal(err)
	}
	run("")
	if !computer.subscription {
		t.Fatal("subscription token did not select native OAuth CLI mode")
	}
	computer.runErr = errors.New("fixture-private-provider-key")
	run(engine.FailureProvider)
	computer.runErr = engine.Stream(ctx, "claude", io.NopCloser(strings.NewReader(`{"type":"assistant","message":"fixture-private"}`)), nil, func(engine.Event) error { return nil })
	if computer.runErr == nil {
		t.Fatal("malformed stream accepted")
	}
	run(engine.FailureStreamFormat)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	err = runCLITurn(canceled, pool, q, ring, computer, session, func(engine.Event) error { return nil })
	if !errors.Is(err, context.Canceled) || computer.calls != 4 {
		t.Fatal("canceled request reached CLI")
	}
}

func TestCodexOpenAIPlanSecretPreflight(t *testing.T) {
	pool := pgtest.Open(t)
	q := db.New(pool)
	ctx := context.Background()
	ws, bot := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO workspaces(id,name,slug) VALUES($1,'CodexWS','codex-ws')", ws); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO bots(id,workspace_id,name,avatar_seed,llm_mode,llm_provider,llm_model) VALUES($1,$2,'Codex','codex','cli','openai','gpt-5')", bot, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO environments(bot_id,driver,env_id,status) SELECT id,'docker','codex-id','running' FROM bots WHERE workspace_id=$1 AND id=$2", ws, bot); err != nil {
		t.Fatal(err)
	}
	pg := func(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }
	config, err := q.GetBotByWorkspaceAndID(ctx, db.GetBotByWorkspaceAndIDParams{WorkspaceID: pg(ws), ID: pg(bot)})
	if err != nil {
		t.Fatal(err)
	}
	ring, err := secrets.NewKeyRing("v1", map[string][]byte{"v1": bytes.Repeat([]byte{2}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	computer := &preflightComputer{}
	session := engine.Session{WorkspaceID: ws, BotID: bot, ConversationID: uuid.New(), Bot: config, Messages: []db.Message{{Content: "hi"}}}

	// Missing llm-openai fails with credentials_missing.
	if err = runCLITurn(ctx, pool, q, ring, computer, session, func(engine.Event) error { return nil }); engine.TurnFailureCode(err) != engine.FailureCredentialsMissing {
		t.Fatalf("missing secret failure=%s want=%s", engine.TurnFailureCode(err), engine.FailureCredentialsMissing)
	}

	// Workspace-level openai_plan secret enables Codex CLI turns.
	wsSecretID := uuid.New()
	payload, err := secrets.MarshalOpenAIPlanTokens(secrets.OpenAIPlanTokens{
		Access:   "fixture-private-access",
		Refresh:  "fixture-private-refresh",
		Expires:  time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC),
		ClientID: "oaiapp_fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	wsCipher, err := ring.Encrypt(payload, secrets.SecretAAD(ws, "workspace", wsSecretID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.UpsertCodexPlanWorkspaceSecret(ctx, db.UpsertCodexPlanWorkspaceSecretParams{
		ID: pg(wsSecretID), WorkspaceID: pg(ws), KekID: wsCipher.KeyID, WrappedDek: wsCipher.WrappedDEK, EncryptedValue: wsCipher.Ciphertext, MaskedPreview: "fixt...cess",
	}); err != nil {
		t.Fatal(err)
	}
	if err = runCLITurn(ctx, pool, q, ring, computer, session, func(engine.Event) error { return nil }); err != nil {
		t.Fatalf("codex openai_plan preflight failed: %v", err)
	}
	if computer.calls != 1 {
		t.Fatalf("CLI calls=%d want=1", computer.calls)
	}

	// Reconnect-required state blocks turn before running CLI.
	if _, err = q.UpdateWorkspaceSecretOpenAIPlanTokens(ctx, db.UpdateWorkspaceSecretOpenAIPlanTokensParams{
		WorkspaceID: pg(ws), ID: pg(wsSecretID), AuthKind: secrets.SecretAuthKindOpenAIPlanReconnect,
		KekID: wsCipher.KeyID, WrappedDek: wsCipher.WrappedDEK, EncryptedValue: wsCipher.Ciphertext, MaskedPreview: "reco...nect",
	}); err != nil {
		t.Fatal(err)
	}
	if err = runCLITurn(ctx, pool, q, ring, computer, session, func(engine.Event) error { return nil }); engine.TurnFailureCode(err) != engine.FailureCredentialsUnavailable {
		t.Fatalf("reconnect failure=%s want=%s", engine.TurnFailureCode(err), engine.FailureCredentialsUnavailable)
	}
	if computer.calls != 1 {
		t.Fatal("CLI ran while reconnect was required")
	}
}

// agy turns need the Gemini API key in llm-google like the other CLIs need
// their keys. Before ADR-0040 they started without any credential check.
func TestAgyGeminiKeyPreflight(t *testing.T) {
	pool := pgtest.Open(t)
	q := db.New(pool)
	ctx := context.Background()
	ws, other, bot := uuid.New(), uuid.New(), uuid.New()
	for i, id := range []uuid.UUID{ws, other} {
		if _, err := pool.Exec(ctx, "INSERT INTO workspaces(id,name,slug) VALUES($1,$2,$2)", id, fmt.Sprintf("agy-ws-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, "INSERT INTO bots(id,workspace_id,name,avatar_seed,llm_mode,llm_provider,llm_model) VALUES($1,$2,'Gem','gem','cli','google','gemini-3.8-flash')", bot, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO environments(bot_id,driver,env_id,status) VALUES($1,'docker','agy-id','running')", bot); err != nil {
		t.Fatal(err)
	}
	pg := func(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }
	config, err := q.GetBotByWorkspaceAndID(ctx, db.GetBotByWorkspaceAndIDParams{WorkspaceID: pg(ws), ID: pg(bot)})
	if err != nil {
		t.Fatal(err)
	}
	ring, err := secrets.NewKeyRing("v1", map[string][]byte{"v1": bytes.Repeat([]byte{3}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	computer := &preflightComputer{}
	session := engine.Session{WorkspaceID: ws, BotID: bot, ConversationID: uuid.New(), Bot: config, Messages: []db.Message{{Content: "hi"}}}
	run := func(want string) {
		t.Helper()
		err := runCLITurn(ctx, pool, q, ring, computer, session, func(engine.Event) error { return nil })
		if engine.TurnFailureCode(err) != want && (want != "" || err != nil) {
			t.Fatalf("failure=%v want=%q", err, want)
		}
		if err != nil && strings.Contains(err.Error(), "fixture-private") {
			t.Fatal("private cause exposed")
		}
	}
	workspaceSecret := func(workspace uuid.UUID, domain string, aad []byte) {
		t.Helper()
		id := uuid.New()
		if aad == nil {
			aad = secrets.SecretAAD(workspace, "workspace", id)
		}
		cipher, err := ring.Encrypt([]byte("fixture-private-gemini-key"), aad)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = q.CreateWorkspaceSecret(ctx, db.CreateWorkspaceSecretParams{ID: pg(id), WorkspaceID: pg(workspace), Name: "llm-google", DomainMatch: domain, AuthScheme: "header", HeaderName: "x-goog-api-key", KekID: cipher.KeyID, WrappedDek: cipher.WrappedDEK, EncryptedValue: cipher.Ciphertext, MaskedPreview: "masked"}); err != nil {
			t.Fatal(err)
		}
	}

	run(engine.FailureCredentialsMissing)
	// A key of another workspace is never used.
	workspaceSecret(other, "generativelanguage.googleapis.com", nil)
	run(engine.FailureCredentialsMissing)
	if computer.calls != 0 {
		t.Fatal("agy started without a Gemini key")
	}
	// A key that does not decrypt for this workspace is unavailable, not missing.
	workspaceSecret(ws, "generativelanguage.googleapis.com", secrets.SecretAAD(other, "workspace", uuid.New()))
	run(engine.FailureCredentialsUnavailable)
	if computer.calls != 0 {
		t.Fatal("agy started with an unreadable Gemini key")
	}
	if _, err = pool.Exec(ctx, "DELETE FROM workspace_secrets WHERE workspace_id=$1", ws); err != nil {
		t.Fatal(err)
	}
	// The secret must be bound to the Gemini API host.
	workspaceSecret(ws, "api.openai.com", nil)
	run(engine.FailureCredentialsMissing)
	if _, err = pool.Exec(ctx, "DELETE FROM workspace_secrets WHERE workspace_id=$1", ws); err != nil {
		t.Fatal(err)
	}
	workspaceSecret(ws, "googleapis.com", nil)
	run(engine.FailureCredentialsMissing)
	if _, err = pool.Exec(ctx, "UPDATE workspace_secrets SET match_subdomains=true WHERE workspace_id=$1", ws); err != nil {
		t.Fatal(err)
	}
	run("")
	if computer.calls != 1 || computer.subscription {
		t.Fatalf("calls=%d subscription=%v", computer.calls, computer.subscription)
	}
}
