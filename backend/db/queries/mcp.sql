-- name: CreateMCPServer :one
INSERT INTO mcp_servers (
    id,
    workspace_id,
    slug,
    name,
    endpoint_url,
    auth_scheme,
    auth_header_name,
    auth_username,
    kek_id,
    wrapped_dek,
    encrypted_value,
    masked_preview
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: GetMCPServer :one
SELECT * FROM mcp_servers
WHERE workspace_id = $1 AND id = $2;

-- name: GetMCPServerBySlug :one
SELECT * FROM mcp_servers
WHERE workspace_id = $1 AND slug = $2;

-- name: ListMCPServers :many
SELECT * FROM mcp_servers
WHERE workspace_id = $1
ORDER BY name ASC, slug ASC;

-- name: UpdateMCPServer :one
UPDATE mcp_servers
SET name = $3,
    endpoint_url = $4,
    auth_scheme = $5,
    auth_header_name = $6,
    auth_username = $7,
    kek_id = $8,
    wrapped_dek = $9,
    encrypted_value = $10,
    masked_preview = $11,
    updated_at = now()
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: DeleteMCPServer :execrows
DELETE FROM mcp_servers
WHERE workspace_id = $1 AND id = $2;

-- name: UpdateMCPServerSyncStatus :one
UPDATE mcp_servers
SET last_synced_at = $3,
    last_sync_error = $4,
    updated_at = now()
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: DeleteMCPServerTools :exec
DELETE FROM mcp_server_tools
WHERE workspace_id = $1 AND server_id = $2;

-- name: UpsertMCPServerTool :one
INSERT INTO mcp_server_tools (
    server_id,
    workspace_id,
    tool_name,
    description,
    input_schema,
    discovered_at
)
VALUES ($1, $2, $3, $4, $5, now())
ON CONFLICT (server_id, tool_name) DO UPDATE SET
    description = EXCLUDED.description,
    input_schema = EXCLUDED.input_schema,
    discovered_at = now()
RETURNING *;

-- name: ListMCPServerTools :many
SELECT * FROM mcp_server_tools
WHERE workspace_id = $1 AND server_id = $2
ORDER BY tool_name ASC;

-- name: ListWorkspaceMCPServerTools :many
SELECT * FROM mcp_server_tools
WHERE workspace_id = $1
ORDER BY server_id ASC, tool_name ASC;

-- name: LockBotMCPPolicy :exec
SELECT pg_advisory_xact_lock(hashtext('mcp:' || $1::uuid::text), hashtext($2::uuid::text));

-- name: GetBotMCPAssignment :one
SELECT * FROM bot_mcp_assignments
WHERE workspace_id = $1 AND bot_id = $2 AND server_id = $3;

-- name: ListBotMCPAssignments :many
SELECT * FROM bot_mcp_assignments
WHERE workspace_id = $1 AND bot_id = $2
ORDER BY server_id ASC;

-- name: UpsertBotMCPAssignment :one
INSERT INTO bot_mcp_assignments (
    workspace_id,
    bot_id,
    server_id,
    enabled,
    default_policy,
    revision
)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (workspace_id, bot_id, server_id) DO UPDATE SET
    enabled = EXCLUDED.enabled,
    default_policy = EXCLUDED.default_policy,
    revision = EXCLUDED.revision,
    updated_at = now()
RETURNING *;

-- name: AcknowledgeBotMCPPolicyRevision :exec
UPDATE bot_mcp_assignments
SET acknowledged_revision = GREATEST(acknowledged_revision, $4::bigint),
    acknowledged_at = CASE
        WHEN $4::bigint >= acknowledged_revision THEN now()
        ELSE acknowledged_at
    END
WHERE workspace_id = $1 AND bot_id = $2 AND server_id = $3;

-- name: ListBotMCPToolPolicies :many
SELECT * FROM bot_mcp_tool_policies
WHERE workspace_id = $1 AND bot_id = $2
ORDER BY server_id ASC, tool_name ASC;

-- name: ListBotMCPToolPoliciesForServer :many
SELECT * FROM bot_mcp_tool_policies
WHERE workspace_id = $1 AND bot_id = $2 AND server_id = $3
ORDER BY tool_name ASC;

-- name: DeleteBotMCPToolPoliciesForServer :exec
DELETE FROM bot_mcp_tool_policies
WHERE workspace_id = $1 AND bot_id = $2 AND server_id = $3;

-- name: UpsertBotMCPToolPolicy :one
INSERT INTO bot_mcp_tool_policies (
    workspace_id,
    bot_id,
    server_id,
    tool_name,
    policy
)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (workspace_id, bot_id, server_id, tool_name) DO UPDATE SET
    policy = EXCLUDED.policy,
    updated_at = now()
RETURNING *;

-- name: CreateMCPToolGrant :one
INSERT INTO mcp_tool_grants (
    id,
    workspace_id,
    bot_id,
    server_id,
    tool_name,
    args_sha256,
    approval_id,
    status
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetApprovedMCPToolGrant :one
SELECT * FROM mcp_tool_grants
WHERE workspace_id = $1
  AND bot_id = $2
  AND server_id = $3
  AND tool_name = $4
  AND args_sha256 = $5
  AND status = 'approved'
ORDER BY created_at ASC
LIMIT 1
FOR UPDATE;

-- name: ConsumeMCPToolGrant :execrows
UPDATE mcp_tool_grants
SET status = 'consumed'
WHERE workspace_id = $1 AND id = $2 AND status = 'approved';

-- name: UpdateMCPToolGrantByApproval :execrows
UPDATE mcp_tool_grants
SET status = $3,
    decided_at = now()
WHERE workspace_id = $1 AND approval_id = $2 AND status = 'pending';

-- name: CreateMCPCallLog :one
INSERT INTO mcp_call_log (
    workspace_id,
    bot_id,
    server_id,
    server_slug,
    tool_name,
    action,
    status_code,
    latency_ms
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: ListBotMCPCallLogs :many
SELECT * FROM mcp_call_log
WHERE workspace_id = $1
  AND bot_id = $2
  AND ($3::text = '' OR action = $3::text)
ORDER BY created_at DESC, id DESC
LIMIT $4 OFFSET $5;

-- name: ListBotAssignedMCPServerSlugs :many
SELECT s.slug
FROM bot_mcp_assignments a
JOIN mcp_servers s ON s.workspace_id = a.workspace_id AND s.id = a.server_id
WHERE a.workspace_id = $1 AND a.bot_id = $2 AND a.enabled = true
ORDER BY s.slug ASC;

-- name: GetConversationUserID :one
SELECT user_id, type FROM conversations
WHERE workspace_id = $1 AND id = $2;

-- name: DeleteBotSessionsByBot :exec
DELETE FROM bot_sessions
WHERE workspace_id = $1 AND bot_id = $2;

-- name: DeleteBotSessionsByWorkspace :exec
DELETE FROM bot_sessions
WHERE workspace_id = $1;
