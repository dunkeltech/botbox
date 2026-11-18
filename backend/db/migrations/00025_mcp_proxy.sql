-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Workspace-scoped MCP servers, discovered tools, per-bot command policies,
-- one-time approval grants, and proxy audit logs (ADR-0029).

CREATE TABLE mcp_servers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    slug VARCHAR(64) NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$'),
    name VARCHAR(100) NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 100),
    endpoint_url VARCHAR(512) NOT NULL,
    auth_scheme VARCHAR(20) NOT NULL DEFAULT 'none' CHECK (auth_scheme IN ('none', 'bearer', 'header', 'basic')),
    auth_header_name VARCHAR(100) NOT NULL DEFAULT 'Authorization',
    auth_username VARCHAR(255) NOT NULL DEFAULT '',
    kek_id VARCHAR(64) NOT NULL DEFAULT '',
    wrapped_dek BYTEA,
    encrypted_value BYTEA,
    masked_preview VARCHAR(64) NOT NULL DEFAULT '',
    last_synced_at TIMESTAMPTZ,
    last_sync_error VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id),
    UNIQUE (workspace_id, slug)
);

CREATE TABLE mcp_server_tools (
    server_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    tool_name VARCHAR(64) NOT NULL CHECK (tool_name ~ '^[A-Za-z0-9_-]{1,64}$'),
    description TEXT NOT NULL DEFAULT '',
    input_schema JSONB NOT NULL DEFAULT '{"type":"object"}',
    discovered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (server_id, tool_name),
    FOREIGN KEY (workspace_id, server_id) REFERENCES mcp_servers(workspace_id, id) ON DELETE CASCADE
);

CREATE TABLE bot_mcp_assignments (
    workspace_id UUID NOT NULL,
    bot_id UUID NOT NULL,
    server_id UUID NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    default_policy VARCHAR(10) NOT NULL DEFAULT 'deny' CHECK (default_policy IN ('allow', 'ask', 'deny')),
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision >= 1),
    acknowledged_revision BIGINT NOT NULL DEFAULT 0 CHECK (acknowledged_revision >= 0),
    acknowledged_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, bot_id, server_id),
    FOREIGN KEY (workspace_id, bot_id) REFERENCES bots(workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, server_id) REFERENCES mcp_servers(workspace_id, id) ON DELETE CASCADE
);

CREATE TABLE bot_mcp_tool_policies (
    workspace_id UUID NOT NULL,
    bot_id UUID NOT NULL,
    server_id UUID NOT NULL,
    tool_name VARCHAR(64) NOT NULL CHECK (tool_name ~ '^[A-Za-z0-9_-]{1,64}$'),
    policy VARCHAR(10) NOT NULL CHECK (policy IN ('allow', 'ask', 'deny')),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, bot_id, server_id, tool_name),
    FOREIGN KEY (workspace_id, bot_id, server_id) REFERENCES bot_mcp_assignments(workspace_id, bot_id, server_id) ON DELETE CASCADE
);

CREATE TABLE mcp_tool_grants (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    bot_id UUID NOT NULL,
    server_id UUID NOT NULL,
    tool_name VARCHAR(64) NOT NULL,
    args_sha256 VARCHAR(64) NOT NULL DEFAULT '',
    approval_id UUID,
    status VARCHAR(20) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'denied', 'consumed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at TIMESTAMPTZ,
    FOREIGN KEY (workspace_id, bot_id) REFERENCES bots(workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, server_id) REFERENCES mcp_servers(workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, approval_id) REFERENCES approvals(workspace_id, id) ON DELETE CASCADE
);

CREATE INDEX mcp_tool_grants_lookup
    ON mcp_tool_grants (workspace_id, bot_id, server_id, tool_name, args_sha256, status);

CREATE TABLE mcp_call_log (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    bot_id UUID NOT NULL,
    server_id UUID,
    server_slug VARCHAR(64) NOT NULL,
    tool_name VARCHAR(64) NOT NULL,
    action VARCHAR(20) NOT NULL CHECK (action IN ('allowed', 'blocked', 'ask_pending', 'approved_by_human', 'denied_by_human', 'upstream_error')),
    status_code INT NOT NULL DEFAULT 0,
    latency_ms INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (workspace_id, bot_id) REFERENCES bots(workspace_id, id) ON DELETE CASCADE
);

CREATE INDEX mcp_call_log_bot_created ON mcp_call_log (workspace_id, bot_id, created_at DESC);

ALTER TABLE approvals DROP CONSTRAINT approvals_kind_check;
ALTER TABLE approvals
    ADD CONSTRAINT approvals_kind_check CHECK (kind IN ('raise_budget', 'new_host', 'push_protected', 'custom', 'mcp_tool'));
