-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
ALTER TABLE workspace_members
    ADD COLUMN role_description text NOT NULL DEFAULT ''
    CHECK (char_length(role_description) <= 4000);

-- +goose Down
ALTER TABLE workspace_members
    DROP COLUMN role_description;
