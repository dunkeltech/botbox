-- +goose Up
-- Screenshot bytes expire; the action row stays so the timeline remains readable.
ALTER TABLE workspaces
    ADD COLUMN screenshot_retention_days INTEGER NOT NULL DEFAULT 7
    CHECK (screenshot_retention_days BETWEEN 1 AND 30);

CREATE TABLE desktop_actions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    bot_id UUID NOT NULL,
    turn_id UUID,
    session_id TEXT NOT NULL,
    agent_seq BIGINT NOT NULL CHECK (agent_seq > 0),
    kind TEXT NOT NULL,
    params JSONB NOT NULL DEFAULT '{}',
    result TEXT NOT NULL DEFAULT '',
    error_code TEXT NOT NULL DEFAULT '',
    screenshot BYTEA,
    screenshot_mime TEXT NOT NULL DEFAULT '',
    coord_x INTEGER NOT NULL DEFAULT 0,
    coord_y INTEGER NOT NULL DEFAULT 0,
    has_coord BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id),
    UNIQUE (workspace_id, bot_id, session_id, agent_seq),
    FOREIGN KEY (workspace_id, bot_id) REFERENCES bots (workspace_id, id) ON DELETE CASCADE
);

CREATE INDEX desktop_actions_bot_created
    ON desktop_actions (workspace_id, bot_id, created_at DESC);
