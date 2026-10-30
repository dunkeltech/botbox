-- +goose Up
-- Keep the stop request on the turn so a missed signal cannot lose it.
ALTER TABLE bot_turns ADD COLUMN stop_requested BOOLEAN NOT NULL DEFAULT false;
