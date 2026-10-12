-- name: GetBotRuntime :one
SELECT COALESCE(r.paused,false)::boolean AS paused,
       COALESCE(e.control_holder_user_id IS NOT NULL,false)::boolean AS controlled
FROM bots b LEFT JOIN bot_runtime r ON r.workspace_id=b.workspace_id AND r.bot_id=b.id
LEFT JOIN environments e ON e.bot_id=b.id
WHERE b.workspace_id=$1 AND b.id=$2;

-- name: SetBotPaused :exec
INSERT INTO bot_runtime(workspace_id,bot_id,paused) VALUES($1,$2,$3)
ON CONFLICT(workspace_id,bot_id) DO UPDATE SET paused=EXCLUDED.paused;
