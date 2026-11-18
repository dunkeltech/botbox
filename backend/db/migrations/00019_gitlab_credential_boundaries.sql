-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Retain credential boundaries after an instance is removed, so legacy wildcard
-- secrets cannot reactivate access when its registration or assignment disappears.
CREATE TABLE gitlab_known_origins (
 workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 origin VARCHAR(512) NOT NULL,
 PRIMARY KEY(workspace_id,origin)
);
INSERT INTO gitlab_known_origins(workspace_id,origin)
SELECT workspace_id,origin FROM gitlab_instances;

-- +goose StatementBegin
CREATE FUNCTION remember_gitlab_origin() RETURNS trigger AS $$
BEGIN
 INSERT INTO gitlab_known_origins(workspace_id,origin) VALUES(NEW.workspace_id,NEW.origin)
 ON CONFLICT DO NOTHING;
 RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER gitlab_remember_origin AFTER INSERT ON gitlab_instances
FOR EACH ROW EXECUTE FUNCTION remember_gitlab_origin();
