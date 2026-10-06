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
