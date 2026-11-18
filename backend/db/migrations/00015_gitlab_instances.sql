-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Origins are immutable. Removing and re-adding an origin never restores old sessions.
CREATE TABLE gitlab_instances (
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    origin VARCHAR(512) NOT NULL,
    name VARCHAR(100) NOT NULL,
    PRIMARY KEY (workspace_id, origin)
);
