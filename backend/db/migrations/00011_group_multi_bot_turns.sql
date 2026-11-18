-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- One group message can schedule more than one member bot. The Phase 4 unique
-- key allowed a single turn per message, so a handoff had nowhere to go (ADR-0015).
ALTER TABLE bot_turns DROP CONSTRAINT bot_turns_workspace_id_message_id_key;
