-- name: CreateAuditLog :one
INSERT INTO audit_log (workspace_id, actor_user_id, action, target_type, target_id, metadata)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListAuditLogsByWorkspace :many
SELECT *
FROM audit_log
WHERE workspace_id = $1
ORDER BY created_at DESC
LIMIT $2;

-- name: ListAuditLogsByWorkspaceFiltered :many
SELECT a.id,
       a.workspace_id,
       a.actor_user_id,
       COALESCE(u.email, '')::text AS actor_email,
       COALESCE(u.display_name, '')::text AS actor_display_name,
       a.action,
       COALESCE(a.target_type, '')::text AS target_type,
       a.target_id,
       a.metadata,
       a.created_at
FROM audit_log a
LEFT JOIN users u ON u.id = a.actor_user_id
WHERE a.workspace_id = sqlc.arg(workspace_id)
  AND (sqlc.narg(action)::text IS NULL OR a.action = sqlc.narg(action)::text)
  AND (sqlc.narg(target_type)::text IS NULL OR a.target_type = sqlc.narg(target_type)::text)
  AND (sqlc.narg(since)::timestamptz IS NULL OR a.created_at >= sqlc.narg(since)::timestamptz)
  AND (sqlc.narg(until)::timestamptz IS NULL OR a.created_at <= sqlc.narg(until)::timestamptz)
ORDER BY a.created_at DESC, a.id DESC
LIMIT sqlc.arg(page_limit)
OFFSET sqlc.arg(page_offset);

-- name: CountAuditLogsByWorkspaceFiltered :one
SELECT count(*)::bigint AS total
FROM audit_log a
WHERE a.workspace_id = sqlc.arg(workspace_id)
  AND (sqlc.narg(action)::text IS NULL OR a.action = sqlc.narg(action)::text)
  AND (sqlc.narg(target_type)::text IS NULL OR a.target_type = sqlc.narg(target_type)::text)
  AND (sqlc.narg(since)::timestamptz IS NULL OR a.created_at >= sqlc.narg(since)::timestamptz)
  AND (sqlc.narg(until)::timestamptz IS NULL OR a.created_at <= sqlc.narg(until)::timestamptz);

-- SPDX-License-Identifier: AGPL-3.0-only

-- SPDX-License-Identifier: AGPL-3.0-only
