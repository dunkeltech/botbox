-- name: GetCodexPlanConnection :one
SELECT * FROM codex_plan_connections WHERE workspace_id = $1 AND bot_id = $2;

-- name: LockCodexPlanConnection :one
SELECT * FROM codex_plan_connections WHERE workspace_id = $1 AND bot_id = $2 FOR UPDATE;

-- name: PutCodexPlanConnection :execrows
INSERT INTO codex_plan_connections (workspace_id, bot_id, owner_user_id, client_id, subject, revision, kek_id, wrapped_dek, encrypted_tokens)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (workspace_id, bot_id) DO UPDATE
SET revision = EXCLUDED.revision, kek_id = EXCLUDED.kek_id,
    wrapped_dek = EXCLUDED.wrapped_dek, encrypted_tokens = EXCLUDED.encrypted_tokens
WHERE codex_plan_connections.owner_user_id = EXCLUDED.owner_user_id
  AND codex_plan_connections.subject = EXCLUDED.subject
  AND codex_plan_connections.client_id = EXCLUDED.client_id;

-- name: UpdateCodexPlanTokens :execrows
UPDATE codex_plan_connections SET kek_id = $4, wrapped_dek = $5, encrypted_tokens = $6
WHERE workspace_id = $1 AND bot_id = $2 AND revision = $3;

-- name: DeleteCodexPlanConnection :exec
DELETE FROM codex_plan_connections WHERE workspace_id = $1 AND bot_id = $2;

-- name: PutCodexPlanPairing :execrows
INSERT INTO codex_plan_pairings (workspace_id, bot_id, owner_user_id, code_hash, expires_at)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (workspace_id, bot_id) DO UPDATE
SET owner_user_id = EXCLUDED.owner_user_id, code_hash = EXCLUDED.code_hash, expires_at = EXCLUDED.expires_at, claimed = FALSE
WHERE codex_plan_pairings.owner_user_id = EXCLUDED.owner_user_id
   OR codex_plan_pairings.expires_at <= sqlc.arg(as_of)::timestamptz;

-- name: ClaimCodexPlanPairing :one
UPDATE codex_plan_pairings SET claimed = TRUE
WHERE workspace_id = $1 AND bot_id = $2 AND code_hash = $3 AND NOT claimed AND expires_at > $4
RETURNING owner_user_id;

-- name: FinishCodexPlanPairing :one
DELETE FROM codex_plan_pairings
WHERE workspace_id = $1 AND bot_id = $2 AND code_hash = $3 AND claimed AND expires_at > $4
RETURNING owner_user_id;

-- name: DeleteCodexPlanPairing :exec
DELETE FROM codex_plan_pairings WHERE workspace_id = $1 AND bot_id = $2;
