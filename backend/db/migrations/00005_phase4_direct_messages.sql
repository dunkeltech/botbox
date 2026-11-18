-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Composite keys prevent application mistakes from linking different tenants.
ALTER TABLE bots ADD CONSTRAINT bots_workspace_id_id_key UNIQUE (workspace_id, id);
CREATE TABLE conversations (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 type TEXT NOT NULL DEFAULT 'dm' CHECK (type = 'dm'),
 user_id UUID NOT NULL,
 bot_id UUID NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE (workspace_id, id),
 UNIQUE (workspace_id,id,bot_id),
 UNIQUE (workspace_id, user_id, bot_id),
 FOREIGN KEY (workspace_id, user_id) REFERENCES workspace_members(workspace_id,user_id) ON DELETE CASCADE,
 FOREIGN KEY (workspace_id, bot_id) REFERENCES bots(workspace_id,id) ON DELETE CASCADE
);
CREATE TABLE conversation_members (
 workspace_id UUID NOT NULL,
 conversation_id UUID NOT NULL,
 member_type TEXT NOT NULL CHECK(member_type IN ('user','bot')),
 member_id UUID NOT NULL,
 last_read_position BIGINT NOT NULL DEFAULT 0 CHECK(last_read_position >= 0),
 PRIMARY KEY(workspace_id,conversation_id,member_type,member_id),
 FOREIGN KEY(workspace_id,conversation_id) REFERENCES conversations(workspace_id,id) ON DELETE CASCADE
);
CREATE TABLE messages (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 position BIGINT GENERATED ALWAYS AS IDENTITY NOT NULL UNIQUE,
 workspace_id UUID NOT NULL,
 conversation_id UUID NOT NULL,
 sender_type TEXT NOT NULL CHECK(sender_type IN ('user','bot','system')),
 sender_user_id UUID REFERENCES users(id),
 sender_bot_id UUID REFERENCES bots(id),
 kind TEXT NOT NULL DEFAULT 'text' CHECK(kind IN ('text','tool','system','card')),
 content TEXT NOT NULL CHECK(octet_length(content) <= 1048576),
 metadata JSONB NOT NULL DEFAULT '{}',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(workspace_id,id),
 UNIQUE(workspace_id,conversation_id,id),
 FOREIGN KEY(workspace_id,sender_user_id) REFERENCES workspace_members(workspace_id,user_id),
 FOREIGN KEY(workspace_id,sender_bot_id) REFERENCES bots(workspace_id,id),
 FOREIGN KEY(workspace_id,conversation_id) REFERENCES conversations(workspace_id,id) ON DELETE CASCADE,
 CHECK((sender_type='user' AND sender_user_id IS NOT NULL AND sender_bot_id IS NULL) OR
 (sender_type='bot' AND sender_bot_id IS NOT NULL AND sender_user_id IS NULL) OR
 (sender_type='system' AND sender_user_id IS NULL AND sender_bot_id IS NULL))
);
CREATE INDEX messages_conversation_position ON messages(workspace_id,conversation_id,position DESC);
CREATE TABLE bot_turns (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 position BIGINT GENERATED ALWAYS AS IDENTITY NOT NULL UNIQUE,
 workspace_id UUID NOT NULL,
 bot_id UUID NOT NULL,
 conversation_id UUID NOT NULL,
 message_id UUID NOT NULL,
 status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','completed','failed','interrupted','budget_exceeded')),
 error_code TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(workspace_id,id),
 UNIQUE(workspace_id,message_id),
 UNIQUE(workspace_id,id,bot_id),
 FOREIGN KEY(workspace_id,bot_id) REFERENCES bots(workspace_id,id) ON DELETE CASCADE,
 FOREIGN KEY(workspace_id,conversation_id,bot_id) REFERENCES conversations(workspace_id,id,bot_id) ON DELETE CASCADE,
 FOREIGN KEY(workspace_id,conversation_id,message_id) REFERENCES messages(workspace_id,conversation_id,id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX bot_turns_one_running ON bot_turns(workspace_id,bot_id) WHERE status='running';
CREATE TABLE bot_sessions (
 workspace_id UUID NOT NULL,
 bot_id UUID NOT NULL,
 conversation_id UUID NOT NULL,
 runner TEXT NOT NULL,
 external_session_id TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(workspace_id,bot_id,conversation_id),
 FOREIGN KEY(workspace_id,bot_id) REFERENCES bots(workspace_id,id) ON DELETE CASCADE,
 FOREIGN KEY(workspace_id,conversation_id,bot_id) REFERENCES conversations(workspace_id,id,bot_id) ON DELETE CASCADE
);
CREATE TABLE model_prices (
 workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 provider TEXT NOT NULL,
 model TEXT NOT NULL,
 input_micros_per_million BIGINT NOT NULL CHECK(input_micros_per_million>=0),
 output_micros_per_million BIGINT NOT NULL CHECK(output_micros_per_million>=0),
 cache_micros_per_million BIGINT NOT NULL CHECK(cache_micros_per_million>=0),
 PRIMARY KEY(workspace_id,provider,model)
);
CREATE TABLE usage_records (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id UUID NOT NULL,
 bot_id UUID NOT NULL,
 turn_id UUID NOT NULL,
 request_id UUID NOT NULL,
 source TEXT NOT NULL CHECK(source IN ('cli_result','api_response','proxy_meter')),
 model TEXT NOT NULL,
 input_tokens BIGINT NOT NULL CHECK(input_tokens>=0),
 output_tokens BIGINT NOT NULL CHECK(output_tokens>=0),
 cache_tokens BIGINT NOT NULL CHECK(cache_tokens>=0),
 cost_micros BIGINT CHECK(cost_micros>=0),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(workspace_id,request_id),
 FOREIGN KEY(workspace_id,bot_id) REFERENCES bots(workspace_id,id) ON DELETE CASCADE,
 FOREIGN KEY(workspace_id,turn_id,bot_id) REFERENCES bot_turns(workspace_id,id,bot_id) ON DELETE CASCADE
);
CREATE INDEX usage_records_period ON usage_records(workspace_id,bot_id,created_at);
CREATE TABLE budgets (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 bot_id UUID,
 period TEXT NOT NULL CHECK(period IN ('day','month')),
 limit_micros BIGINT NOT NULL CHECK(limit_micros>0),
 FOREIGN KEY(workspace_id,bot_id) REFERENCES bots(workspace_id,id) ON DELETE CASCADE,
 CHECK(bot_id IS NOT NULL OR period='month'),
 UNIQUE NULLS NOT DISTINCT(workspace_id,bot_id,period),
 UNIQUE(workspace_id,id)
);
CREATE TABLE approvals (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 bot_id UUID NOT NULL,
 budget_id UUID NOT NULL,
 FOREIGN KEY(workspace_id,budget_id) REFERENCES budgets(workspace_id,id) ON DELETE CASCADE,
 kind TEXT NOT NULL DEFAULT 'raise_budget' CHECK(kind='raise_budget'),
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','approved','denied')),
 decided_by UUID REFERENCES users(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 decided_at TIMESTAMPTZ,
 FOREIGN KEY(workspace_id,bot_id) REFERENCES bots(workspace_id,id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX approvals_one_pending ON approvals(workspace_id,budget_id) WHERE status='pending';
