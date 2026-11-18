-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Pause survives API restarts and applies to every conversation of this bot.
CREATE TABLE bot_runtime (
 workspace_id UUID NOT NULL,
 bot_id UUID NOT NULL,
 paused BOOLEAN NOT NULL DEFAULT false,
 PRIMARY KEY (workspace_id,bot_id),
 FOREIGN KEY (workspace_id,bot_id) REFERENCES bots(workspace_id,id) ON DELETE CASCADE
);
