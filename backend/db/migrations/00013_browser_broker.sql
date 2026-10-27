-- +goose Up
-- Isolated browser authentication broker session state and atomic target bindings (ADR-0016, issue #30).

CREATE TABLE broker_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    bot_id UUID NOT NULL,
    conversation_id UUID NOT NULL,
    turn_id UUID NOT NULL,
    adapter_id VARCHAR(64) NOT NULL,
    origin VARCHAR(512) NOT NULL,
    target_id TEXT NOT NULL DEFAULT '',
    frame_id TEXT NOT NULL DEFAULT '',
    document_id TEXT NOT NULL DEFAULT '',
    form_action TEXT NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT 'bound' CHECK (
        status IN ('bound', 'authenticated', 'mfa_required', 'takeover', 'revoked', 'expired')
    ),
    capture_suppressed BOOLEAN NOT NULL DEFAULT TRUE,
    takeover_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id),
    FOREIGN KEY (workspace_id, bot_id) REFERENCES bots(workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, conversation_id) REFERENCES conversations(workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, turn_id) REFERENCES bot_turns(workspace_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_broker_sessions_workspace_bot_active
    ON broker_sessions(workspace_id, bot_id, expires_at)
    WHERE revoked_at IS NULL;

CREATE INDEX idx_broker_sessions_workspace_turn
    ON broker_sessions(workspace_id, turn_id);

ALTER TABLE secret_requests
    ADD COLUMN broker_session_id UUID,
    ADD COLUMN broker_target_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN broker_frame_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN broker_document_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN broker_form_action TEXT NOT NULL DEFAULT '',
    ADD CONSTRAINT secret_requests_broker_session_fk
        FOREIGN KEY (workspace_id, broker_session_id)
        REFERENCES broker_sessions(workspace_id, id) ON DELETE SET NULL (broker_session_id);

-- +goose Down
ALTER TABLE secret_requests
    DROP CONSTRAINT IF EXISTS secret_requests_broker_session_fk,
    DROP COLUMN IF EXISTS broker_form_action,
    DROP COLUMN IF EXISTS broker_document_id,
    DROP COLUMN IF EXISTS broker_frame_id,
    DROP COLUMN IF EXISTS broker_target_id,
    DROP COLUMN IF EXISTS broker_session_id;

DROP INDEX IF EXISTS idx_broker_sessions_workspace_turn;
DROP INDEX IF EXISTS idx_broker_sessions_workspace_bot_active;
DROP TABLE IF EXISTS broker_sessions;
