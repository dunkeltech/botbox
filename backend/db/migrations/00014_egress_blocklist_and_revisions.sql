-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Explicit per-bot egress blocklist, monotonic concurrency revisions,
-- proxy enforcement acknowledgements, and durable invalidation intents (#31).

ALTER TABLE egress_rules
    ADD COLUMN blocked_hosts TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN revision BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0),
    ADD COLUMN acknowledged_revision BIGINT NOT NULL DEFAULT 0 CHECK (acknowledged_revision >= 0),
    ADD COLUMN acknowledged_at TIMESTAMPTZ;

CREATE TABLE egress_invalidation_intents (
    bot_id UUID PRIMARY KEY REFERENCES bots(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    target_revision BIGINT NOT NULL CHECK (target_revision >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_egress_invalidation_intents_workspace
    ON egress_invalidation_intents(workspace_id, target_revision);

-- +goose Down
DROP INDEX IF EXISTS idx_egress_invalidation_intents_workspace;
DROP TABLE IF EXISTS egress_invalidation_intents;

ALTER TABLE egress_rules
    DROP COLUMN IF EXISTS acknowledged_at,
    DROP COLUMN IF EXISTS acknowledged_revision,
    DROP COLUMN IF EXISTS revision,
    DROP COLUMN IF EXISTS blocked_hosts;
