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
       e.last_activity_at,
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

-- name: InitializeEnvironment :one
INSERT INTO environments (bot_id, driver, env_id, status, image, cpu_millis, memory_mib, volume_ref)
SELECT b.id, sqlc.arg(driver), '', 'stopped', sqlc.arg(image),
       sqlc.arg(cpu_millis), sqlc.arg(memory_mib), sqlc.arg(volume_ref)
FROM bots b
WHERE b.workspace_id = sqlc.arg(workspace_id) AND b.id = sqlc.arg(bot_id)
ON CONFLICT (bot_id) DO UPDATE SET bot_id = EXCLUDED.bot_id
RETURNING *;

-- name: MarkEnvironmentStopped :one
UPDATE environments e
SET status = 'stopped', started_at = NULL, control_holder_user_id = NULL, updated_at = now()
FROM bots b
WHERE e.bot_id = b.id AND b.workspace_id = sqlc.arg(workspace_id) AND b.id = sqlc.arg(bot_id)
RETURNING e.*;

-- name: MarkIdleEnvironmentStopped :one
UPDATE environments e
SET status = 'stopped', started_at = NULL, updated_at = now()
FROM bots b
JOIN workspaces w ON w.id = b.workspace_id
WHERE e.bot_id = b.id AND b.workspace_id = sqlc.arg(workspace_id) AND b.id = sqlc.arg(bot_id)
  AND e.status = 'running' AND e.env_id <> '' AND e.control_holder_user_id IS NULL
  AND COALESCE(w.sandbox_idle_timeout_minutes * interval '1 minute', sqlc.arg(default_timeout)::interval) > interval '0'
  AND e.last_activity_at <= sqlc.arg(as_of)::timestamptz
      - COALESCE(w.sandbox_idle_timeout_minutes * interval '1 minute', sqlc.arg(default_timeout)::interval)
  AND NOT EXISTS (
      SELECT 1 FROM bot_turns t
      WHERE t.workspace_id = b.workspace_id AND t.bot_id = b.id
        AND t.status IN ('queued', 'running', 'waiting_secret')
  )
RETURNING e.*;

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
    started_at,
    last_activity_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, COALESCE($11, now()))
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
    last_activity_at = COALESCE(EXCLUDED.started_at, now()),
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

-- name: CountEnvironmentsByState :many
SELECT status, count(*)::bigint AS total
FROM environments
GROUP BY status;

-- name: TouchEnvironmentActivity :exec
UPDATE environments e
SET last_activity_at = sqlc.arg(as_of)::timestamptz
FROM bots b
WHERE e.bot_id = b.id
  AND b.workspace_id = sqlc.arg(workspace_id)
  AND b.id = sqlc.arg(bot_id);

-- name: ListIdleRunningEnvironments :many
SELECT e.bot_id,
       b.workspace_id,
       e.env_id,
       e.last_activity_at,
       e.started_at,
       e.control_holder_user_id,
       w.sandbox_idle_timeout_minutes
FROM environments e
JOIN bots b ON b.id = e.bot_id
JOIN workspaces w ON w.id = b.workspace_id
WHERE e.status = 'running'
  AND e.control_holder_user_id IS NULL
  AND NOT EXISTS (
      SELECT 1
      FROM bot_turns bt
      WHERE bt.workspace_id = b.workspace_id
        AND bt.bot_id = b.id
        AND bt.status IN ('queued', 'running', 'waiting_secret')
  );

-- name: LockHostCapacity :exec
SELECT pg_advisory_xact_lock(7428102);

-- name: RunningEnvironmentCapacity :one
SELECT count(*)::bigint AS running_count,
       COALESCE(sum(memory_mib), 0)::bigint AS running_memory_mib
FROM environments
WHERE status IN ('starting', 'running')
  AND bot_id <> sqlc.arg(exclude_bot_id);

-- SPDX-License-Identifier: AGPL-3.0-only
