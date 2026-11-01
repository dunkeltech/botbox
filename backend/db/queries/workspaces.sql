-- name: CreateWorkspace :one
INSERT INTO workspaces (name, slug, default_driver, default_locale)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetWorkspaceByIDForUser :one
SELECT w.*, wm.role
FROM workspaces w
JOIN workspace_members wm ON wm.workspace_id = w.id
WHERE w.id = $1 AND wm.user_id = $2;

-- name: GetWorkspaceBySlugForUser :one
SELECT w.*, wm.role
FROM workspaces w
JOIN workspace_members wm ON wm.workspace_id = w.id
WHERE w.slug = $1 AND wm.user_id = $2;

-- name: ListWorkspacesForUser :many
SELECT w.*, wm.role
FROM workspaces w
JOIN workspace_members wm ON wm.workspace_id = w.id
WHERE wm.user_id = $1
ORDER BY w.created_at ASC, w.slug ASC;

-- name: AddWorkspaceMember :one
INSERT INTO workspace_members (workspace_id, user_id, role)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetWorkspaceMember :one
SELECT wm.workspace_id, wm.user_id, wm.role, wm.joined_at, u.email, u.display_name, u.locale
FROM workspace_members wm
JOIN users u ON u.id = wm.user_id
WHERE wm.workspace_id = $1 AND wm.user_id = $2;

-- name: ListWorkspaceMembers :many
SELECT wm.workspace_id, wm.user_id, wm.role, wm.joined_at, u.email, u.display_name, u.locale
FROM workspace_members wm
JOIN users u ON u.id = wm.user_id
WHERE wm.workspace_id = $1
ORDER BY wm.joined_at ASC, u.email ASC;

-- name: UpdateWorkspaceMemberRole :one
UPDATE workspace_members
SET role = $3
WHERE workspace_id = $1 AND user_id = $2
RETURNING *;

-- name: RemoveWorkspaceMember :execrows
DELETE FROM workspace_members
WHERE workspace_id = $1 AND user_id = $2;

-- name: CountWorkspaceOwners :one
SELECT count(*)::bigint
FROM workspace_members
WHERE workspace_id = $1 AND role = 'owner';

-- name: GetWorkspaceForUpdate :one
SELECT *
FROM workspaces
WHERE id = $1
FOR UPDATE;

-- name: UpdateWorkspaceSettings :one
UPDATE workspaces
SET name = $2,
    default_driver = $3,
    default_locale = $4,
    screenshot_retention_days = $5
WHERE id = $1
RETURNING *;

-- name: CountWorkspacesForUser :one
SELECT count(*)::bigint
FROM workspace_members
WHERE user_id = $1;

-- name: HasActiveWorkspaceWork :one
SELECT (
    EXISTS (
        SELECT 1 FROM bot_turns bt
        WHERE bt.workspace_id = w.id AND bt.status IN ('running', 'waiting_secret')
    )
    OR EXISTS (
        SELECT 1 FROM secret_requests sr
        WHERE sr.workspace_id = w.id AND sr.status = 'pending'
    )
    OR EXISTS (
        SELECT 1 FROM secret_leases sl
        WHERE sl.workspace_id = w.id AND sl.revoked_at IS NULL AND sl.expires_at > now()
    )
    OR EXISTS (
        SELECT 1 FROM broker_sessions bs
        WHERE bs.workspace_id = w.id
          AND bs.revoked_at IS NULL
          AND bs.status IN ('bound', 'authenticated', 'mfa_required', 'takeover')
          AND bs.expires_at > now()
    )
)::boolean AS has_active_work
FROM workspaces w
WHERE w.id = $1;

-- name: DeleteWorkspaceConversations :exec
DELETE FROM conversations
WHERE workspace_id = $1;

-- name: DeleteWorkspaceByID :execrows
DELETE FROM workspaces
WHERE id = $1;
