-- name: CreateAskApproval :one
-- One pending card per group, kind, and public payload. A repeat of the same
-- blocked host or label does not stack cards (ADR-0015).
INSERT INTO approvals(workspace_id, bot_id, conversation_id, kind, payload)
VALUES ($1, $2, $3, sqlc.arg(kind), sqlc.arg(payload))
ON CONFLICT (workspace_id, conversation_id, kind, payload)
    WHERE status = 'pending' AND kind <> 'raise_budget'
DO NOTHING
RETURNING id;

-- name: GetPendingApprovalByPayload :one
SELECT id FROM approvals
WHERE workspace_id = $1
  AND conversation_id = $2
  AND kind = sqlc.arg(kind)
  AND payload = sqlc.arg(payload)
  AND status = 'pending'
LIMIT 1;

-- name: AttachApprovalMessage :exec
UPDATE approvals SET message_id = $3 WHERE workspace_id = $1 AND id = $2;

-- name: PauseGroupForApproval :exec
-- Leave a turn-limit, loop, or budget pause in place. Only an unpaused group
-- becomes an approval pause (ADR-0015).
UPDATE conversations
SET paused = true, pause_reason = 'approval'
WHERE workspace_id = $1 AND id = $2 AND type = 'group' AND paused = false;

-- name: ClearApprovalPause :execrows
UPDATE conversations
SET paused = false, pause_reason = ''
WHERE workspace_id = $1 AND id = $2 AND type = 'group'
  AND paused = true AND pause_reason = 'approval';

-- name: GetApprovalForUpdate :one
SELECT * FROM approvals WHERE workspace_id = $1 AND id = $2 FOR UPDATE;

-- name: DecideApproval :execrows
UPDATE approvals
SET status = $3, decided_by = $4, decided_at = now()
WHERE workspace_id = $1 AND id = $2 AND status = 'pending';

-- name: ListVisibleApprovals :many
SELECT a.*
FROM approvals a
WHERE a.workspace_id = $1
  AND (sqlc.narg(status)::text IS NULL OR a.status = sqlc.narg(status)::text)
  AND (
    (
      a.conversation_id IS NOT NULL
      AND (
        EXISTS (
          SELECT 1 FROM conversation_members m
          WHERE m.workspace_id = a.workspace_id
            AND m.conversation_id = a.conversation_id
            AND m.member_type = 'user'
            AND m.member_id = sqlc.arg(member_id)
        )
        OR EXISTS (
          SELECT 1 FROM conversations c
          WHERE c.workspace_id = a.workspace_id
            AND c.id = a.conversation_id
            AND c.type = 'dm'
            AND c.user_id = sqlc.arg(member_id)
        )
      )
    )
    OR (
      sqlc.arg(include_workspace_budgets)::bool
      AND a.conversation_id IS NULL
      AND a.budget_id IS NOT NULL
    )
  )
ORDER BY a.created_at DESC
LIMIT 100;

-- name: LatestRunnableGroupMessage :one
SELECT id FROM messages
WHERE workspace_id = $1 AND conversation_id = $2 AND kind = 'text'
  AND sender_type IN ('user', 'bot')
ORDER BY position DESC
LIMIT 1;

-- name: GetBudgetForUpdate :one
SELECT * FROM budgets WHERE workspace_id = $1 AND id = $2 FOR UPDATE;
