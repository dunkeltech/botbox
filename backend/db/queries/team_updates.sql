-- name: LockTeamToolTurn :one
SELECT t.* FROM bot_turns t
JOIN conversation_members member ON member.workspace_id=t.workspace_id
  AND member.conversation_id=t.conversation_id AND member.member_type='bot' AND member.member_id=t.bot_id
WHERE t.workspace_id=$1 AND t.conversation_id=$2 AND t.id=$3 AND t.bot_id=$4
  AND t.status='running' AND NOT t.stop_requested AND t.heartbeat_at >= sqlc.arg(fresh_since)::timestamptz
FOR NO KEY UPDATE OF t;

-- name: FindTeamPeer :many
SELECT b.id FROM conversation_members member
JOIN bots b ON b.workspace_id=member.workspace_id AND b.id=member.member_id
WHERE member.workspace_id=$1 AND member.conversation_id=$2 AND member.member_type='bot'
  AND lower(b.name)=lower(sqlc.arg(bot_name)::text)
LIMIT 2;

-- name: GetTeamPeerActivity :one
SELECT b.name,
       COALESCE(t.status, 'idle')::text AS turn_status,
       COALESCE(t.stop_requested, false)::boolean AS stop_requested,
       t.updated_at, t.heartbeat_at,
       EXISTS (
           SELECT 1 FROM approvals a
           WHERE a.workspace_id=b.workspace_id AND a.conversation_id=member.conversation_id
             AND a.bot_id=b.id AND a.status='pending' AND a.payload->>'triggerMessageId'=t.message_id::text
       ) AS awaiting_approval,
       COALESCE(progress.content, '')::text AS progress,
       progress.created_at AS progress_at
FROM conversation_members member
JOIN bots b ON b.workspace_id=member.workspace_id AND b.id=member.member_id
LEFT JOIN LATERAL (
    SELECT t.id, t.status, t.stop_requested, t.updated_at, t.heartbeat_at, t.message_id FROM bot_turns t
    WHERE t.workspace_id=member.workspace_id AND t.conversation_id=member.conversation_id AND t.bot_id=b.id
    ORDER BY CASE t.status WHEN 'running' THEN 0 WHEN 'waiting_secret' THEN 1 WHEN 'queued' THEN 2 ELSE 3 END,
             t.position DESC
    LIMIT 1
) t ON true
LEFT JOIN LATERAL (
    SELECT m.content, m.created_at FROM messages m
    WHERE m.workspace_id=member.workspace_id AND m.conversation_id=member.conversation_id
      AND m.kind='progress' AND m.metadata->>'turnId'=t.id::text
    ORDER BY m.position DESC LIMIT 1
) progress ON true
WHERE member.workspace_id=$1 AND member.conversation_id=$2 AND member.member_type='bot' AND member.member_id=$3;

-- name: ListTeamTurnUpdates :many
SELECT kind, content, metadata, created_at FROM messages
WHERE workspace_id=$1 AND conversation_id=$2 AND kind IN ('progress', 'team_status')
  AND metadata->>'turnId'=sqlc.arg(turn_id)::text
ORDER BY position DESC;

-- name: CreateTeamUpdate :exec
INSERT INTO messages(workspace_id, conversation_id, sender_type, sender_bot_id, kind, content, metadata, created_at)
VALUES ($1, $2, 'bot', $3, $4, $5, $6, $7);
