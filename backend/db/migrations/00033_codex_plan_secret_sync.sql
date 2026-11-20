-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
ALTER TABLE bot_secrets
    ADD COLUMN auth_kind VARCHAR(32) NOT NULL DEFAULT '' CHECK (auth_kind IN ('', 'openai_plan', 'openai_plan_reconnect'));

ALTER TABLE workspace_secrets
    ADD COLUMN auth_kind VARCHAR(32) NOT NULL DEFAULT '' CHECK (auth_kind IN ('', 'openai_plan', 'openai_plan_reconnect'));

ALTER TABLE codex_plan_pairings
    ADD COLUMN secret_scope VARCHAR(16) NOT NULL DEFAULT 'bot' CHECK (secret_scope IN ('bot', 'workspace'));

ALTER TABLE codex_plan_connections
    ADD COLUMN secret_scope VARCHAR(16) NOT NULL DEFAULT 'bot' CHECK (secret_scope IN ('bot', 'workspace')),
    ADD COLUMN bot_secret_id UUID REFERENCES bot_secrets(id) ON DELETE SET NULL,
    ADD COLUMN workspace_secret_id UUID REFERENCES workspace_secrets(id) ON DELETE SET NULL,
    ADD CHECK (bot_secret_id IS NULL OR workspace_secret_id IS NULL);
