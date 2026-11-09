-- name: ListBudgetState :many
SELECT b.*,COALESCE((SELECT sum(COALESCE(u.cost_micros, u.internal_charge_micros)) FROM usage_records u WHERE u.workspace_id=b.workspace_id AND (b.bot_id IS NULL OR u.bot_id=b.bot_id)
AND u.created_at >= CASE
    WHEN b.period = '5h' THEN sqlc.arg(as_of)::timestamptz - interval '5 hours'
    ELSE date_trunc(b.period,sqlc.arg(as_of)::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
END),0)::bigint AS spent_micros,
COALESCE((SELECT sum(u.reserved_micros) FROM usage_records u WHERE u.workspace_id=b.workspace_id AND (b.bot_id IS NULL OR u.bot_id=b.bot_id)
AND u.created_at >= CASE
    WHEN b.period = '5h' THEN sqlc.arg(as_of)::timestamptz - interval '5 hours'
    ELSE date_trunc(b.period,sqlc.arg(as_of)::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
END),0)::bigint AS reserved_micros
FROM budgets b WHERE b.workspace_id=$1 AND (b.bot_id IS NULL OR b.bot_id=sqlc.arg(bot_id)::uuid);

-- name: SetBudget :one
INSERT INTO budgets(workspace_id,bot_id,period,limit_micros) VALUES($1,$2,$3,$4)
ON CONFLICT(workspace_id,bot_id,period) DO UPDATE SET limit_micros=EXCLUDED.limit_micros RETURNING *;

-- name: DeleteBotBudget :one
DELETE FROM budgets
WHERE workspace_id = $1 AND bot_id = $2 AND period = $3
RETURNING id;

-- name: CreateBudgetApproval :one
-- RETURNING nothing on conflict so a later turn does not notify again for the same pending card.
INSERT INTO approvals(workspace_id,bot_id,budget_id) VALUES($1,$2,$3)
ON CONFLICT(workspace_id,budget_id) WHERE status='pending' DO NOTHING
RETURNING id;

-- name: RecordTurnUsage :exec
INSERT INTO usage_records(workspace_id,bot_id,turn_id,request_id,source,model,input_tokens,output_tokens,cache_tokens,cost_micros,created_at,billing_kind,subscription_budgeted)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,COALESCE(NULLIF(sqlc.arg(billing_kind)::text,''),'metered'),sqlc.arg(subscription_budgeted)) ON CONFLICT(workspace_id,request_id) DO NOTHING;

-- name: ListModelPrices :many
SELECT * FROM model_prices WHERE workspace_id=$1 ORDER BY provider ASC, model ASC;

-- name: GetModelPrice :one
SELECT * FROM model_prices WHERE workspace_id=$1 AND provider=$2 AND model=$3;

-- name: SetModelPrice :one
INSERT INTO model_prices(workspace_id,provider,model,input_micros_per_million,output_micros_per_million,cache_micros_per_million)
VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(workspace_id,provider,model) DO UPDATE SET input_micros_per_million=EXCLUDED.input_micros_per_million,output_micros_per_million=EXCLUDED.output_micros_per_million,cache_micros_per_million=EXCLUDED.cache_micros_per_million RETURNING *;

-- name: SpendTotals :one
SELECT
    COALESCE(sum(cost_micros) FILTER (WHERE created_at >= date_trunc('day', sqlc.arg(as_of)::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'), 0)::bigint AS provider_today_micros,
    COALESCE(sum(cost_micros), 0)::bigint AS provider_week_micros,
    COALESCE(sum(COALESCE(cost_micros, internal_charge_micros)) FILTER (WHERE created_at >= date_trunc('day', sqlc.arg(as_of)::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'), 0)::bigint AS total_today_micros,
    COALESCE(sum(COALESCE(cost_micros, internal_charge_micros)), 0)::bigint AS total_week_micros
FROM usage_records
WHERE workspace_id = $1
  AND (sqlc.narg(bot_id)::uuid IS NULL OR bot_id = sqlc.narg(bot_id))
  AND created_at >= date_trunc('week', sqlc.arg(as_of)::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC';

-- name: LockWorkspaceSpend :exec
SELECT pg_advisory_lock(hashtextextended(sqlc.arg(lock_key)::text, 1));

-- name: UnlockWorkspaceSpend :exec
SELECT pg_advisory_unlock(hashtextextended(sqlc.arg(lock_key)::text, 1));

-- name: LockWorkspaceBudgetCheck :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(lock_key)::text, 1));

-- name: ResolveBudgetApprovals :exec
UPDATE approvals SET status='approved',decided_by=$3,decided_at=now() WHERE workspace_id=$1 AND budget_id=$2 AND status='pending';

-- name: ListBudgetApprovals :many
SELECT * FROM approvals WHERE workspace_id=$1 AND status='pending' ORDER BY created_at;

-- name: HasUnpricedAPIUsage :one
SELECT EXISTS(SELECT 1 FROM usage_records u WHERE u.workspace_id=$1
AND (sqlc.narg(bot_id)::uuid IS NULL OR u.bot_id=sqlc.narg(bot_id)::uuid)
AND u.source IN ('api_response','proxy_meter') AND (u.billing_kind='metered' OR u.subscription_budgeted) AND u.cost_micros IS NULL AND u.internal_charge_micros IS NULL AND u.reserved_micros IS NULL
AND u.created_at >= CASE
    WHEN sqlc.arg(period)::text = '5h' THEN sqlc.arg(as_of)::timestamptz - interval '5 hours'
    ELSE date_trunc(sqlc.arg(period)::text,sqlc.arg(as_of)::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
END);

-- name: SettleProxyUsage :execrows
UPDATE usage_records SET input_tokens=$3,output_tokens=$4,cache_tokens=$5,cost_micros=$6,internal_charge_micros=$7,usage_known=$8,reserved_micros=NULL
WHERE workspace_id=$1 AND request_id=$2 AND source='proxy_meter';

-- name: GetRunningBotTurn :one
SELECT t.* FROM bot_turns t WHERE t.workspace_id=$1 AND t.bot_id=$2 AND t.status='running';

-- name: HasProxyTurnUsage :one
SELECT EXISTS(SELECT 1 FROM usage_records WHERE workspace_id=$1 AND turn_id=$2 AND source='proxy_meter');

-- name: ReserveProxyUsage :execrows
UPDATE usage_records SET reserved_micros=$3
WHERE workspace_id=$1 AND request_id=$2 AND source='proxy_meter' AND cost_micros IS NULL;

-- name: GroupBudgetCommitment :one
SELECT COALESCE(sum(COALESCE(u.cost_micros,u.internal_charge_micros,u.reserved_micros)),0)::bigint AS committed_micros,
count(*) FILTER (WHERE u.cost_micros IS NULL AND u.internal_charge_micros IS NULL AND u.reserved_micros IS NULL AND (u.billing_kind='metered' OR u.subscription_budgeted))::bigint AS unknown_rows
FROM usage_records u JOIN bot_turns t ON t.workspace_id=u.workspace_id AND t.id=u.turn_id
WHERE u.workspace_id=$1 AND t.conversation_id=$2;

-- name: GetSubscriptionBudgetPolicy :one
SELECT count_subscription_usage FROM workspaces WHERE id = $1;

-- name: SetSubscriptionBudgetPolicy :execrows
UPDATE workspaces SET count_subscription_usage = $2 WHERE id = $1;

-- name: ListSubscriptionUsage :many
WITH periods AS (
    SELECT '5h'::text AS name
    UNION ALL SELECT 'day'::text
    UNION ALL SELECT 'week'::text
    UNION ALL SELECT 'month'::text
)
SELECT u.model, period.name AS period,
       sum(u.input_tokens)::bigint AS input_tokens,
       sum(u.output_tokens)::bigint AS output_tokens,
       sum(u.cache_tokens)::bigint AS cache_tokens,
       COALESCE(sum(u.internal_charge_micros), 0)::bigint AS internal_charge_micros,
       count(*) FILTER (WHERE u.usage_known IS FALSE OR
           (u.usage_known IS NULL AND u.input_tokens = 0 AND u.output_tokens = 0))::bigint AS unknown_requests
FROM usage_records u
CROSS JOIN periods period
WHERE u.workspace_id = sqlc.arg(workspace_id)
  AND (sqlc.narg(bot_id)::uuid IS NULL OR u.bot_id = sqlc.narg(bot_id))
  AND u.billing_kind = 'subscription'
  AND u.created_at >= CASE
      WHEN period.name = '5h' THEN sqlc.arg(as_of)::timestamptz - interval '5 hours'
      ELSE date_trunc(period.name, sqlc.arg(as_of)::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
  END
GROUP BY period.name, u.model ORDER BY period.name, u.model;
