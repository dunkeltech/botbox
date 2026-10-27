package egresspolicy_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/egresspolicy"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"github.com/dunkeltech/botbox/backend/internal/testutil/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func createTestWorkspaceAndBot(t *testing.T) (*pgxpool.Pool, uuid.UUID, uuid.UUID) {
	t.Helper()
	pool := pgtest.Open(t)
	ctx := context.Background()
	q := db.New(pool)

	ws, err := q.CreateWorkspace(ctx, db.CreateWorkspaceParams{
		Name:          "Test WS",
		Slug:          "test-ws-" + uuid.NewString()[:8],
		DefaultDriver: "docker",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	bot, err := q.CreateBot(ctx, db.CreateBotParams{
		WorkspaceID: ws.ID,
		Name:        "Atlas",
		AvatarSeed:  "atlas-seed",
		LlmMode:     "api",
		LlmProvider: "anthropic",
		LlmModel:    "claude-sonnet-4",
		DriverType:  "docker",
	})
	if err != nil {
		t.Fatalf("CreateBot: %v", err)
	}
	return pool, uuid.UUID(ws.ID.Bytes), uuid.UUID(bot.ID.Bytes)
}

func TestApplyUpdateTxAtomicIntentAuditAndRollback(t *testing.T) {
	t.Parallel()
	pool, wsID, botID := createTestWorkspaceAndBot(t)
	ctx := context.Background()
	q := db.New(pool)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	res, err := egresspolicy.ApplyUpdateTx(ctx, q.WithTx(tx), egresspolicy.UpdateInput{
		WorkspaceID:      wsID,
		BotID:            botID,
		ActorID:          uuid.Nil,
		Policy:           mitmproxy.PolicyAllowlist,
		AllowedHosts:     []string{"docs.example.com"},
		HasBlockedHosts:  true,
		BlockedHosts:     []string{"evil.example.com"},
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatalf("ApplyUpdateTx: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !res.Changed || res.Rule.Revision != 1 {
		t.Fatalf("unexpected res: %+v", res)
	}

	wsPG := pgtype.UUID{Bytes: wsID, Valid: true}
	botPG := pgtype.UUID{Bytes: botID, Valid: true}
	intent, err := q.GetEgressInvalidationIntent(ctx, db.GetEgressInvalidationIntentParams{
		WorkspaceID: wsPG,
		BotID:       botPG,
	})
	if err != nil || intent.TargetRevision != 1 {
		t.Fatalf("intent=%+v err=%v", intent, err)
	}

	// Rolled-back transaction must leave policy, audit, and intent unchanged.
	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin tx2: %v", err)
	}
	if _, err := egresspolicy.ApplyUpdateTx(ctx, q.WithTx(tx2), egresspolicy.UpdateInput{
		WorkspaceID:      wsID,
		BotID:            botID,
		ActorID:          uuid.Nil,
		Policy:           mitmproxy.PolicyOpen,
		AllowedHosts:     []string{},
		HasBlockedHosts:  true,
		BlockedHosts:     []string{},
		ExpectedRevision: 1,
	}); err != nil {
		t.Fatalf("ApplyUpdateTx tx2: %v", err)
	}
	_ = tx2.Rollback(ctx)

	persisted, err := q.GetEgressRuleByWorkspaceAndBot(ctx, db.GetEgressRuleByWorkspaceAndBotParams{
		WorkspaceID: wsPG,
		BotID:       botPG,
	})
	if err != nil {
		t.Fatalf("GetEgressRuleByWorkspaceAndBot: %v", err)
	}
	if persisted.Revision != 1 || persisted.Policy != "allowlist" {
		t.Fatalf("rollback leaked state: %+v", persisted)
	}
}

func TestConcurrentPolicyWritersSerializeOnImplicitDefault(t *testing.T) {
	t.Parallel()
	pool, wsID, botID := createTestWorkspaceAndBot(t)
	ctx := context.Background()
	q := db.New(pool)

	gate := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 2)

	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-gate
			tx, err := pool.Begin(ctx)
			if err != nil {
				errs[idx] = err
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()
			_, err = egresspolicy.ApplyUpdateTx(ctx, q.WithTx(tx), egresspolicy.UpdateInput{
				WorkspaceID:      wsID,
				BotID:            botID,
				ActorID:          uuid.Nil,
				Policy:           mitmproxy.PolicyAllowlist,
				AllowedHosts:     []string{fmt.Sprintf("host-%d.example.com", idx)},
				HasBlockedHosts:  true,
				BlockedHosts:     []string{},
				ExpectedRevision: 0,
			})
			if err != nil {
				errs[idx] = err
				return
			}
			errs[idx] = tx.Commit(ctx)
		}(i)
	}
	close(gate)
	wg.Wait()

	var successes, conflicts int
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, egresspolicy.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("expected 1 winner and 1 conflict on revision 0 race, got successes=%d conflicts=%d errs=%v", successes, conflicts, errs)
	}
}
