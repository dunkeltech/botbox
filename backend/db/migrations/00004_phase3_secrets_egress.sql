-- +goose Up
-- Phase 3 schema: envelope-encrypted bot/workspace secrets, per-bot egress policies,
-- and non-sensitive egress audit logs.

CREATE TABLE bot_secrets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
    name VARCHAR(128) NOT NULL CHECK (length(trim(name)) > 0),
    domain_match VARCHAR(255) NOT NULL CHECK (length(trim(domain_match)) > 0),
    match_subdomains BOOLEAN NOT NULL DEFAULT false,
    path_prefix VARCHAR(255) NOT NULL DEFAULT '',
    auth_scheme VARCHAR(32) NOT NULL CHECK (auth_scheme IN ('bearer', 'header', 'basic')),
    header_name VARCHAR(128) NOT NULL DEFAULT 'Authorization',
    username VARCHAR(128) NOT NULL DEFAULT '',
    kek_id VARCHAR(64) NOT NULL,
    wrapped_dek BYTEA NOT NULL,
    encrypted_value BYTEA NOT NULL,
    masked_preview VARCHAR(64) NOT NULL,
    used_count BIGINT NOT NULL DEFAULT 0 CHECK (used_count >= 0),
    last_used_at TIMESTAMPTZ,
    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (bot_id, name)
);

CREATE TABLE workspace_secrets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name VARCHAR(128) NOT NULL CHECK (length(trim(name)) > 0),
    domain_match VARCHAR(255) NOT NULL CHECK (length(trim(domain_match)) > 0),
    match_subdomains BOOLEAN NOT NULL DEFAULT false,
    path_prefix VARCHAR(255) NOT NULL DEFAULT '',
    auth_scheme VARCHAR(32) NOT NULL CHECK (auth_scheme IN ('bearer', 'header', 'basic')),
    header_name VARCHAR(128) NOT NULL DEFAULT 'Authorization',
    username VARCHAR(128) NOT NULL DEFAULT '',
    kek_id VARCHAR(64) NOT NULL,
    wrapped_dek BYTEA NOT NULL,
    encrypted_value BYTEA NOT NULL,
    masked_preview VARCHAR(64) NOT NULL,
    used_count BIGINT NOT NULL DEFAULT 0 CHECK (used_count >= 0),
    last_used_at TIMESTAMPTZ,
    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, name)
);

CREATE TABLE egress_rules (
    bot_id UUID PRIMARY KEY REFERENCES bots(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    policy VARCHAR(20) NOT NULL DEFAULT 'allowlist' CHECK (policy IN ('allowlist', 'open')),
    allowed_hosts TEXT[] NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE egress_log (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
    method VARCHAR(16) NOT NULL,
    host VARCHAR(255) NOT NULL,
    port VARCHAR(16) NOT NULL DEFAULT '443',
    path TEXT NOT NULL DEFAULT '',
    action VARCHAR(32) NOT NULL CHECK (action IN ('blocked_host', 'tunnel_passthrough', 'mitm_token_added')),
    status_code INT NOT NULL,
    secret_id UUID,
    secret_scope VARCHAR(16) NOT NULL DEFAULT '' CHECK (secret_scope IN ('', 'bot', 'workspace')),
    secret_name VARCHAR(128) NOT NULL DEFAULT '',
    bytes_transferred BIGINT NOT NULL DEFAULT 0 CHECK (bytes_transferred >= 0),
    latency_ms INT NOT NULL DEFAULT 0 CHECK (latency_ms >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_bot_secrets_workspace_bot ON bot_secrets(workspace_id, bot_id);
CREATE INDEX idx_workspace_secrets_workspace ON workspace_secrets(workspace_id);
CREATE INDEX idx_egress_rules_workspace ON egress_rules(workspace_id);
CREATE INDEX idx_egress_log_workspace_bot_created ON egress_log(workspace_id, bot_id, created_at DESC);
CREATE INDEX idx_egress_log_bot_action_created ON egress_log(bot_id, action, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_egress_log_bot_action_created;
DROP INDEX IF EXISTS idx_egress_log_workspace_bot_created;
DROP INDEX IF EXISTS idx_egress_rules_workspace;
DROP INDEX IF EXISTS idx_workspace_secrets_workspace;
DROP INDEX IF EXISTS idx_bot_secrets_workspace_bot;

DROP TABLE IF EXISTS egress_log;
DROP TABLE IF EXISTS egress_rules;
DROP TABLE IF EXISTS workspace_secrets;
DROP TABLE IF EXISTS bot_secrets;
