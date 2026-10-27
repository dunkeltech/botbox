-- name: CreateSecretRequest :one
INSERT INTO secret_requests (
    id,
    workspace_id,
    bot_id,
    conversation_id,
    message_id,
    turn_id,
    tool_call_id,
    kind,
    adapter_id,
    target_origin,
    reason,
    env_placeholder_name,
    placeholder_value,
    username_hint,
    status,
    expires_at
)
VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 'pending', $15
)
RETURNING *;

-- name: AttachSecretRequestMessage :exec
UPDATE secret_requests
SET message_id = $3, updated_at = now()
WHERE workspace_id = $1 AND id = $2;

-- name: GetSecretRequest :one
SELECT *
FROM secret_requests
WHERE workspace_id = $1 AND id = $2;

-- name: GetSecretRequestForUpdate :one
SELECT *
FROM secret_requests
WHERE workspace_id = $1 AND id = $2
FOR UPDATE;

-- name: CountPendingSecretRequestsForBot :one
SELECT count(*)::bigint
FROM secret_requests
WHERE workspace_id = $1
  AND bot_id = $2
  AND status IN ('pending', 'fulfilling')
  AND expires_at > sqlc.arg(as_of)::timestamptz;

-- name: CountRecentSecretRequestsForBot :one
SELECT count(*)::bigint
FROM secret_requests
WHERE workspace_id = $1
  AND bot_id = $2
  AND created_at >= sqlc.arg(since)::timestamptz;

-- name: CountRecentSecretRequestsForWorkspace :one
SELECT count(*)::bigint
FROM secret_requests
WHERE workspace_id = $1
  AND created_at >= sqlc.arg(since)::timestamptz;

-- name: ClaimSecretRequestForFulfill :one
UPDATE secret_requests
SET status = 'fulfilling',
    operation_id = sqlc.arg(operation_id),
    updated_at = now()
WHERE workspace_id = $1
  AND id = $2
  AND status = 'pending'
  AND expires_at > sqlc.arg(as_of)::timestamptz
RETURNING *;

-- name: CompleteSecretRequestFulfill :one
UPDATE secret_requests
SET status = 'fulfilled',
    granted_scope = sqlc.arg(granted_scope),
    decided_by = sqlc.arg(decided_by),
    decided_at = sqlc.arg(decided_at)::timestamptz,
    updated_at = now()
WHERE workspace_id = $1
  AND id = $2
  AND status = 'fulfilling'
  AND operation_id = sqlc.arg(operation_id)
RETURNING *;

-- name: FailSecretRequestClaim :execrows
UPDATE secret_requests
SET status = 'failed',
    error_code = sqlc.arg(error_code),
    updated_at = now()
WHERE workspace_id = $1
  AND id = $2
  AND status = 'fulfilling'
  AND operation_id = sqlc.arg(operation_id);

-- name: DenySecretRequest :one
UPDATE secret_requests
SET status = 'denied',
    decided_by = sqlc.arg(decided_by),
    decided_at = sqlc.arg(decided_at)::timestamptz,
    updated_at = now()
WHERE workspace_id = $1
  AND id = $2
  AND status = 'pending'
  AND expires_at > sqlc.arg(as_of)::timestamptz
RETURNING *;

-- name: ExpirePendingSecretRequest :execrows
UPDATE secret_requests
SET status = 'expired',
    updated_at = now()
WHERE workspace_id = $1
  AND id = $2
  AND status = 'pending'
  AND expires_at <= sqlc.arg(as_of)::timestamptz;

-- name: CancelPendingSecretRequestsForTurn :many
UPDATE secret_requests
SET status = 'cancelled',
    updated_at = now()
WHERE workspace_id = $1
  AND turn_id = $2
  AND status IN ('pending', 'fulfilling')
RETURNING *;

-- name: UpdateSecretRequestMessageCard :exec
UPDATE messages
SET content = sqlc.arg(content),
    metadata = sqlc.arg(metadata)
WHERE workspace_id = $1 AND id = $2;

-- name: PauseGroupForSecretRequest :exec
UPDATE conversations
SET paused = true, pause_reason = 'secret_required'
WHERE workspace_id = $1 AND id = $2 AND type = 'group' AND paused = false;

-- name: ClearSecretRequiredPause :execrows
UPDATE conversations
SET paused = false, pause_reason = ''
WHERE workspace_id = $1 AND id = $2 AND type = 'group'
  AND paused = true AND pause_reason = 'secret_required';

-- name: FindConflictingBotSecret :one
SELECT id
FROM bot_secrets
WHERE workspace_id = $1
  AND bot_id = $2
  AND lower(domain_match) = lower(sqlc.arg(domain_match)::text)
  AND path_prefix = sqlc.arg(path_prefix)::text
  AND lower(header_name) = lower(sqlc.arg(header_name)::text)
LIMIT 1;

-- name: FindConflictingWorkspaceSecret :one
SELECT id
FROM workspace_secrets
WHERE workspace_id = $1
  AND lower(domain_match) = lower(sqlc.arg(domain_match)::text)
  AND path_prefix = sqlc.arg(path_prefix)::text
  AND lower(header_name) = lower(sqlc.arg(header_name)::text)
LIMIT 1;

-- name: CreateSecretLease :one
INSERT INTO secret_leases (
    id,
    workspace_id,
    bot_id,
    secret_request_id,
    turn_id,
    conversation_id,
    adapter_id,
    domain_match,
    port,
    auth_scheme,
    header_name,
    username,
    placeholder_env,
    placeholder_value,
    kek_id,
    wrapped_dek,
    encrypted_value,
    expires_at,
    created_by
)
VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19
)
RETURNING *;

-- name: ListActiveSecretLeasesForBot :many
SELECT *
FROM secret_leases
WHERE workspace_id = $1
  AND bot_id = $2
  AND revoked_at IS NULL
  AND expires_at > sqlc.arg(as_of)::timestamptz
ORDER BY created_at DESC;

-- name: RevokeSecretLeasesForTurn :execrows
UPDATE secret_leases
SET revoked_at = sqlc.arg(revoked_at)::timestamptz,
    revocation_reason = sqlc.arg(reason)
WHERE workspace_id = $1
  AND turn_id = $2
  AND revoked_at IS NULL;

-- name: RevokeSecretLeaseByID :execrows
UPDATE secret_leases
SET revoked_at = sqlc.arg(revoked_at)::timestamptz,
    revocation_reason = sqlc.arg(reason)
WHERE workspace_id = $1
  AND id = $2
  AND revoked_at IS NULL;

-- name: CreateBotSecretWithAdapter :one
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
    adapter_id,
    kek_id,
    wrapped_dek,
    encrypted_value,
    masked_preview,
    created_by
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
RETURNING *;

-- name: CreateWorkspaceSecretWithAdapter :one
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
    adapter_id,
    kek_id,
    wrapped_dek,
    encrypted_value,
    masked_preview,
    created_by
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
RETURNING *;

-- name: ListSecretRequestsForConversation :many
SELECT *
FROM secret_requests
WHERE workspace_id = $1
  AND conversation_id = $2
ORDER BY created_at DESC
LIMIT 50;
