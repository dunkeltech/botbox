package budgets

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/bots"
	"github.com/dunkeltech/botbox/backend/internal/codexplan"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ProxyAdmission reserves cost before a request and settles outside cancellation.
// Opaque or unbounded budgeted requests retain per-request serialization. A crash
// leaves a durable reservation or unknown cost, never free budget capacity.
func ProxyAdmission(pool *pgxpool.Pool, workspaceID, botID uuid.UUID, now func() time.Time) func(context.Context, string, string, string) (func(llmproxy.Usage) error, error) {
	if now == nil {
		now = time.Now
	}
	return func(ctx context.Context, provider, model, _ string) (func(llmproxy.Usage) error, error) {
		if pool == nil || workspaceID == uuid.Nil || botID == uuid.Nil {
			return nil, errors.New("proxy identity unavailable")
		}
		q, release, err := lockSpend(ctx, pool, workspaceID)
		if err != nil {
			return nil, err
		}
		retained := false
		defer func() {
			if !retained {
				release()
			}
		}()
		ws, bot := pgtype.UUID{Bytes: workspaceID, Valid: true}, pgtype.UUID{Bytes: botID, Valid: true}
		state, err := q.GetBotRuntime(ctx, db.GetBotRuntimeParams{WorkspaceID: ws, ID: bot})
		if err != nil || state.Paused {
			return nil, errors.New("bot execution suspended")
		}
		var turn db.BotTurn
		var turnID pgtype.UUID
		priceProvider := provider
		if provider == llmproxy.ProviderOpenAIPlan {
			revision := llmproxy.PlanRevision(ctx)
			if revision == "" {
				return nil, llmproxy.ErrPlanAccess
			}
			if err = codexplan.CheckDesktop(ctx, q, ws, bot, revision); err != nil {
				if errors.Is(err, codexplan.ErrForbidden) {
					return nil, llmproxy.ErrPlanAccess
				}
				return nil, err
			}
			priceProvider = llmproxy.ProviderOpenAI
		} else {
			// A desktop CLI can use the metered route without a chat turn.
			var turnErr error
			turn, turnErr = q.GetRunningBotTurn(ctx, db.GetRunningBotTurnParams{WorkspaceID: ws, BotID: bot})
			if turnErr == nil {
				turnID = turn.ID
			} else if !errors.Is(turnErr, pgx.ErrNoRows) {
				return nil, turnErr
			}
			config, cfgErr := q.GetBotByWorkspaceAndID(ctx, db.GetBotByWorkspaceAndIDParams{WorkspaceID: ws, ID: bot})
			_, configured := bots.RunnerForProvider(config.LlmProvider)
			if cfgErr != nil || configured != provider {
				return nil, errors.New("provider is not configured for bot")
			}
			if provider == llmproxy.ProviderOpenRouter && model != config.LlmModel {
				// Pi can resolve model patterns fuzzily. Meter only the exact saved OpenRouter ID.
				return nil, errors.New("OpenRouter model is not configured for bot")
			}
		}

		if err = Check(ctx, q, workspaceID, botID, now()); err != nil {
			return nil, err
		}
		limits, err := q.ListBudgetState(ctx, db.ListBudgetStateParams{WorkspaceID: ws, BotID: bot, AsOf: pgtype.Timestamptz{Time: now(), Valid: true}})
		if err != nil {
			return nil, err
		}
		var groupLimit *int64
		var groupSpent int64
		if turnID.Valid {
			group, groupErr := q.GetGroup(ctx, db.GetGroupParams{WorkspaceID: ws, ID: turn.ConversationID})
			if groupErr != nil && !errors.Is(groupErr, pgx.ErrNoRows) {
				return nil, groupErr
			}
			if groupErr == nil && group.BudgetLimitMicros.Valid {
				groupLimit = &group.BudgetLimitMicros.Int64
				committed, e := q.GroupBudgetCommitment(ctx, db.GroupBudgetCommitmentParams{WorkspaceID: ws, ConversationID: turn.ConversationID})
				if e != nil {
					return nil, e
				}
				if committed.UnknownRows > 0 {
					return nil, errors.New("usage reconciliation required")
				}
				groupSpent = committed.CommittedMicros
				if groupSpent >= *groupLimit {
					return nil, llmproxy.ErrBudgetExceeded
				}
			}
		}
		limited := len(limits) > 0 || groupLimit != nil
		subscription := llmproxy.SubscriptionRequest(ctx)
		var subscriptionBudgeted bool
		if subscription {
			subscriptionBudgeted, err = q.GetSubscriptionBudgetPolicy(ctx, ws)
			if err != nil {
				return nil, err
			}
		}
		budgeted := !subscription || subscriptionBudgeted
		price, priceErr := q.GetModelPrice(ctx, db.GetModelPriceParams{WorkspaceID: ws, Provider: priceProvider, Model: model})
		if priceErr != nil && !errors.Is(priceErr, pgx.ErrNoRows) {
			return nil, priceErr
		}
		if limited && priceErr != nil && budgeted {
			return nil, llmproxy.ErrModelPriceRequired
		}
		var reserved pgtype.Int8
		if input, output, bounded := llmproxy.RequestTokenBound(ctx); limited && bounded && budgeted {
			inputRate := max(price.InputMicrosPerMillion, price.CacheMicrosPerMillion)
			amount, e := PriceMicros(input, output, 0, inputRate, price.OutputMicrosPerMillion, 0)
			if e != nil {
				return nil, e
			}
			for _, limit := range limits {
				if amount > limit.LimitMicros-limit.SpentMicros-limit.ReservedMicros {
					return nil, llmproxy.ErrBudgetExceeded
				}
			}
			if groupLimit != nil && amount > *groupLimit-groupSpent {
				return nil, llmproxy.ErrBudgetExceeded
			}
			reserved = pgtype.Int8{Int64: amount, Valid: true}
		}
		billingKind := "metered"
		if subscription {
			billingKind = "subscription"
		}
		request := pgtype.UUID{Bytes: uuid.New(), Valid: true}
		if err = q.RecordTurnUsage(ctx, db.RecordTurnUsageParams{WorkspaceID: ws, BotID: bot, TurnID: turnID, RequestID: request, Source: "proxy_meter", Model: model, BillingKind: billingKind, SubscriptionBudgeted: subscriptionBudgeted, CreatedAt: pgtype.Timestamptz{Time: now(), Valid: true}}); err != nil {
			return nil, err
		}
		if reserved.Valid {
			rows, e := q.ReserveProxyUsage(ctx, db.ReserveProxyUsageParams{WorkspaceID: ws, RequestID: request, ReservedMicros: reserved})
			if e != nil {
				return nil, e
			}
			if rows != 1 {
				return nil, errors.New("usage record disappeared")
			}
		}
		retained = limited && budgeted && !reserved.Valid
		var once sync.Once
		var settleErr error
		return func(usage llmproxy.Usage) error {
			once.Do(func() {
				settlement, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				settleQ := q
				if retained {
					defer release()
				} else {
					var unlock func()
					settleQ, unlock, settleErr = lockSpend(settlement, pool, workspaceID)
					if settleErr != nil {
						return
					}
					defer unlock()
				}
				var cost, internalCharge pgtype.Int8
				valid := usage.Known && usage.InputTokens >= 0 && usage.OutputTokens >= 0 && usage.CacheTokens >= 0 && usage.CacheTokens <= usage.InputTokens
				if valid && budgeted {
					var amount int64
					var e error
					switch {
					case usage.ReportedCost != "" && !subscription:
						amount, e = ReportedMicros(string(usage.ReportedCost))
					case usage.InputTokens == 0 && usage.OutputTokens == 0:
						amount = 0
					case priceErr == nil:
						amount, e = PriceMicros(usage.InputTokens, usage.OutputTokens, usage.CacheTokens, price.InputMicrosPerMillion, price.OutputMicrosPerMillion, price.CacheMicrosPerMillion)
					default:
						e = errors.New("model price unavailable")
					}
					if e == nil && (!reserved.Valid || amount <= reserved.Int64) {
						if subscription {
							internalCharge = pgtype.Int8{Int64: amount, Valid: true}
						} else {
							cost = pgtype.Int8{Int64: amount, Valid: true}
						}
					}
				}
				// Unknown or excessive usage clears the estimate but keeps a
				// durable unknown charge, blocking monetary budgets for review.
				if !valid {
					usage.InputTokens, usage.OutputTokens, usage.CacheTokens = 0, 0, 0
				}
				rows, e := settleQ.SettleProxyUsage(settlement, db.SettleProxyUsageParams{WorkspaceID: ws, RequestID: request, InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CacheTokens: usage.CacheTokens, CostMicros: cost, InternalChargeMicros: internalCharge, UsageKnown: pgtype.Bool{Bool: valid, Valid: true}})
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

func lockSpend(ctx context.Context, pool *pgxpool.Pool, workspace uuid.UUID) (*db.Queries, func(), error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	q := db.New(conn)
	if err = q.LockWorkspaceSpend(ctx, workspace.String()); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = conn.Hijack().Close(cleanup)
		return nil, nil, err
	}
	return q, func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if q.UnlockWorkspaceSpend(cleanup, workspace.String()) != nil {
			_ = conn.Hijack().Close(cleanup)
		} else {
			conn.Release()
		}
	}, nil
}

// CheckSerialized reads commitments under the same short lock used for admission.
func CheckSerialized(ctx context.Context, pool *pgxpool.Pool, workspace, bot uuid.UUID, now time.Time) error {
	if pool == nil {
		return errors.New("budget database unavailable")
	}
	q, release, err := lockSpend(ctx, pool, workspace)
	if err != nil {
		return err
	}
	defer release()
	return Check(ctx, q, workspace, bot, now)
}
