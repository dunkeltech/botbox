-- name: CreateGroupInputRequest :execrows
INSERT INTO group_input_requests (workspace_id, conversation_id, message_id, requester_bot_id, deadline, required)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (workspace_id, conversation_id, requester_bot_id) WHERE state = 'waiting' DO NOTHING;

-- name: CreateGroupInputRecipient :exec
INSERT INTO group_input_recipients (workspace_id, conversation_id, request_message_id, bot_id, bot_name, turn_id)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: GroupInputTurnAllowed :one
WITH matched AS (
    SELECT p.request_message_id AS message_id, p.bot_id AS recipient_bot_id
    FROM group_input_recipients p
    WHERE p.workspace_id = $1 AND p.conversation_id = $2 AND p.turn_id = sqlc.arg(turn_id)
    UNION ALL
    SELECT r.message_id, NULL::uuid AS recipient_bot_id
    FROM group_input_requests r
    WHERE r.workspace_id = $1 AND r.conversation_id = $2 AND r.resume_turn_id = sqlc.arg(turn_id)
)
SELECT COALESCE(m.recipient_bot_id IS NOT NULL, false)::boolean AS recipient,
       COALESCE(c.bot_talk
           AND (m.recipient_bot_id IS NULL OR (r.state = 'waiting' AND r.deadline > sqlc.arg(as_of)::timestamptz))
           AND requester.member_id IS NOT NULL
           AND (m.recipient_bot_id IS NULL OR recipient.member_id IS NOT NULL), false)::boolean AS allowed
FROM matched m
JOIN group_input_requests r ON r.workspace_id = $1 AND r.conversation_id = $2 AND r.message_id = m.message_id
JOIN conversations c ON c.workspace_id = r.workspace_id AND c.id = r.conversation_id
LEFT JOIN conversation_members requester ON requester.workspace_id = r.workspace_id AND requester.conversation_id = r.conversation_id
  AND requester.member_id = r.requester_bot_id AND requester.member_type = 'bot'
LEFT JOIN conversation_members recipient ON recipient.workspace_id = r.workspace_id AND recipient.conversation_id = r.conversation_id
  AND recipient.member_id = m.recipient_bot_id AND recipient.member_type = 'bot';

-- name: RecordGroupInputReply :exec
UPDATE group_input_recipients p SET reply_message_id = sqlc.arg(reply_message_id)
FROM group_input_requests r
WHERE p.workspace_id = $1 AND p.conversation_id = $2 AND p.turn_id = $3
  AND r.workspace_id = p.workspace_id AND r.conversation_id = p.conversation_id
  AND r.message_id = p.request_message_id AND r.state = 'waiting'
  AND r.deadline > sqlc.arg(as_of)::timestamptz;

-- name: ListGroupInputWorkspaces :many
SELECT w.id FROM workspaces w WHERE EXISTS (
    SELECT 1 FROM group_input_requests r WHERE r.workspace_id = w.id AND r.state = 'waiting'
);

-- name: ListReadyGroupInputs :many
SELECT r.conversation_id, r.message_id
FROM group_input_requests r
JOIN conversations c ON c.workspace_id = r.workspace_id AND c.id = r.conversation_id
WHERE r.workspace_id = $1 AND r.state = 'waiting'
  AND (NOT c.bot_talk OR NOT c.paused OR c.pause_reason IN ('turn_limit', 'loop'))
  AND (NOT c.bot_talk OR c.pause_reason IN ('turn_limit', 'loop') OR r.deadline <= sqlc.arg(as_of)::timestamptz OR NOT EXISTS (
      SELECT 1 FROM group_input_recipients p
      JOIN bot_turns t ON t.workspace_id = p.workspace_id AND t.conversation_id = p.conversation_id AND t.id = p.turn_id
      WHERE p.workspace_id = r.workspace_id AND p.conversation_id = r.conversation_id
        AND p.request_message_id = r.message_id AND t.status IN ('queued', 'running', 'waiting_secret')
  ))
ORDER BY r.deadline LIMIT 32;

-- name: GetWaitingGroupInput :one
SELECT * FROM group_input_requests
WHERE workspace_id = $1 AND conversation_id = $2 AND message_id = $3 AND state = 'waiting';

-- name: ListGroupInputResults :many
SELECT p.bot_id, p.bot_name, COALESCE(t.status, '')::text AS turn_status,
       COALESCE(right(m.content, 1200), '')::text AS reply
FROM group_input_recipients p
LEFT JOIN bot_turns t ON t.workspace_id = p.workspace_id AND t.conversation_id = p.conversation_id AND t.id = p.turn_id
LEFT JOIN messages m ON m.workspace_id = p.workspace_id AND m.conversation_id = p.conversation_id AND m.id = p.reply_message_id
WHERE p.workspace_id = $1 AND p.conversation_id = $2 AND p.request_message_id = $3
ORDER BY p.bot_name, p.bot_id;

-- name: CloseGroupInput :exec
UPDATE group_input_requests SET state = $4, resume_turn_id = $5
WHERE workspace_id = $1 AND conversation_id = $2 AND message_id = $3 AND state = 'waiting';

-- name: StopGroupInputRequests :exec
UPDATE group_input_requests SET state = 'stopped'
WHERE workspace_id = $1 AND conversation_id = $2 AND state = 'waiting';

-- name: ListGroupInputProgress :many
SELECT r.message_id, r.deadline, r.state, r.required, p.bot_id, p.bot_name, COALESCE(p.reply_message_id IS NOT NULL, false)::boolean AS replied,
       COALESCE(t.status IN ('queued', 'running', 'waiting_secret'), false)::boolean AS active
FROM group_input_requests r
JOIN group_input_recipients p ON p.workspace_id = r.workspace_id AND p.conversation_id = r.conversation_id
  AND p.request_message_id = r.message_id
LEFT JOIN bot_turns t ON t.workspace_id = p.workspace_id AND t.conversation_id = p.conversation_id AND t.id = p.turn_id
WHERE r.workspace_id = $1 AND r.conversation_id = $2
  AND (r.message_id = ANY(sqlc.arg(message_ids)::uuid[])
       OR (r.requester_bot_id = sqlc.narg(requester_bot_id)::uuid AND r.state = 'waiting'))
ORDER BY p.bot_name, p.bot_id;

-- name: LatestGroupTextMessage :one
SELECT id FROM messages WHERE workspace_id = $1 AND conversation_id = $2
  AND kind = 'text' AND sender_type IN ('user', 'bot') AND content <> ''
ORDER BY position DESC LIMIT 1;

-- name: CreateGroupInputTurn :one
INSERT INTO bot_turns (workspace_id, bot_id, conversation_id, message_id, continuation_prompt)
VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- SPDX-License-Identifier: AGPL-3.0-only
