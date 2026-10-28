-- +goose Up
-- Per-bot exclusions preserve revocation of inherited credentials across reassignment.
ALTER TABLE workspace_secrets ADD CONSTRAINT workspace_secrets_workspace_id_id_key UNIQUE (workspace_id, id);
CREATE TABLE gitlab_workspace_secret_blocks (
 workspace_id UUID NOT NULL,
 bot_id UUID NOT NULL,
 secret_id UUID NOT NULL,
 PRIMARY KEY (workspace_id,bot_id,secret_id),
 FOREIGN KEY (workspace_id,bot_id) REFERENCES bots(workspace_id,id) ON DELETE CASCADE,
 FOREIGN KEY (workspace_id,secret_id) REFERENCES workspace_secrets(workspace_id,id) ON DELETE CASCADE
);

-- glab supplies access-token placeholders as PRIVATE-TOKEN, not Bearer authorization.
UPDATE bot_secrets SET header_name='PRIVATE-TOKEN',auth_scheme='header' WHERE adapter_id='gitlab_api';
UPDATE workspace_secrets SET header_name='PRIVATE-TOKEN',auth_scheme='header' WHERE adapter_id='gitlab_api';
UPDATE secret_leases SET header_name='PRIVATE-TOKEN',auth_scheme='header' WHERE adapter_id='gitlab_api';
