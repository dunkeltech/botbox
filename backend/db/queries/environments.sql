-- name: GetEnvironmentByWorkspaceAndBot :one
SELECT e.bot_id,
       e.driver,
       e.env_id,
       e.status,
       e.image,
       e.cpu_millis,
       e.memory_mib,
       e.volume_ref,
       e.control_holder_user_id,
       e.control_note,
       e.started_at,
       e.updated_at,
       COALESCE(u.display_name, '')::text AS control_holder_display_name,
       COALESCE(u.email, '')::text AS control_holder_email
FROM environments e
JOIN bots b ON b.id = e.bot_id
LEFT JOIN users u ON u.id = e.control_holder_user_id
WHERE b.workspace_id = $1 AND b.id = $2;

-- name: CanAccessDesktop :one
SELECT EXISTS (
    SELECT 1 FROM bots b
    JOIN workspace_members wm ON wm.workspace_id = b.workspace_id
    JOIN environments e ON e.bot_id = b.id
    WHERE b.workspace_id = sqlc.arg(workspace_id) AND b.id = sqlc.arg(bot_id)
      AND wm.user_id = sqlc.arg(user_id) AND e.status = 'running'
      AND (NOT sqlc.arg(can_control)::boolean
           OR (wm.role <> 'viewer' AND e.control_holder_user_id = wm.user_id))
) AS allowed;

-- name: ListEnvironmentsByWorkspace :many
SELECT e.*
FROM environments e
JOIN bots b ON b.id = e.bot_id
WHERE b.workspace_id = $1;

-- name: UpsertEnvironment :one
INSERT INTO environments (
    bot_id,
    driver,
    env_id,
    status,
    image,
    cpu_millis,
    memory_mib,
    volume_ref,
    control_holder_user_id,
    control_note,
    started_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (bot_id) DO UPDATE SET
    driver = EXCLUDED.driver,
    env_id = EXCLUDED.env_id,
    status = EXCLUDED.status,
    image = EXCLUDED.image,
    cpu_millis = EXCLUDED.cpu_millis,
    memory_mib = EXCLUDED.memory_mib,
    volume_ref = EXCLUDED.volume_ref,
    control_holder_user_id = EXCLUDED.control_holder_user_id,
    control_note = EXCLUDED.control_note,
    started_at = EXCLUDED.started_at,
    updated_at = now()
RETURNING *;

-- name: UpdateEnvironmentStatus :one
UPDATE environments
SET status = $2,
    started_at = $3,
    updated_at = now()
WHERE bot_id = $1
RETURNING *;

-- name: ClaimEnvironmentControl :one
UPDATE environments
SET control_holder_user_id = $2,
    control_note = '',
    updated_at = now()
WHERE bot_id = $1
  AND (control_holder_user_id IS NULL OR control_holder_user_id = $2)
RETURNING *;

-- name: ReleaseEnvironmentControl :one
UPDATE environments
SET control_holder_user_id = NULL,
    control_note = $3,
    updated_at = now()
WHERE bot_id = $1
  AND (control_holder_user_id = $2 OR $4::boolean = true)
RETURNING *;

-- name: ListEnvironmentsForReconcile :many
SELECT e.*, b.workspace_id
FROM environments e
JOIN bots b ON b.id = e.bot_id;

-- SPDX-License-Identifier: AGPL-3.0-only
