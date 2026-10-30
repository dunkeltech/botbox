-- +goose Up
-- A pending request reserves capacity without holding a database connection.
-- Uncertain settlement clears the reservation and retains unknown cost.
ALTER TABLE usage_records ADD COLUMN reserved_micros bigint CHECK (reserved_micros >= 0);
