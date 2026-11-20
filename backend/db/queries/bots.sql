-- name: CreateBot :one
INSERT INTO bots (
    workspace_id,
    owner_user_id,
    name,
    description,
    avatar_seed,
    llm_mode,
    llm_provider,
    llm_model,
    driver_type,
    reply_language,
    turn_timeout_minutes
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, COALESCE(sqlc.narg(turn_timeout_minutes)::integer, 10))
RETURNING *;

-- name: ListBotsByWorkspace :many
SELECT *
FROM bots
WHERE workspace_id = $1
ORDER BY created_at ASC, name ASC;

-- name: GetBotByWorkspaceAndID :one
SELECT *
FROM bots
WHERE workspace_id = $1 AND id = $2;

-- name: LockBotByWorkspaceAndID :one
SELECT *
FROM bots
WHERE workspace_id = $1 AND id = $2
FOR NO KEY UPDATE;

-- name: UpdateBotByWorkspaceAndID :one
UPDATE bots
SET name = $3,
    description = $4,
    avatar_seed = $5,
    llm_mode = $6,
    llm_provider = $7,
    llm_model = $8,
    driver_type = $9,
    reply_language = $10,
    turn_timeout_minutes = COALESCE(sqlc.narg(turn_timeout_minutes)::integer, turn_timeout_minutes),
    updated_at = now()
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: DeleteBotByWorkspaceAndID :execrows
DELETE FROM bots
WHERE workspace_id = $1 AND id = $2;

-- SPDX-License-Identifier: AGPL-3.0-only
