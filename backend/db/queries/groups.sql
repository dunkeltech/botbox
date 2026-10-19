-- name: InsertGroup :one
-- The client supplies the id so a replay of the same create is idempotent (ADR-0015).
INSERT INTO conversations(
    id, workspace_id, type, name, goal, lead_bot_id, bot_talk, turn_limit,
    answer_policy, ask_before, budget_limit_micros
) VALUES ($1, $2, 'group', $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: GetGroupForMember :one
SELECT c.id, c.workspace_id, c.name, c.goal, c.lead_bot_id, c.bot_talk, c.turn_limit,
       c.answer_policy, c.ask_before, c.budget_limit_micros, c.turns_since_human,
       c.paused, c.pause_reason, c.summary, c.rr_index, m.last_read_position,
       (SELECT count(*) FROM messages msg
         WHERE msg.workspace_id = c.workspace_id AND msg.conversation_id = c.id
           AND msg.position > m.last_read_position)::bigint AS unread
FROM conversations c
JOIN conversation_members m
  ON m.workspace_id = c.workspace_id AND m.conversation_id = c.id
 AND m.member_type = 'user' AND m.member_id = $3
WHERE c.workspace_id = $1 AND c.id = $2 AND c.type = 'group';

-- name: ListGroupsForMember :many
SELECT c.id, c.workspace_id, c.name, c.goal, c.lead_bot_id, c.bot_talk, c.turn_limit,
       c.answer_policy, c.ask_before, c.budget_limit_micros, c.turns_since_human,
       c.paused, c.pause_reason, c.summary, c.rr_index, m.last_read_position,
       (SELECT count(*) FROM messages msg
         WHERE msg.workspace_id = c.workspace_id AND msg.conversation_id = c.id
           AND msg.position > m.last_read_position)::bigint AS unread
FROM conversations c
JOIN conversation_members m
  ON m.workspace_id = c.workspace_id AND m.conversation_id = c.id
 AND m.member_type = 'user' AND m.member_id = $2
WHERE c.workspace_id = $1 AND c.type = 'group'
ORDER BY c.created_at DESC;

-- name: UpsertGroupMember :exec
-- Conflict updates the role only. last_read_position and notify stay, so saving
-- settings does not mark the group unread or reset a member's notification choice.
INSERT INTO conversation_members(workspace_id, conversation_id, member_type, member_id, role_text)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (workspace_id, conversation_id, member_type, member_id)
DO UPDATE SET role_text = EXCLUDED.role_text;

-- name: DeleteGroupMember :exec
DELETE FROM conversation_members
WHERE workspace_id = $1 AND conversation_id = $2
  AND member_type = $3 AND member_id = $4;

-- name: ListGroupMemberRows :many
SELECT m.member_type, m.member_id, m.role_text,
       COALESCE(u.display_name, b.name, '') AS display_name
FROM conversation_members m
LEFT JOIN users u ON m.member_type = 'user' AND u.id = m.member_id
LEFT JOIN bots b ON m.member_type = 'bot' AND b.workspace_id = m.workspace_id AND b.id = m.member_id
WHERE m.workspace_id = $1 AND m.conversation_id = $2
ORDER BY m.member_type, display_name;

-- name: UpdateGroupSettings :execrows
UPDATE conversations
SET name = $3, goal = $4, lead_bot_id = $5, bot_talk = $6, turn_limit = $7,
    answer_policy = $8, ask_before = $9, budget_limit_micros = $10
WHERE workspace_id = $1 AND id = $2 AND type = 'group';

-- name: GetGroup :one
SELECT * FROM conversations WHERE workspace_id = $1 AND id = $2 AND type = 'group';

-- name: GetGroupForUpdate :one
SELECT * FROM conversations WHERE workspace_id = $1 AND id = $2 AND type = 'group' FOR UPDATE;

-- name: GetConversationType :one
SELECT type FROM conversations WHERE workspace_id = $1 AND id = $2;

-- name: ApplyGroupPlan :exec
UPDATE conversations
SET turns_since_human = $3, paused = $4, pause_reason = $5, rr_index = $6, summary = $7
WHERE workspace_id = $1 AND id = $2 AND type = 'group';

-- name: IncrementGroupTurnsSinceHuman :execrows
UPDATE conversations
SET turns_since_human = turns_since_human + 1
WHERE workspace_id = $1 AND id = $2 AND type = 'group';

-- name: ListRecentGroupBotSpeakers :many
SELECT sender_bot_id FROM messages
WHERE workspace_id = $1 AND conversation_id = $2
  AND sender_type = 'bot' AND kind = 'text' AND sender_bot_id IS NOT NULL
ORDER BY position DESC
LIMIT 4;

-- name: ListGroupTranscript :many
SELECT sender_type, sender_user_id, sender_bot_id, content
FROM messages
WHERE workspace_id = $1 AND conversation_id = $2 AND kind = 'text'
ORDER BY position ASC;

-- name: GroupSpend :one
SELECT
    COALESCE(sum(u.cost_micros) FILTER (WHERE u.cost_micros IS NOT NULL), 0)::bigint AS spent_micros,
    (CASE WHEN bool_or(u.cost_micros IS NULL AND u.billing_kind = 'metered') THEN 1 ELSE 0 END)::int AS unknown_rows
FROM usage_records u
JOIN bot_turns t ON t.workspace_id = u.workspace_id AND t.id = u.turn_id
WHERE u.workspace_id = $1 AND t.conversation_id = $2 AND u.turn_id IS NOT NULL;

-- name: HasGroupBudgetWarning :one
SELECT EXISTS(
    SELECT 1 FROM messages
    WHERE workspace_id = $1 AND conversation_id = $2 AND kind = 'system'
      AND metadata->>'budgetWarning' = 'true'
) AS warned;

-- name: CreateGroupBudgetApproval :exec
INSERT INTO approvals(workspace_id, bot_id, conversation_id, kind)
VALUES ($1, $2, $3, 'raise_budget')
ON CONFLICT (workspace_id, conversation_id) WHERE status = 'pending' AND kind = 'raise_budget' AND budget_id IS NULL
DO NOTHING;

-- name: MarkGroupRead :execrows
UPDATE conversation_members
SET last_read_position = COALESCE((
    SELECT max(position) FROM messages WHERE messages.workspace_id = $1 AND messages.conversation_id = $2
), 0)
WHERE workspace_id = $1 AND conversation_id = $2 AND member_type = 'user' AND member_id = $3;
