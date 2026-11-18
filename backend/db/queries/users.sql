-- name: CountUsers :one
SELECT count(*)::bigint FROM users;

-- name: AcquireSetupLock :exec
SELECT pg_advisory_xact_lock(7428101);

-- name: CreateUser :one
INSERT INTO users (email, password_hash, display_name, locale)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users
WHERE id = $1;

-- name: GetUserByEmail :one
SELECT * FROM users
WHERE email = $1;

-- name: UpdateUserPasswordHash :exec
UPDATE users
SET password_hash = $2,
    updated_at = now()
WHERE id = $1;

-- SPDX-License-Identifier: AGPL-3.0-only
