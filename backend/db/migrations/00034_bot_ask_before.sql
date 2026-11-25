-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Allow per-bot ask-before approval rules so 1:1 direct messages can opt into
-- new_host, push_protected, and custom approval cards (ADR-0015).
ALTER TABLE bots
    ADD COLUMN ask_before JSONB NOT NULL DEFAULT '[]'
    CHECK (jsonb_typeof(ask_before) = 'array');

-- +goose Down
ALTER TABLE bots
    DROP COLUMN IF EXISTS ask_before;
