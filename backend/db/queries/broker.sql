-- name: CreateBrokerSession :one
INSERT INTO broker_sessions (
    id,
    workspace_id,
    bot_id,
    conversation_id,
    turn_id,
    adapter_id,
    origin,
    target_id,
    frame_id,
    document_id,
    form_action,
    status,
    capture_suppressed,
    expires_at
)
VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14
)
RETURNING *;

-- name: GetBrokerSession :one
SELECT *
FROM broker_sessions
WHERE workspace_id = $1 AND id = $2;

-- name: UpdateBrokerSessionState :one
UPDATE broker_sessions
SET status = sqlc.arg(status),
    target_id = sqlc.arg(target_id),
    frame_id = sqlc.arg(frame_id),
    document_id = sqlc.arg(document_id),
    form_action = sqlc.arg(form_action),
    capture_suppressed = sqlc.arg(capture_suppressed),
    takeover_user_id = sqlc.narg(takeover_user_id),
    updated_at = now()
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: AttachBrokerBindingToSecretRequest :one
UPDATE secret_requests
SET broker_session_id = $3,
    broker_target_id = $4,
    broker_frame_id = $5,
    broker_document_id = $6,
    broker_form_action = $7,
    updated_at = now()
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: SetSecretRequestStatusFulfilledFromTakeover :one
UPDATE secret_requests
SET status = 'fulfilled',
    granted_scope = 'ephemeral',
    error_code = '',
    decided_by = sqlc.arg(decided_by),
    decided_at = sqlc.arg(decided_at)::timestamptz,
    updated_at = now()
WHERE workspace_id = $1
  AND id = $2
  AND status IN ('pending', 'fulfilling')
RETURNING *;

-- name: ResetSecretRequestForMFA :one
UPDATE secret_requests
SET status = 'pending',
    operation_id = NULL,
    error_code = 'mfa_required',
    broker_target_id = sqlc.arg(broker_target_id),
    broker_frame_id = sqlc.arg(broker_frame_id),
    broker_document_id = sqlc.arg(broker_document_id),
    broker_form_action = sqlc.arg(broker_form_action),
    decided_by = sqlc.arg(decided_by),
    updated_at = now()
WHERE workspace_id = $1
  AND id = $2
  AND status = 'fulfilling'
  AND operation_id = sqlc.arg(operation_id)
RETURNING *;

-- name: RevokeBrokerSessionsForBot :execrows
UPDATE broker_sessions
SET status = 'revoked',
    revoked_at = sqlc.arg(revoked_at)::timestamptz,
    capture_suppressed = TRUE,
    takeover_user_id = NULL,
    updated_at = now()
WHERE workspace_id = $1
  AND bot_id = $2
  AND revoked_at IS NULL;

-- name: RevokeBrokerSession :execrows
UPDATE broker_sessions
SET status = 'revoked',
    revoked_at = sqlc.arg(revoked_at)::timestamptz,
    capture_suppressed = TRUE,
    takeover_user_id = NULL,
    updated_at = now()
WHERE workspace_id = $1
  AND id = $2
  AND revoked_at IS NULL;

-- name: ListActiveBrokerSessionsForBot :many
SELECT *
FROM broker_sessions
WHERE workspace_id = $1
  AND bot_id = $2
  AND revoked_at IS NULL
  AND status IN ('bound', 'authenticated', 'mfa_required', 'takeover')
  AND expires_at > sqlc.arg(as_of)::timestamptz
ORDER BY created_at DESC;

-- SPDX-License-Identifier: AGPL-3.0-only
