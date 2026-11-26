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

-- name: RecordFailedLogin :one
UPDATE users
SET failed_login_attempts = CASE
        WHEN locked_until IS NOT NULL THEN 1
        ELSE failed_login_attempts + 1
    END,
    locked_until = CASE
        WHEN (CASE WHEN locked_until IS NOT NULL THEN 1 ELSE failed_login_attempts + 1 END) >= sqlc.arg(threshold)::int
            THEN sqlc.arg(lock_until)::timestamptz
    END,
    updated_at = now()
WHERE id = $1 AND (locked_until IS NULL OR locked_until <= sqlc.arg(as_of)::timestamptz)
RETURNING failed_login_attempts, locked_until;

-- name: ClearFailedLogins :execrows
UPDATE users
SET failed_login_attempts = 0,
    locked_until = NULL,
    updated_at = CASE WHEN failed_login_attempts <> 0 OR locked_until IS NOT NULL THEN now() ELSE updated_at END
WHERE id = $1 AND (locked_until IS NULL OR locked_until <= sqlc.arg(as_of)::timestamptz);

-- SPDX-License-Identifier: AGPL-3.0-only
