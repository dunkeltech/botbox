-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
ALTER TABLE environments
    ADD COLUMN last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now();

ALTER TABLE workspaces
    ADD COLUMN sandbox_idle_timeout_minutes INT
    CHECK (sandbox_idle_timeout_minutes BETWEEN 0 AND 10080);

CREATE INDEX idx_environments_status_activity
    ON environments(status, last_activity_at);

-- +goose Down
DROP INDEX IF EXISTS idx_environments_status_activity;
ALTER TABLE workspaces
    DROP COLUMN IF EXISTS sandbox_idle_timeout_minutes;
ALTER TABLE environments
    DROP COLUMN IF EXISTS last_activity_at;
