-- +goose Up
ALTER TABLE bots ADD COLUMN turn_timeout_minutes integer NOT NULL DEFAULT 10
    CHECK (turn_timeout_minutes BETWEEN 1 AND 120);

ALTER TABLE bot_turns ADD COLUMN run_deadline_at timestamptz;
