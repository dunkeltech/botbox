-- name: ListWorkspaceSecretsForKEKRotation :many
SELECT id, workspace_id, kek_id, wrapped_dek, encrypted_value
FROM workspace_secrets
WHERE kek_id <> $1
ORDER BY created_at ASC, id ASC
FOR UPDATE;

-- name: UpdateWorkspaceSecretWrappedDEK :exec
UPDATE workspace_secrets
SET kek_id = $3,
    wrapped_dek = $4,
    updated_at = now()
WHERE workspace_id = $1 AND id = $2;

-- name: ListBotSecretsForKEKRotation :many
SELECT id, workspace_id, kek_id, wrapped_dek, encrypted_value
FROM bot_secrets
WHERE kek_id <> $1
ORDER BY created_at ASC, id ASC
FOR UPDATE;

-- name: UpdateBotSecretWrappedDEK :exec
UPDATE bot_secrets
SET kek_id = $3,
    wrapped_dek = $4,
    updated_at = now()
WHERE workspace_id = $1 AND id = $2;

-- name: ListSecretLeasesForKEKRotation :many
SELECT id, workspace_id, kek_id, wrapped_dek, encrypted_value
FROM secret_leases
WHERE kek_id <> $1
ORDER BY created_at ASC, id ASC
FOR UPDATE;

-- name: UpdateSecretLeaseWrappedDEK :exec
UPDATE secret_leases
SET kek_id = $3,
    wrapped_dek = $4
WHERE workspace_id = $1 AND id = $2;

-- name: ListMCPServersForKEKRotation :many
SELECT id, workspace_id, kek_id, wrapped_dek, encrypted_value
FROM mcp_servers
WHERE wrapped_dek IS NOT NULL
  AND encrypted_value IS NOT NULL
  AND kek_id <> $1
ORDER BY created_at ASC, id ASC
FOR UPDATE;

-- name: UpdateMCPServerWrappedDEK :exec
UPDATE mcp_servers
SET kek_id = $3,
    wrapped_dek = $4,
    updated_at = now()
WHERE workspace_id = $1 AND id = $2;

-- name: ListCodexPlanConnectionsForKEKRotation :many
SELECT workspace_id, bot_id, revision, kek_id, wrapped_dek, encrypted_tokens
FROM codex_plan_connections
WHERE kek_id IS NOT NULL
  AND wrapped_dek IS NOT NULL
  AND kek_id <> $1
ORDER BY workspace_id ASC, bot_id ASC
FOR UPDATE;

-- name: UpdateCodexPlanConnectionWrappedDEK :exec
UPDATE codex_plan_connections
SET kek_id = $4,
    wrapped_dek = $5
WHERE workspace_id = $1 AND bot_id = $2 AND revision = $3;

-- SPDX-License-Identifier: AGPL-3.0-only
