-- name: OpenDirectMessage :one
INSERT INTO conversations(workspace_id,user_id,bot_id)
VALUES($1,$2,$3) ON CONFLICT(workspace_id,user_id,bot_id) WHERE type = 'dm' DO UPDATE SET user_id=EXCLUDED.user_id
RETURNING *;

-- name: AddDirectMessageMember :exec
INSERT INTO conversation_members(workspace_id,conversation_id,member_type,member_id)
VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING;

-- name: GetPrivateConversation :one
SELECT * FROM conversations WHERE workspace_id=$1 AND id=$2 AND user_id=$3;

-- name: ListDirectMessages :many
SELECT c.*,b.name AS bot_name FROM conversations c JOIN bots b ON b.workspace_id=c.workspace_id AND b.id=c.bot_id
WHERE c.workspace_id=$1 AND c.user_id=$2 ORDER BY c.created_at DESC;

-- name: CreateChatMessage :one
INSERT INTO messages(id,workspace_id,conversation_id,sender_type,sender_user_id,sender_bot_id,kind,content,metadata)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING *;

-- name: ListChatMessages :many
SELECT * FROM messages WHERE workspace_id=$1 AND conversation_id=$2
AND position < sqlc.arg(before_position)::bigint ORDER BY position DESC LIMIT sqlc.arg(page_limit)::int;

-- name: CreateBotTurn :one
INSERT INTO bot_turns(workspace_id,bot_id,conversation_id,message_id) VALUES($1,$2,$3,$4) RETURNING *;

-- name: GetBotTurn :one
SELECT * FROM bot_turns WHERE workspace_id=$1 AND id=$2;

-- name: RequestBotTurnStop :exec
UPDATE bot_turns SET stop_requested=true
WHERE workspace_id=$1 AND conversation_id=$2 AND id=$3 AND status='running' AND NOT stop_requested;

-- name: IsBotTurnStopRequested :one
SELECT stop_requested FROM bot_turns WHERE workspace_id=$1 AND id=$2;

-- name: LockRunningBotTurnForFinalization :one
SELECT stop_requested FROM bot_turns WHERE workspace_id=$1 AND id=$2 AND status='running' FOR NO KEY UPDATE;

-- name: ListConversationTurns :many
SELECT t.*, (SELECT count(*) FROM bot_turns q WHERE q.workspace_id=t.workspace_id AND q.bot_id=t.bot_id AND q.status IN ('queued','running') AND q.position<t.position)::bigint AS queue_ahead
FROM bot_turns t WHERE t.workspace_id=$1 AND t.conversation_id=$2 ORDER BY t.position DESC LIMIT 100;

-- name: NextQueuedBotTurn :one
SELECT t.* FROM bot_turns t
WHERE t.workspace_id=$1 AND t.bot_id=$2 AND t.status='queued'
  AND NOT EXISTS (SELECT 1 FROM bot_turns running WHERE running.workspace_id=$1 AND running.bot_id=$2 AND running.status='running')
  AND NOT EXISTS (SELECT 1 FROM secret_leases sl WHERE sl.workspace_id=$1 AND sl.bot_id=$2 AND sl.revoked_at IS NULL AND sl.expires_at > now() AND sl.turn_id <> t.id)
ORDER BY t.position LIMIT 1 FOR UPDATE OF t SKIP LOCKED;

-- name: UpdateBotTurn :exec
UPDATE bot_turns SET status=$3,error_code=$4,updated_at=now() WHERE workspace_id=$1 AND id=$2;

-- name: StartBotTurn :exec
UPDATE bot_turns SET status='running', error_code='', updated_at=sqlc.arg(as_of), heartbeat_at=sqlc.arg(as_of)
WHERE workspace_id=$1 AND id=$2 AND status='queued';

-- name: TouchBotTurnHeartbeat :exec
UPDATE bot_turns SET heartbeat_at=sqlc.arg(as_of)
WHERE workspace_id=$1 AND id=$2 AND status='running' AND NOT stop_requested;

-- name: SetBotTurnWaitingSecret :execrows
UPDATE bot_turns
SET status='waiting_secret', error_code='', updated_at=now()
WHERE workspace_id=$1 AND id=$2 AND status='running' AND NOT stop_requested;

-- name: ResumeBotTurnFromSecret :execrows
UPDATE bot_turns
SET status='queued', continuation_prompt=$3, error_code='', updated_at=now()
WHERE workspace_id=$1 AND id=$2 AND status='waiting_secret';

-- name: InterruptWaitingSecretTurnsForBot :execrows
UPDATE bot_turns
SET status='interrupted', error_code='engine.interrupted', updated_at=now()
WHERE workspace_id=$1 AND bot_id=$2 AND status='waiting_secret';

-- name: GetTurnMessage :one
SELECT * FROM messages WHERE workspace_id=$1 AND id=$2;

-- name: ListQueuedBots :many
SELECT DISTINCT workspace_id,bot_id FROM bot_turns WHERE workspace_id=$1 AND status='queued';

-- name: UpsertBotSession :exec
INSERT INTO bot_sessions(workspace_id,bot_id,conversation_id,runner,external_session_id) VALUES($1,$2,$3,$4,$5)
ON CONFLICT(workspace_id,bot_id,conversation_id) DO UPDATE SET runner=EXCLUDED.runner,external_session_id=EXCLUDED.external_session_id;

-- name: LockBotQueue :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(lock_key)::text, 0));

-- name: ListTurnContext :many
SELECT * FROM messages WHERE workspace_id=$1 AND conversation_id=$2 AND position <= sqlc.arg(up_to_position)::bigint AND kind='text' ORDER BY position DESC LIMIT 50;

-- name: GetBotSession :one
SELECT * FROM bot_sessions WHERE workspace_id=$1 AND bot_id=$2 AND conversation_id=$3;

-- name: ListRunningBotActivity :many
SELECT t.bot_id,CASE WHEN (SELECT m.metadata->>'Kind' FROM messages m WHERE m.workspace_id=t.workspace_id
AND m.conversation_id=t.conversation_id AND m.kind='tool' AND m.metadata->>'TurnID'=t.id::text ORDER BY m.position DESC LIMIT 1)='tool_call'
THEN 'action' ELSE 'thinking' END::text AS activity
FROM bot_turns t WHERE t.workspace_id=$1 AND t.status='running';

-- name: ExpireStaleBotTurns :exec
UPDATE bot_turns SET status='interrupted',
error_code=CASE WHEN stop_requested THEN 'engine.interrupted' ELSE 'engine.worker_lost' END,
updated_at=sqlc.arg(as_of)::timestamptz
WHERE workspace_id=$1 AND bot_id=$2 AND status='running' AND updated_at < sqlc.arg(as_of)::timestamptz - interval '20 minutes';

-- name: CountPendingBotTurns :one
SELECT count(*) FROM bot_turns WHERE workspace_id=$1 AND bot_id=$2 AND status IN ('queued','running','waiting_secret');

-- name: FinishRunningBotTurn :exec
-- Preserve the running slot until execution has returned and cleanup is done.
UPDATE bot_turns SET
status=CASE WHEN stop_requested THEN 'interrupted' ELSE sqlc.arg(status)::text END,
error_code=CASE WHEN stop_requested THEN 'engine.interrupted' ELSE sqlc.arg(error_code)::text END,
updated_at=now()
WHERE workspace_id=$1 AND id=$2 AND status='running';
