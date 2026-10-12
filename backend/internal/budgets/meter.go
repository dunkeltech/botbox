package budgets

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ProxyAdmission binds accounting to trusted provisioning, never sandbox headers.
// A durable unpriced record precedes every request. A crash releases the advisory
// lock but leaves that record blocking monetary budgets until reconciliation.
func ProxyAdmission(pool *pgxpool.Pool, workspaceID, botID uuid.UUID, now func() time.Time) func(context.Context, string, string, string) (func(llmproxy.Usage) error, error) {
	if now == nil {
		now = time.Now
	}
	return func(ctx context.Context, provider, model, _ string) (func(llmproxy.Usage) error, error) {
		if pool == nil || workspaceID == uuid.Nil || botID == uuid.Nil {
			return nil, errors.New("proxy identity unavailable")
		}
		conn, err := pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		q := db.New(conn)
		locked := false
		release := func() {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if locked {
				if err := q.UnlockWorkspaceSpend(cleanup, workspaceID.String()); err != nil {
					_ = conn.Hijack().Close(cleanup)
					return
				}
			}
			conn.Release()
		}
		success := false
		defer func() {
			if !success {
				release()
			}
		}()
		if err = q.LockWorkspaceSpend(ctx, workspaceID.String()); err != nil {
			success = true
			_ = conn.Hijack().Close(context.WithoutCancel(ctx))
			return nil, err
		}
		locked = true
		ws, bot := pgtype.UUID{Bytes: workspaceID, Valid: true}, pgtype.UUID{Bytes: botID, Valid: true}
		state, err := q.GetBotRuntime(ctx, db.GetBotRuntimeParams{WorkspaceID: ws, ID: bot})
		// Pause is the provider kill switch. Desktop control stops the engine in
		// the worker; the person at the computer still needs the metered route.
		if err != nil || state.Paused {
			return nil, errors.New("bot execution suspended")
		}
		// Desktop claude has no chat turn. Meter it anyway so the configured
		// credential works in the computer. Budgets still apply. A null turn id
		// stays out of the chat queue's single running turn.
		turn, err := q.GetRunningBotTurn(ctx, db.GetRunningBotTurnParams{WorkspaceID: ws, BotID: bot})
		var turnID pgtype.UUID
		if err == nil {
			turnID = turn.ID
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		config, err := q.GetBotByWorkspaceAndID(ctx, db.GetBotByWorkspaceAndIDParams{WorkspaceID: ws, ID: bot})
		if err != nil || config.LlmMode != "cli" || cliProvider(config.LlmProvider) != provider {
			return nil, errors.New("provider is not configured for bot")
		}
		if err = Check(ctx, q, workspaceID, botID, now()); err != nil {
			return nil, err
		}
		price, priceErr := q.GetModelPrice(ctx, db.GetModelPriceParams{WorkspaceID: ws, Provider: provider, Model: model})
		if priceErr != nil && !errors.Is(priceErr, pgx.ErrNoRows) {
			return nil, priceErr
		}
		subscription := llmproxy.SubscriptionRequest(ctx)
		if priceErr != nil && !subscription {
			limits, e := q.ListBudgetState(ctx, db.ListBudgetStateParams{WorkspaceID: ws, BotID: bot, AsOf: pgtype.Timestamptz{Time: now(), Valid: true}})
			if e != nil {
				return nil, e
			}
			if len(limits) > 0 {
				return nil, errors.New("budgeted model requires a configured price")
			}
		}
		billingKind := "metered"
		if subscription {
			billingKind = "subscription"
		}
		request := pgtype.UUID{Bytes: uuid.New(), Valid: true}
		if err = q.RecordTurnUsage(ctx, db.RecordTurnUsageParams{WorkspaceID: ws, BotID: bot, TurnID: turnID, RequestID: request, Source: "proxy_meter", Model: model, BillingKind: billingKind, CreatedAt: pgtype.Timestamptz{Time: now(), Valid: true}}); err != nil {
			return nil, err
		}
		var once sync.Once
		var settleErr error
		success = true
		return func(usage llmproxy.Usage) error {
			once.Do(func() {
				defer release()
				settlement, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				cost := pgtype.Int8{}
				if usage.Known && !subscription {
					var amount int64
					var e error
					if usage.ReportedCost != "" {
						amount, e = ReportedMicros(string(usage.ReportedCost))
					} else if usage.InputTokens == 0 && usage.OutputTokens == 0 {
						amount = 0
					} else if priceErr == nil {
						amount, e = PriceMicros(usage.InputTokens, usage.OutputTokens, usage.CacheTokens, price.InputMicrosPerMillion, price.OutputMicrosPerMillion, price.CacheMicrosPerMillion)
					} else {
						e = errors.New("model price unavailable")
					}
					if e == nil && usage.InputTokens >= 0 && usage.OutputTokens >= 0 && usage.CacheTokens >= 0 && usage.CacheTokens <= usage.InputTokens {
						cost = pgtype.Int8{Int64: amount, Valid: true}
					}
				}
				// A missing price must not erase known tokens. An unfinished or
				// invalid response retains the provisional unpriced record.
				if !usage.Known || usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CacheTokens < 0 || usage.CacheTokens > usage.InputTokens {
					return
				}
				rows, e := q.SettleProxyUsage(settlement, db.SettleProxyUsageParams{WorkspaceID: ws, RequestID: request, InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CacheTokens: usage.CacheTokens, CostMicros: cost})
				if e != nil {
					settleErr = e
				} else if rows != 1 {
					settleErr = errors.New("usage record disappeared")
				}
			})
			return settleErr
		}, nil
	}
}

func cliProvider(provider string) string {
	switch provider {
	case "claude":
		return "anthropic"
	case "codex":
		return "openai"
	case "agy":
		return "google"
	default:
		return provider
	}
}

// CheckSerialized waits for in-flight settlement before scheduler admission.
func CheckSerialized(ctx context.Context, pool *pgxpool.Pool, workspace, bot uuid.UUID, now time.Time) error {
	if pool == nil {
		return errors.New("budget database unavailable")
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	q := db.New(conn)
	if err = q.LockWorkspaceSpend(ctx, workspace.String()); err != nil {
		_ = conn.Hijack().Close(context.WithoutCancel(ctx))
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if q.UnlockWorkspaceSpend(cleanup, workspace.String()) != nil {
			_ = conn.Hijack().Close(cleanup)
		} else {
			conn.Release()
		}
	}()
	return Check(ctx, q, workspace, bot, now)
}
