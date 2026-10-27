-- +goose Up
-- Zero-context in-chat secret requests and bounded ephemeral egress leases (ADR-0016, issue #30).

ALTER TABLE bot_turns DROP CONSTRAINT bot_turns_status_check;
ALTER TABLE bot_turns
    ADD COLUMN continuation_prompt TEXT NOT NULL DEFAULT '',
    ADD CONSTRAINT bot_turns_status_check CHECK (
        status IN ('queued', 'running', 'waiting_secret', 'completed', 'failed', 'interrupted', 'budget_exceeded')
    );

ALTER TABLE messages DROP CONSTRAINT messages_kind_check;
ALTER TABLE messages
    ADD CONSTRAINT messages_kind_check CHECK (
        kind IN ('text', 'tool', 'system', 'card', 'approval', 'secret_request')
    );

ALTER TABLE conversations DROP CONSTRAINT conversations_pause_reason_check;
ALTER TABLE conversations
    ADD CONSTRAINT conversations_pause_reason_check CHECK (
        pause_reason IN ('', 'turn_limit', 'loop', 'approval', 'budget', 'secret_required')
    );

ALTER TABLE bot_secrets
    ADD COLUMN adapter_id VARCHAR(64) NOT NULL DEFAULT '';

ALTER TABLE workspace_secrets
    ADD COLUMN adapter_id VARCHAR(64) NOT NULL DEFAULT '';

ALTER TABLE egress_log DROP CONSTRAINT egress_log_secret_scope_check;
ALTER TABLE egress_log
    ADD CONSTRAINT egress_log_secret_scope_check CHECK (secret_scope IN ('', 'bot', 'workspace', 'lease'));

CREATE TABLE secret_requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    bot_id UUID NOT NULL,
    conversation_id UUID NOT NULL,
    message_id UUID,
    turn_id UUID NOT NULL,
    tool_call_id TEXT NOT NULL DEFAULT '',
    kind VARCHAR(32) NOT NULL CHECK (kind IN ('cli_egress', 'browser_login')),
    adapter_id VARCHAR(64) NOT NULL DEFAULT '',
    target_origin VARCHAR(512) NOT NULL DEFAULT '',
    reason TEXT NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 1024),
    env_placeholder_name VARCHAR(64) NOT NULL DEFAULT '',
    placeholder_value VARCHAR(128) NOT NULL DEFAULT '',
    username_hint VARCHAR(255) NOT NULL DEFAULT '',
    status VARCHAR(24) NOT NULL DEFAULT 'pending' CHECK (
        status IN ('pending', 'fulfilling', 'fulfilled', 'denied', 'expired', 'cancelled', 'failed')
    ),
    operation_id UUID,
    granted_scope VARCHAR(24) NOT NULL DEFAULT '' CHECK (
        granted_scope IN ('', 'ephemeral', 'bot_secret', 'workspace_secret')
    ),
    decided_by UUID REFERENCES users(id) ON DELETE SET NULL,
    decided_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL,
    error_code VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id),
    FOREIGN KEY (workspace_id, bot_id) REFERENCES bots(workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, conversation_id) REFERENCES conversations(workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, message_id) REFERENCES messages(workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, turn_id) REFERENCES bot_turns(workspace_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_secret_requests_workspace_conv_created
    ON secret_requests(workspace_id, conversation_id, created_at DESC);
CREATE INDEX idx_secret_requests_workspace_bot_status
    ON secret_requests(workspace_id, bot_id, status, expires_at);
CREATE INDEX idx_secret_requests_turn
    ON secret_requests(workspace_id, turn_id);

-- Ephemeral proxy leases are strictly bounded by expires_at and revoked_at.
-- Unlike persistent secrets, leases never expand egress_rules.allowed_hosts.
CREATE TABLE secret_leases (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    bot_id UUID NOT NULL,
    secret_request_id UUID NOT NULL,
    turn_id UUID NOT NULL,
    conversation_id UUID NOT NULL,
    adapter_id VARCHAR(64) NOT NULL,
    domain_match VARCHAR(255) NOT NULL,
    port VARCHAR(16) NOT NULL DEFAULT '443',
    auth_scheme VARCHAR(32) NOT NULL CHECK (auth_scheme IN ('bearer', 'header', 'basic')),
    header_name VARCHAR(128) NOT NULL DEFAULT 'Authorization',
    username VARCHAR(128) NOT NULL DEFAULT '',
    placeholder_env VARCHAR(64) NOT NULL DEFAULT '',
    placeholder_value VARCHAR(128) NOT NULL DEFAULT '',
    kek_id VARCHAR(64) NOT NULL,
    wrapped_dek BYTEA NOT NULL,
    encrypted_value BYTEA NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    revocation_reason VARCHAR(64) NOT NULL DEFAULT '',
    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (workspace_id, bot_id) REFERENCES bots(workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, secret_request_id) REFERENCES secret_requests(workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, turn_id) REFERENCES bot_turns(workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, conversation_id) REFERENCES conversations(workspace_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_secret_leases_active
    ON secret_leases(workspace_id, bot_id, expires_at)
    WHERE revoked_at IS NULL;
CREATE INDEX idx_secret_leases_turn
    ON secret_leases(workspace_id, turn_id)
    WHERE revoked_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_secret_leases_turn;
DROP INDEX IF EXISTS idx_secret_leases_active;
DROP TABLE IF EXISTS secret_leases;

DROP INDEX IF EXISTS idx_secret_requests_turn;
DROP INDEX IF EXISTS idx_secret_requests_workspace_bot_status;
DROP INDEX IF EXISTS idx_secret_requests_workspace_conv_created;
DROP TABLE IF EXISTS secret_requests;

ALTER TABLE egress_log DROP CONSTRAINT IF EXISTS egress_log_secret_scope_check;
ALTER TABLE egress_log
    ADD CONSTRAINT egress_log_secret_scope_check CHECK (secret_scope IN ('', 'bot', 'workspace'));
ALTER TABLE workspace_secrets DROP COLUMN IF EXISTS adapter_id;
ALTER TABLE bot_secrets DROP COLUMN IF EXISTS adapter_id;

ALTER TABLE conversations DROP CONSTRAINT IF EXISTS conversations_pause_reason_check;
ALTER TABLE conversations
    ADD CONSTRAINT conversations_pause_reason_check CHECK (
        pause_reason IN ('', 'turn_limit', 'loop', 'approval', 'budget')
    );

ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_kind_check;
ALTER TABLE messages
    ADD CONSTRAINT messages_kind_check CHECK (
        kind IN ('text', 'tool', 'system', 'card', 'approval')
    );

ALTER TABLE bot_turns DROP CONSTRAINT IF EXISTS bot_turns_status_check;
ALTER TABLE bot_turns
    DROP COLUMN IF EXISTS continuation_prompt,
    ADD CONSTRAINT bot_turns_status_check CHECK (
        status IN ('queued', 'running', 'completed', 'failed', 'interrupted', 'budget_exceeded')
    );
