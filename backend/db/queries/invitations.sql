-- name: CreateInvitation :one
INSERT INTO invitations (workspace_id, email, role, token_hash, created_by, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListPendingInvitationsByWorkspace :many
SELECT *
FROM invitations
WHERE workspace_id = $1 AND accepted_at IS NULL
ORDER BY created_at DESC;

-- name: GetInvitationByTokenHash :one
SELECT i.*, w.name AS workspace_name, w.slug AS workspace_slug
FROM invitations i
JOIN workspaces w ON w.id = i.workspace_id
WHERE i.token_hash = $1;

-- name: GetInvitationByTokenHashForUpdate :one
SELECT i.*, w.name AS workspace_name, w.slug AS workspace_slug
FROM invitations i
JOIN workspaces w ON w.id = i.workspace_id
WHERE i.token_hash = $1
FOR UPDATE OF i;

-- name: MarkInvitationAccepted :one
UPDATE invitations
SET accepted_at = $2
WHERE id = $1 AND accepted_at IS NULL
RETURNING *;

-- name: DeleteInvitationByWorkspaceAndID :execrows
DELETE FROM invitations
WHERE workspace_id = $1 AND id = $2;
