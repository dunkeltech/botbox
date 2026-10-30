-- name: ListGitLabInstances :many
SELECT * FROM gitlab_instances WHERE workspace_id = $1 ORDER BY origin;

-- name: GetGitLabInstance :one
SELECT * FROM gitlab_instances WHERE workspace_id = $1 AND origin = $2;

-- name: LockGitLabInstance :one
SELECT * FROM gitlab_instances WHERE workspace_id = $1 AND origin = $2 FOR SHARE;

-- name: CreateGitLabInstance :one
INSERT INTO gitlab_instances (workspace_id, origin, name)
SELECT $1, $2, $3 WHERE (SELECT count(*) FROM gitlab_instances WHERE workspace_id = $1) < 20
RETURNING *;

-- name: DeleteGitLabInstance :execrows
DELETE FROM gitlab_instances WHERE workspace_id = $1 AND origin = $2;

-- name: RevokeGitLabInstanceSessions :many
UPDATE broker_sessions SET status = 'revoked', revoked_at = now(), capture_suppressed = TRUE,
    takeover_user_id = NULL, updated_at = now()
WHERE workspace_id = $1 AND origin = $2 AND adapter_id = 'gitlab_web' AND revoked_at IS NULL
RETURNING id;

-- name: LockGitLabWorkspace :one
SELECT id FROM workspaces WHERE id = $1 FOR UPDATE;

-- name: CancelGitLabInstanceRequests :many
UPDATE secret_requests SET status = 'cancelled', error_code = 'instance_removed',
    operation_id = NULL, updated_at = now()
WHERE workspace_id = $1 AND target_origin = $2 AND kind IN ('browser_login','cli_egress')
    AND adapter_id IN ('gitlab_web','gitlab_api','gitlab_git') AND status IN ('pending', 'fulfilling')
RETURNING *;

-- name: ListBotGitLabInstances :many
SELECT gi.* FROM bot_gitlab_instances bgi
JOIN gitlab_instances gi ON gi.workspace_id = bgi.workspace_id AND gi.origin = bgi.origin
WHERE bgi.workspace_id = $1 AND bgi.bot_id = $2 ORDER BY gi.origin;

-- name: LockBotGitLabInstance :one
SELECT origin FROM bot_gitlab_instances WHERE workspace_id = $1 AND bot_id = $2 AND origin = $3 FOR SHARE;

-- name: AssignBotGitLabInstance :exec
INSERT INTO bot_gitlab_instances (workspace_id, bot_id, origin) VALUES ($1, $2, $3);

-- name: RemoveBotGitLabInstance :exec
DELETE FROM bot_gitlab_instances WHERE workspace_id = $1 AND bot_id = $2 AND origin = $3;

-- name: RevokeBotGitLabInstanceSessions :many
UPDATE broker_sessions SET status = 'revoked', revoked_at = now(), capture_suppressed = TRUE,
    takeover_user_id = NULL, updated_at = now()
WHERE workspace_id = $1 AND bot_id = $2 AND origin = $3 AND adapter_id = 'gitlab_web' AND revoked_at IS NULL
RETURNING id;

-- name: CancelBotGitLabInstanceRequests :many
UPDATE secret_requests SET status = 'cancelled', error_code = 'instance_removed', operation_id = NULL, updated_at = now()
WHERE workspace_id = $1 AND bot_id = $2 AND target_origin = $3 AND kind IN ('browser_login','cli_egress')
    AND adapter_id IN ('gitlab_web','gitlab_api','gitlab_git') AND status IN ('pending', 'fulfilling')
RETURNING *;

-- name: BlockGitLabWorkspaceSecretsForBot :exec
INSERT INTO gitlab_workspace_secret_blocks(workspace_id,bot_id,secret_id)
SELECT s.workspace_id,sqlc.arg(bot_id)::uuid,s.id FROM workspace_secrets s
WHERE s.workspace_id=$1 AND s.domain_match=$2 AND s.adapter_id IN ('gitlab_api','gitlab_git')
ON CONFLICT DO NOTHING;

-- name: ListBlockedGitLabWorkspaceSecretsForBot :many
SELECT secret_id FROM gitlab_workspace_secret_blocks WHERE workspace_id=$1 AND bot_id=$2;

-- name: HasSavedGitLabCredential :one
SELECT EXISTS (
    SELECT 1 FROM bot_secrets s
    WHERE s.workspace_id = sqlc.arg(workspace_id)::uuid AND s.bot_id = sqlc.arg(bot_id)::uuid
      AND s.adapter_id = sqlc.arg(adapter_id)::varchar AND s.domain_match = sqlc.arg(domain_match)::varchar
      AND NOT s.match_subdomains AND s.path_prefix IN ('', '/')
    UNION ALL
    SELECT 1 FROM workspace_secrets s
    WHERE s.workspace_id = sqlc.arg(workspace_id)::uuid
      AND s.adapter_id = sqlc.arg(adapter_id)::varchar AND s.domain_match = sqlc.arg(domain_match)::varchar
      AND NOT s.match_subdomains AND s.path_prefix IN ('', '/')
      AND NOT EXISTS (
          SELECT 1 FROM gitlab_workspace_secret_blocks b
          WHERE b.workspace_id = s.workspace_id AND b.bot_id = sqlc.arg(bot_id)::uuid AND b.secret_id = s.id
      )
);

-- name: UnblockGitLabWorkspaceSecretForBot :exec
DELETE FROM gitlab_workspace_secret_blocks WHERE workspace_id=$1 AND bot_id=$2 AND secret_id=$3;

-- name: DeleteBotGitLabCredentials :exec
DELETE FROM bot_secrets WHERE workspace_id=$1 AND bot_id=$2 AND domain_match=$3 AND adapter_id IN ('gitlab_api','gitlab_git');

-- name: DeleteInstanceBotGitLabCredentials :exec
DELETE FROM bot_secrets WHERE workspace_id=$1 AND domain_match=$2 AND adapter_id IN ('gitlab_api','gitlab_git');

-- name: DeleteInstanceWorkspaceGitLabCredentials :exec
DELETE FROM workspace_secrets WHERE workspace_id=$1 AND domain_match=$2 AND adapter_id IN ('gitlab_api','gitlab_git');

-- name: RevokeBotGitLabLeases :exec
UPDATE secret_leases SET revoked_at=now(),revocation_reason='instance_removed'
WHERE workspace_id=$1 AND bot_id=$2 AND domain_match=$3 AND adapter_id IN ('gitlab_api','gitlab_git') AND revoked_at IS NULL;

-- name: RevokeInstanceGitLabLeases :exec
UPDATE secret_leases SET revoked_at=now(),revocation_reason='instance_removed'
WHERE workspace_id=$1 AND domain_match=$2 AND adapter_id IN ('gitlab_api','gitlab_git') AND revoked_at IS NULL;

-- name: BindBotSecretAdapter :exec
UPDATE bot_secrets SET adapter_id=$4 WHERE workspace_id=$1 AND bot_id=$2 AND id=$3;

-- name: BindWorkspaceSecretAdapter :exec
UPDATE workspace_secrets SET adapter_id=$3 WHERE workspace_id=$1 AND id=$2;

-- name: ListGitLabAssignedBots :many
SELECT bot_id FROM bot_gitlab_instances WHERE workspace_id=$1 AND origin=$2;

-- name: ListKnownGitLabOrigins :many
SELECT origin FROM gitlab_known_origins WHERE workspace_id=$1 ORDER BY origin;
