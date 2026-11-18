-- name: GetWorkspaceGuardrailPolicy :one
SELECT *
FROM workspace_guardrail_policies
WHERE workspace_id = $1;

-- name: UpsertWorkspaceGuardrailPolicy :one
INSERT INTO workspace_guardrail_policies (
    workspace_id,
    enabled,
    failure_mode,
    secrets_enabled,
    secrets_direction,
    secrets_action,
    pii_enabled,
    pii_direction,
    pii_action,
    pii_entities,
    prompt_injection_enabled,
    prompt_injection_direction,
    prompt_injection_action,
    revision,
    updated_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 1, now())
ON CONFLICT (workspace_id) DO UPDATE SET
    enabled = EXCLUDED.enabled,
    failure_mode = EXCLUDED.failure_mode,
    secrets_enabled = EXCLUDED.secrets_enabled,
    secrets_direction = EXCLUDED.secrets_direction,
    secrets_action = EXCLUDED.secrets_action,
    pii_enabled = EXCLUDED.pii_enabled,
    pii_direction = EXCLUDED.pii_direction,
    pii_action = EXCLUDED.pii_action,
    pii_entities = EXCLUDED.pii_entities,
    prompt_injection_enabled = EXCLUDED.prompt_injection_enabled,
    prompt_injection_direction = EXCLUDED.prompt_injection_direction,
    prompt_injection_action = EXCLUDED.prompt_injection_action,
    revision = workspace_guardrail_policies.revision + 1,
    updated_at = now()
RETURNING *;

-- name: ListWorkspaceGuardrailRules :many
SELECT *
FROM workspace_guardrail_rules
WHERE workspace_id = $1
ORDER BY created_at ASC, id ASC;

-- name: CountWorkspaceGuardrailRules :one
SELECT count(*)::bigint
FROM workspace_guardrail_rules
WHERE workspace_id = $1;

-- name: GetWorkspaceGuardrailRule :one
SELECT *
FROM workspace_guardrail_rules
WHERE workspace_id = $1 AND id = $2;

-- name: CreateWorkspaceGuardrailRule :one
INSERT INTO workspace_guardrail_rules (
    id,
    workspace_id,
    slug,
    name,
    match_mode,
    pattern,
    direction,
    action,
    replacement_token,
    enabled
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: UpdateWorkspaceGuardrailRule :one
UPDATE workspace_guardrail_rules
SET name = $3,
    match_mode = $4,
    pattern = $5,
    direction = $6,
    action = $7,
    replacement_token = $8,
    enabled = $9,
    updated_at = now()
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: DeleteWorkspaceGuardrailRule :execrows
DELETE FROM workspace_guardrail_rules
WHERE workspace_id = $1 AND id = $2;

-- name: InsertLLMGuardrailEvent :one
INSERT INTO llm_guardrail_events (
    id,
    workspace_id,
    bot_id,
    conversation_id,
    turn_id,
    provider,
    model,
    route_path,
    direction,
    action,
    status_code,
    latency_ms,
    rule_slugs,
    match_labels,
    match_count,
    summary,
    created_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
RETURNING *;

-- name: ListLLMGuardrailEvents :many
SELECT
    e.*,
    b.name AS bot_name
FROM llm_guardrail_events e
JOIN bots b
  ON b.workspace_id = e.workspace_id
 AND b.id = e.bot_id
WHERE e.workspace_id = $1
  AND (
      (sqlc.arg(action_filter)::text = '' AND e.action <> 'allowed')
      OR (sqlc.arg(action_filter)::text <> '' AND e.action = sqlc.arg(action_filter)::text)
  )
  AND (sqlc.arg(direction_filter)::text = '' OR e.direction = sqlc.arg(direction_filter)::text)
  AND (sqlc.narg(bot_id_filter)::uuid IS NULL OR e.bot_id = sqlc.narg(bot_id_filter)::uuid)
ORDER BY e.created_at DESC, e.id DESC
LIMIT sqlc.arg(page_limit)::int
OFFSET sqlc.arg(page_offset)::int;

-- name: SummarizeLLMGuardrailEvents24h :one
SELECT
    count(*)::bigint AS total_count,
    count(*) FILTER (WHERE action = 'redacted')::bigint AS redacted_count,
    count(*) FILTER (WHERE action = 'blocked')::bigint AS blocked_count,
    count(*) FILTER (WHERE action = 'audited')::bigint AS audited_count
FROM llm_guardrail_events
WHERE workspace_id = $1
  AND created_at >= sqlc.arg(since)::timestamptz;

-- name: HasTurnGuardrailBlock :one
SELECT EXISTS (
    SELECT 1
    FROM llm_guardrail_events
    WHERE workspace_id = $1
      AND turn_id = $2
      AND action = 'blocked'
)::boolean;

-- SPDX-License-Identifier: AGPL-3.0-only
