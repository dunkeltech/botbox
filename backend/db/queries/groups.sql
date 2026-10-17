-- name: InsertGroup :one
INSERT INTO conversations(
    workspace_id, type, name, goal, lead_bot_id, bot_talk, turn_limit,
    answer_policy, ask_before, budget_limit_micros
) VALUES ($1, 'group', $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: ListGroupColumns :one
SELECT id, workspace_id, type, name, goal, lead_bot_id, bot_talk, turn_limit,
       answer_policy, ask_before, budget_limit_micros, turns_since_human,
       paused, pause_reason, summary, rr_index
FROM conversations
WHERE workspace_id = $1 AND id = $2 AND type = 'group';
