-- name: InsertDesktopAction :one
INSERT INTO desktop_actions (
    id, workspace_id, bot_id, turn_id, session_id, agent_seq, kind, params,
    result, error_code, screenshot, screenshot_mime, coord_x, coord_y, has_coord, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16
)
ON CONFLICT (workspace_id, bot_id, session_id, agent_seq) DO UPDATE
SET id = desktop_actions.id
RETURNING id;

-- name: ListDesktopActions :many
SELECT id, workspace_id, bot_id, turn_id, session_id, agent_seq, kind, params,
    result, error_code, screenshot_mime,
    (screenshot IS NOT NULL)::boolean AS has_screenshot,
    coord_x, coord_y, has_coord, created_at
FROM desktop_actions
WHERE workspace_id = $1 AND bot_id = $2
ORDER BY created_at DESC, agent_seq DESC
LIMIT sqlc.arg(page_limit)::int;

-- name: GetDesktopActionScreenshot :one
SELECT screenshot, screenshot_mime
FROM desktop_actions
WHERE workspace_id = $1 AND bot_id = $2 AND id = $3;

-- name: ExpireDesktopScreenshots :exec
UPDATE desktop_actions
SET screenshot = NULL, screenshot_mime = ''
WHERE workspace_id = $1
  AND screenshot IS NOT NULL
  AND created_at < sqlc.arg(as_of)::timestamptz - make_interval(days => sqlc.arg(retention_days)::int);

-- name: UpdateScreenshotRetention :one
UPDATE workspaces
SET screenshot_retention_days = $2
WHERE id = $1
RETURNING *;

-- name: LatestBotTurn :one
SELECT * FROM bot_turns
WHERE workspace_id = $1 AND bot_id = $2
ORDER BY position DESC
LIMIT 1;

-- name: LatestConversationMessage :one
SELECT * FROM messages
WHERE workspace_id = $1 AND conversation_id = $2
ORDER BY position DESC
LIMIT 1;

-- SPDX-License-Identifier: AGPL-3.0-only
