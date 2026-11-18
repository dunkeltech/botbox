-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Workspace-scoped LLM proxy guardrails: opt-in policies, custom regex/keyword
-- rules, sanitized proxy audit events, and inline chat guardrail cards.

CREATE TABLE workspace_guardrail_policies (
    workspace_id UUID PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    enabled BOOLEAN NOT NULL DEFAULT false,
    failure_mode VARCHAR(16) NOT NULL DEFAULT 'fail_closed' CHECK (failure_mode IN ('fail_closed', 'fail_open')),
    secrets_enabled BOOLEAN NOT NULL DEFAULT true,
    secrets_direction VARCHAR(10) NOT NULL DEFAULT 'both' CHECK (secrets_direction IN ('input', 'output', 'both')),
    secrets_action VARCHAR(16) NOT NULL DEFAULT 'block' CHECK (secrets_action IN ('block', 'redact', 'audit')),
    pii_enabled BOOLEAN NOT NULL DEFAULT true,
    pii_direction VARCHAR(10) NOT NULL DEFAULT 'both' CHECK (pii_direction IN ('input', 'output', 'both')),
    pii_action VARCHAR(16) NOT NULL DEFAULT 'redact' CHECK (pii_action IN ('block', 'redact', 'audit')),
    pii_entities TEXT[] NOT NULL DEFAULT ARRAY['email', 'iban', 'card', 'phone'],
    prompt_injection_enabled BOOLEAN NOT NULL DEFAULT true,
    prompt_injection_direction VARCHAR(10) NOT NULL DEFAULT 'input' CHECK (prompt_injection_direction IN ('input', 'output', 'both')),
    prompt_injection_action VARCHAR(16) NOT NULL DEFAULT 'audit' CHECK (prompt_injection_action IN ('block', 'redact', 'audit')),
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision >= 1),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE workspace_guardrail_rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    slug VARCHAR(64) NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$'),
    name VARCHAR(100) NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 100),
    match_mode VARCHAR(16) NOT NULL CHECK (match_mode IN ('regex', 'keywords')),
    pattern TEXT NOT NULL CHECK (char_length(btrim(pattern)) BETWEEN 1 AND 2000),
    direction VARCHAR(10) NOT NULL CHECK (direction IN ('input', 'output', 'both')),
    action VARCHAR(16) NOT NULL CHECK (action IN ('block', 'redact', 'audit')),
    replacement_token VARCHAR(80) NOT NULL DEFAULT '[REDACTED]',
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id),
    UNIQUE (workspace_id, slug)
);

CREATE INDEX workspace_guardrail_rules_ws_created
    ON workspace_guardrail_rules (workspace_id, created_at ASC, id ASC);

CREATE TABLE llm_guardrail_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    bot_id UUID NOT NULL,
    conversation_id UUID,
    turn_id UUID,
    provider VARCHAR(32) NOT NULL,
    model VARCHAR(200) NOT NULL DEFAULT '',
    route_path VARCHAR(200) NOT NULL DEFAULT '',
    direction VARCHAR(10) NOT NULL CHECK (direction IN ('input', 'output')),
    action VARCHAR(16) NOT NULL CHECK (action IN ('allowed', 'audited', 'redacted', 'blocked')),
    status_code INT NOT NULL DEFAULT 200,
    latency_ms INT NOT NULL DEFAULT 0,
    rule_slugs TEXT[] NOT NULL DEFAULT '{}',
    match_labels TEXT[] NOT NULL DEFAULT '{}',
    match_count INT NOT NULL DEFAULT 0 CHECK (match_count >= 0),
    summary VARCHAR(240) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (workspace_id, bot_id) REFERENCES bots(workspace_id, id) ON DELETE CASCADE
);

CREATE INDEX llm_guardrail_events_ws_created
    ON llm_guardrail_events (workspace_id, created_at DESC, id DESC);

CREATE INDEX llm_guardrail_events_ws_action_created
    ON llm_guardrail_events (workspace_id, action, created_at DESC);

CREATE INDEX llm_guardrail_events_ws_turn_action
    ON llm_guardrail_events (workspace_id, turn_id, action)
    WHERE turn_id IS NOT NULL;

ALTER TABLE messages DROP CONSTRAINT messages_kind_check;
ALTER TABLE messages ADD CONSTRAINT messages_kind_check
    CHECK (kind IN ('text', 'tool', 'system', 'card', 'approval', 'secret_request', 'progress', 'team_status', 'memory', 'guardrail'));

-- +goose Down
ALTER TABLE messages DROP CONSTRAINT messages_kind_check;
ALTER TABLE messages ADD CONSTRAINT messages_kind_check
    CHECK (kind IN ('text', 'tool', 'system', 'card', 'approval', 'secret_request', 'progress', 'team_status', 'memory'));

DROP INDEX IF EXISTS llm_guardrail_events_ws_turn_action;
DROP INDEX IF EXISTS llm_guardrail_events_ws_action_created;
DROP INDEX IF EXISTS llm_guardrail_events_ws_created;
DROP TABLE IF EXISTS llm_guardrail_events;

DROP INDEX IF EXISTS workspace_guardrail_rules_ws_created;
DROP TABLE IF EXISTS workspace_guardrail_rules;

DROP TABLE IF EXISTS workspace_guardrail_policies;
