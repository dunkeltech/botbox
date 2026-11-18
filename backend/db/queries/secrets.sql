-- name: CreateBotSecret :one
INSERT INTO bot_secrets (
    id,
    workspace_id,
    bot_id,
    name,
    domain_match,
    match_subdomains,
    path_prefix,
    auth_scheme,
    header_name,
    username,
    kek_id,
    wrapped_dek,
    encrypted_value,
    masked_preview,
    created_by
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
RETURNING *;

-- name: ListBotSecretsByWorkspaceAndBot :many
SELECT *
FROM bot_secrets
WHERE workspace_id = $1 AND bot_id = $2
ORDER BY created_at ASC, name ASC;

-- name: GetBotSecretByWorkspaceBotAndID :one
SELECT *
FROM bot_secrets
WHERE workspace_id = $1 AND bot_id = $2 AND id = $3;

-- name: UpdateBotSecretByWorkspaceBotAndID :one
UPDATE bot_secrets
SET name = $4,
    domain_match = $5,
    match_subdomains = $6,
    path_prefix = $7,
    auth_scheme = $8,
    header_name = $9,
    username = $10,
    kek_id = $11,
    wrapped_dek = $12,
    encrypted_value = $13,
    masked_preview = $14,
    updated_at = now()
WHERE workspace_id = $1 AND bot_id = $2 AND id = $3
RETURNING *;

-- name: DeleteBotSecretByWorkspaceBotAndID :execrows
DELETE FROM bot_secrets
WHERE workspace_id = $1 AND bot_id = $2 AND id = $3;

-- name: RecordBotSecretUsage :exec
UPDATE bot_secrets
SET used_count = used_count + 1,
    last_used_at = now()
WHERE workspace_id = $1 AND id = $2;

-- name: CreateWorkspaceSecret :one
INSERT INTO workspace_secrets (
    id,
    workspace_id,
    name,
    domain_match,
    match_subdomains,
    path_prefix,
    auth_scheme,
    header_name,
    username,
    kek_id,
    wrapped_dek,
    encrypted_value,
    masked_preview,
    created_by
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
RETURNING *;

-- name: ListWorkspaceSecretsByWorkspace :many
SELECT *
FROM workspace_secrets
WHERE workspace_id = $1
ORDER BY created_at ASC, name ASC;

-- name: GetWorkspaceSecretByWorkspaceAndID :one
SELECT *
FROM workspace_secrets
WHERE workspace_id = $1 AND id = $2;

-- name: UpdateWorkspaceSecretByWorkspaceAndID :one
UPDATE workspace_secrets
SET name = $3,
    domain_match = $4,
    match_subdomains = $5,
    path_prefix = $6,
    auth_scheme = $7,
    header_name = $8,
    username = $9,
    kek_id = $10,
    wrapped_dek = $11,
    encrypted_value = $12,
    masked_preview = $13,
    updated_at = now()
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: DeleteWorkspaceSecretByWorkspaceAndID :execrows
DELETE FROM workspace_secrets
WHERE workspace_id = $1 AND id = $2;

-- name: RecordWorkspaceSecretUsage :exec
UPDATE workspace_secrets
SET used_count = used_count + 1,
    last_used_at = now()
WHERE workspace_id = $1 AND id = $2;

-- name: LockBotEgressRule :exec
-- Serializes all egress policy writers per workspace/bot, including when no
-- egress_rules row exists yet.
SELECT pg_advisory_xact_lock(hashtext($1::uuid::text), hashtext($2::uuid::text));

-- name: GetEgressRuleByWorkspaceAndBot :one
SELECT er.*
FROM egress_rules er
JOIN bots b ON b.id = er.bot_id
WHERE b.workspace_id = $1 AND er.bot_id = $2;

-- name: UpsertEgressRule :one
INSERT INTO egress_rules (
    bot_id,
    workspace_id,
    policy,
    allowed_hosts,
    blocked_hosts,
    revision
)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (bot_id) DO UPDATE SET
    workspace_id = EXCLUDED.workspace_id,
    policy = EXCLUDED.policy,
    allowed_hosts = EXCLUDED.allowed_hosts,
    blocked_hosts = EXCLUDED.blocked_hosts,
    revision = EXCLUDED.revision,
    updated_at = now()
RETURNING *;

-- name: UpsertEgressInvalidationIntent :exec
INSERT INTO egress_invalidation_intents (bot_id, workspace_id, target_revision)
VALUES ($1, $2, $3)
ON CONFLICT (bot_id) DO UPDATE SET
    workspace_id = EXCLUDED.workspace_id,
    target_revision = GREATEST(egress_invalidation_intents.target_revision, EXCLUDED.target_revision),
    updated_at = now();

-- name: AcknowledgeEgressPolicyRevision :exec
WITH updated AS (
    UPDATE egress_rules
    SET acknowledged_revision = GREATEST(egress_rules.acknowledged_revision, $3::bigint),
        acknowledged_at = CASE
            WHEN $3::bigint >= egress_rules.acknowledged_revision THEN now()
            ELSE egress_rules.acknowledged_at
        END
    WHERE egress_rules.workspace_id = $1 AND egress_rules.bot_id = $2
    RETURNING egress_rules.acknowledged_revision
)
DELETE FROM egress_invalidation_intents
WHERE egress_invalidation_intents.workspace_id = $1
  AND egress_invalidation_intents.bot_id = $2
  AND egress_invalidation_intents.target_revision <= $3::bigint;

-- name: GetEgressInvalidationIntent :one
SELECT *
FROM egress_invalidation_intents
WHERE workspace_id = $1 AND bot_id = $2;

-- name: CreateEgressLog :one
INSERT INTO egress_log (
    workspace_id,
    bot_id,
    method,
    host,
    port,
    path,
    action,
    status_code,
    secret_id,
    secret_scope,
    secret_name,
    bytes_transferred,
    latency_ms
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: ListEgressLogsByWorkspaceAndBot :many
SELECT *
FROM egress_log
WHERE workspace_id = $1
  AND bot_id = $2
  AND ($3::text = '' OR action = $3::text)
ORDER BY created_at DESC, id DESC
LIMIT $4 OFFSET $5;

-- SPDX-License-Identifier: AGPL-3.0-only
