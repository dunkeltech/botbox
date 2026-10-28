-- +goose Up
-- Workspace registration never grants every bot access implicitly.
CREATE TABLE bot_gitlab_instances (
    workspace_id UUID NOT NULL,
    bot_id UUID NOT NULL,
    origin VARCHAR(512) NOT NULL,
    PRIMARY KEY (workspace_id, bot_id, origin),
    FOREIGN KEY (workspace_id, bot_id) REFERENCES bots(workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, origin) REFERENCES gitlab_instances(workspace_id, origin) ON DELETE CASCADE
);

-- Changing to explicit assignment invalidates previous implicit grants.
UPDATE broker_sessions SET status = 'revoked', revoked_at = now(), capture_suppressed = TRUE,
    takeover_user_id = NULL, updated_at = now()
WHERE adapter_id = 'gitlab_web' AND origin <> 'https://gitlab.com:443' AND revoked_at IS NULL;
