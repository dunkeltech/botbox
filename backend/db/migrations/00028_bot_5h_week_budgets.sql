-- +goose Up
-- Allow 5-hour rolling and weekly UTC caps on bot budgets while keeping
-- workspace-level budgets restricted to monthly via the existing table check.
ALTER TABLE budgets DROP CONSTRAINT IF EXISTS budgets_period_check;
ALTER TABLE budgets ADD CONSTRAINT budgets_period_check
    CHECK (period IN ('5h', 'day', 'week', 'month'));
