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
	if cli != "claude" || model != "sonnet" || strings.Contains(prompt, "fixture-private") {
		return errors.New("unsafe CLI input")
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
