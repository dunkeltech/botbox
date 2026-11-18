-- name: CreateRefreshTokenFamily :exec
INSERT INTO refresh_token_families (id) VALUES ($1);

-- name: CreateRefreshToken :one
INSERT INTO refresh_tokens (user_id, family_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetRefreshTokenByHash :one
SELECT * FROM refresh_tokens
WHERE token_hash = $1;

-- name: LockRefreshTokenFamilyByHash :one
SELECT f.* FROM refresh_token_families f
JOIN refresh_tokens t ON t.family_id = f.id
WHERE t.token_hash = $1
FOR UPDATE OF f;

-- name: ReplaceRefreshToken :exec
UPDATE refresh_tokens
SET revoked_at = $3,
    replaced_by = $2
WHERE id = $1;

-- name: RevokeRefreshTokenFamily :exec
UPDATE refresh_token_families
SET revoked_at = COALESCE(revoked_at, $2)
WHERE id = $1;

-- name: RevokeRefreshTokenByHash :exec
UPDATE refresh_tokens
SET revoked_at = COALESCE(revoked_at, $2)
WHERE token_hash = $1;

-- SPDX-License-Identifier: AGPL-3.0-only
