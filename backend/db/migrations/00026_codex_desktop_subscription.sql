-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
ALTER TABLE workspaces
    ADD COLUMN count_subscription_usage BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE usage_records
    ADD COLUMN subscription_budgeted BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN internal_charge_micros BIGINT CHECK (internal_charge_micros >= 0),
    ADD COLUMN usage_known BOOLEAN,
    ADD CHECK (NOT subscription_budgeted OR billing_kind = 'subscription'),
    ADD CHECK (internal_charge_micros IS NULL OR subscription_budgeted);

CREATE TABLE codex_plan_connections (
    workspace_id UUID NOT NULL,
    bot_id UUID NOT NULL,
    owner_user_id UUID NOT NULL,
    client_id TEXT NOT NULL,
    subject TEXT NOT NULL,
    revision UUID NOT NULL,
    kek_id TEXT,
    wrapped_dek BYTEA,
    encrypted_tokens BYTEA,
    PRIMARY KEY (workspace_id, bot_id),
    FOREIGN KEY (workspace_id, bot_id) REFERENCES bots(workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, owner_user_id) REFERENCES workspace_members(workspace_id, user_id) ON DELETE CASCADE,
    CHECK ((kek_id IS NULL AND wrapped_dek IS NULL AND encrypted_tokens IS NULL) OR
           (kek_id IS NOT NULL AND wrapped_dek IS NOT NULL AND encrypted_tokens IS NOT NULL))
);

CREATE TABLE codex_plan_pairings (
    workspace_id UUID NOT NULL,
    bot_id UUID NOT NULL,
    owner_user_id UUID NOT NULL,
    code_hash BYTEA NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    claimed BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (workspace_id, bot_id),
    FOREIGN KEY (workspace_id, bot_id) REFERENCES bots(workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, owner_user_id) REFERENCES workspace_members(workspace_id, user_id) ON DELETE CASCADE
);
