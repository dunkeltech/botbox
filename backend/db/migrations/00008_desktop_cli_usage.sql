-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- A human can run claude in the bot computer outside a chat turn.
-- Those requests are still metered; a null turn keeps them off the chat queue.
ALTER TABLE usage_records ALTER COLUMN turn_id DROP NOT NULL;
