-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
ALTER TABLE users
    ADD COLUMN failed_login_attempts INT NOT NULL DEFAULT 0 CHECK (failed_login_attempts >= 0),
    ADD COLUMN locked_until TIMESTAMPTZ;

-- +goose Down
ALTER TABLE users
    DROP COLUMN IF EXISTS locked_until,
    DROP COLUMN IF EXISTS failed_login_attempts;
