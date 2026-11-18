-- name: InsertNotification :one
INSERT INTO notifications (workspace_id, user_id, kind, conversation_id, message_id, approval_id)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id;

-- name: ListConversationNotify :many
-- Role comes from workspace membership. Group role_text is a label, not a permission (ADR-0015).
SELECT m.member_type, m.member_id, m.notify, COALESCE(wm.role, '') AS role,
       COALESCE(u.display_name, b.name, '') AS display_name
FROM conversation_members m
LEFT JOIN users u ON m.member_type = 'user' AND u.id = m.member_id
LEFT JOIN bots b ON m.member_type = 'bot' AND b.workspace_id = m.workspace_id AND b.id = m.member_id
LEFT JOIN workspace_members wm
  ON m.member_type = 'user' AND wm.workspace_id = m.workspace_id AND wm.user_id = m.member_id
WHERE m.workspace_id = $1 AND m.conversation_id = $2
ORDER BY m.member_type, display_name;

-- name: ListWorkspaceDeciders :many
-- Owners and admins may decide a group approval without joining the group (ADR-0015).
SELECT user_id, role
FROM workspace_members
WHERE workspace_id = $1 AND role IN ('owner', 'admin')
ORDER BY user_id;

-- name: ListNotifications :many
-- The cursor is the last row already returned. id breaks created_at ties so a page cannot skip a row.
SELECT *
FROM notifications
WHERE workspace_id = sqlc.arg('workspace_id')
  AND user_id = sqlc.arg('user_id')
  AND (
    sqlc.narg('cursor_at')::timestamptz IS NULL
    OR (created_at, id) < (sqlc.narg('cursor_at')::timestamptz, sqlc.narg('cursor_id')::uuid)
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('row_limit');

-- name: MarkNotificationRead :execrows
-- A second call keeps the first read_at. Zero rows means the id is not this caller's.
UPDATE notifications
SET read_at = COALESCE(read_at, now())
WHERE workspace_id = $1 AND id = $2 AND user_id = $3;

-- name: MarkAllNotificationsRead :exec
UPDATE notifications
SET read_at = now()
WHERE workspace_id = $1 AND user_id = $2 AND read_at IS NULL;

-- SPDX-License-Identifier: AGPL-3.0-only
