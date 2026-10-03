-- +goose Up
-- Baseline schema metadata table so sqlc compiles typed database helpers in
-- Phase 0 before Phase 1 introduces the full multi-workspace schema.
CREATE TABLE IF NOT EXISTS system_info (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS system_info;
