-- name: GetBotRuntime :one
SELECT COALESCE(r.paused,false)::boolean AS paused,
       COALESCE(e.control_holder_user_id IS NOT NULL,false)::boolean AS controlled
FROM bots b LEFT JOIN bot_runtime r ON r.workspace_id=b.workspace_id AND r.bot_id=b.id
LEFT JOIN environments e ON e.bot_id=b.id
WHERE b.workspace_id=$1 AND b.id=$2;

-- name: SetBotPaused :exec
INSERT INTO bot_runtime(workspace_id,bot_id,paused) VALUES($1,$2,$3)
ON CONFLICT(workspace_id,bot_id) DO UPDATE SET paused=EXCLUDED.paused;

-- name: CanRunACPComputerTool :one
SELECT EXISTS (
    SELECT 1
    FROM bot_turns AS t
    JOIN bots AS b ON b.workspace_id = t.workspace_id AND b.id = t.bot_id
    JOIN environments AS e ON e.bot_id = b.id
    LEFT JOIN bot_runtime AS r ON r.workspace_id = b.workspace_id AND r.bot_id = b.id
    WHERE t.workspace_id = sqlc.arg(workspace_id)
      AND t.bot_id = sqlc.arg(bot_id)
      AND t.id = sqlc.arg(turn_id)
      AND t.status = 'running' AND NOT t.stop_requested
      AND b.llm_mode = 'cli' AND b.llm_provider = 'google-acp'
      AND e.env_id = sqlc.arg(env_id) AND e.status = 'running'
      AND e.control_holder_user_id IS NULL AND NOT COALESCE(r.paused, false)
);

-- SPDX-License-Identifier: AGPL-3.0-only
