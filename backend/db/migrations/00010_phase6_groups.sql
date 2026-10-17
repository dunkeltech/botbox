-- +goose Up
-- Groups share the conversation tables with direct messages. The old foreign
-- key pinned every turn to the single DM bot, so a group could not schedule
-- any other member (ADR-0015).

ALTER TABLE conversations DROP CONSTRAINT conversations_type_check;
ALTER TABLE conversations ALTER COLUMN user_id DROP NOT NULL;
ALTER TABLE conversations ALTER COLUMN bot_id DROP NOT NULL;
ALTER TABLE conversations DROP CONSTRAINT conversations_workspace_id_user_id_bot_id_key;

ALTER TABLE conversations
    ADD CONSTRAINT conversations_type_check CHECK (type IN ('dm', 'group')),
    ADD CONSTRAINT conversations_shape_check CHECK (
        (type = 'dm' AND user_id IS NOT NULL AND bot_id IS NOT NULL)
        OR (type = 'group' AND user_id IS NULL AND bot_id IS NULL)
    );

-- Direct messages stay one human plus one bot. Groups store nulls there, and
-- PostgreSQL would treat those nulls as distinct, so the partial index is the
-- constraint the DM upsert targets.
CREATE UNIQUE INDEX conversations_dm_pair
    ON conversations (workspace_id, user_id, bot_id)
    WHERE type = 'dm';

ALTER TABLE conversations
    ADD COLUMN name TEXT,
    ADD COLUMN goal TEXT NOT NULL DEFAULT '',
    ADD COLUMN lead_bot_id UUID,
    ADD COLUMN bot_talk BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN turn_limit INT NOT NULL DEFAULT 20,
    ADD COLUMN answer_policy TEXT NOT NULL DEFAULT 'mentioned_else_lead',
    ADD COLUMN ask_before JSONB NOT NULL DEFAULT '[]',
    ADD COLUMN budget_limit_micros BIGINT,
    ADD COLUMN turns_since_human INT NOT NULL DEFAULT 0,
    ADD COLUMN paused BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN pause_reason TEXT NOT NULL DEFAULT '',
    ADD COLUMN summary TEXT NOT NULL DEFAULT '',
    ADD COLUMN rr_index INT NOT NULL DEFAULT 0;

ALTER TABLE conversations
    ADD CONSTRAINT conversations_name_check CHECK (
        (type = 'dm' AND name IS NULL)
        OR (type = 'group' AND name IS NOT NULL AND char_length(name) BETWEEN 1 AND 100)
    ),
    ADD CONSTRAINT conversations_goal_check CHECK (octet_length(goal) <= 8192),
    ADD CONSTRAINT conversations_summary_check CHECK (octet_length(summary) <= 8192),
    ADD CONSTRAINT conversations_turn_limit_check CHECK (turn_limit BETWEEN 1 AND 100),
    ADD CONSTRAINT conversations_policy_check CHECK (answer_policy IN ('mentioned_else_lead', 'lead_first', 'round_robin')),
    ADD CONSTRAINT conversations_ask_before_check CHECK (jsonb_typeof(ask_before) = 'array'),
    ADD CONSTRAINT conversations_budget_check CHECK (budget_limit_micros IS NULL OR budget_limit_micros > 0),
    ADD CONSTRAINT conversations_turns_check CHECK (turns_since_human >= 0),
    ADD CONSTRAINT conversations_pause_reason_check CHECK (pause_reason IN ('', 'turn_limit', 'loop', 'approval', 'budget')),
    ADD CONSTRAINT conversations_rr_check CHECK (rr_index >= 0),
    ADD CONSTRAINT conversations_lead_bot_fk FOREIGN KEY (workspace_id, lead_bot_id) REFERENCES bots (workspace_id, id);

ALTER TABLE bot_turns DROP CONSTRAINT bot_turns_workspace_id_conversation_id_bot_id_fkey;
ALTER TABLE bot_sessions DROP CONSTRAINT bot_sessions_workspace_id_conversation_id_bot_id_fkey;
ALTER TABLE conversations DROP CONSTRAINT conversations_workspace_id_id_bot_id_key;

ALTER TABLE bot_turns
    ADD CONSTRAINT bot_turns_conversation_fk
    FOREIGN KEY (workspace_id, conversation_id) REFERENCES conversations (workspace_id, id) ON DELETE CASCADE;
ALTER TABLE bot_sessions
    ADD CONSTRAINT bot_sessions_conversation_fk
    FOREIGN KEY (workspace_id, conversation_id) REFERENCES conversations (workspace_id, id) ON DELETE CASCADE;

-- A check constraint cannot see the member table. The trigger is what keeps a
-- group turn on a member bot and a DM turn on that DM's bot.
-- +goose StatementBegin
CREATE FUNCTION enforce_turn_bot_membership() RETURNS trigger AS $$
DECLARE
    conv_type TEXT;
    conv_bot UUID;
BEGIN
    SELECT type, bot_id INTO conv_type, conv_bot
    FROM conversations
    WHERE workspace_id = NEW.workspace_id AND id = NEW.conversation_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'conversation not found';
    END IF;
    IF conv_type = 'dm' AND conv_bot IS DISTINCT FROM NEW.bot_id THEN
        RAISE EXCEPTION 'dm turn bot mismatch';
    END IF;
    IF conv_type = 'group' AND NOT EXISTS (
        SELECT 1 FROM conversation_members
        WHERE workspace_id = NEW.workspace_id
          AND conversation_id = NEW.conversation_id
          AND member_type = 'bot'
          AND member_id = NEW.bot_id
    ) THEN
        RAISE EXCEPTION 'group turn bot is not a member';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER bot_turns_member_bot
    BEFORE INSERT OR UPDATE OF bot_id, conversation_id ON bot_turns
    FOR EACH ROW EXECUTE FUNCTION enforce_turn_bot_membership();
CREATE TRIGGER bot_sessions_member_bot
    BEFORE INSERT OR UPDATE OF bot_id, conversation_id ON bot_sessions
    FOR EACH ROW EXECUTE FUNCTION enforce_turn_bot_membership();

-- +goose StatementBegin
CREATE FUNCTION enforce_conversation_member_tenant() RETURNS trigger AS $$
BEGIN
    IF NEW.member_type = 'user' AND NOT EXISTS (
        SELECT 1 FROM workspace_members
        WHERE workspace_id = NEW.workspace_id AND user_id = NEW.member_id
    ) THEN
        RAISE EXCEPTION 'member user is outside the workspace';
    END IF;
    IF NEW.member_type = 'bot' AND NOT EXISTS (
        SELECT 1 FROM bots
        WHERE workspace_id = NEW.workspace_id AND id = NEW.member_id
    ) THEN
        RAISE EXCEPTION 'member bot is outside the workspace';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER conversation_members_same_workspace
    BEFORE INSERT OR UPDATE OF member_type, member_id ON conversation_members
    FOR EACH ROW EXECUTE FUNCTION enforce_conversation_member_tenant();

ALTER TABLE conversation_members
    ADD COLUMN role_text TEXT NOT NULL DEFAULT '',
    ADD COLUMN notify TEXT NOT NULL DEFAULT 'all';
ALTER TABLE conversation_members
    ADD CONSTRAINT conversation_members_role_check CHECK (char_length(role_text) <= 100),
    ADD CONSTRAINT conversation_members_notify_check CHECK (notify IN ('all', 'mentions', 'mute'));

ALTER TABLE messages DROP CONSTRAINT messages_kind_check;
ALTER TABLE messages
    ADD CONSTRAINT messages_kind_check CHECK (kind IN ('text', 'tool', 'system', 'card', 'approval'));

ALTER TABLE approvals ALTER COLUMN budget_id DROP NOT NULL;
ALTER TABLE approvals DROP CONSTRAINT approvals_kind_check;
ALTER TABLE approvals
    ADD COLUMN conversation_id UUID,
    ADD COLUMN message_id UUID,
    ADD COLUMN payload JSONB NOT NULL DEFAULT '{}',
    ADD CONSTRAINT approvals_kind_check CHECK (kind IN ('raise_budget', 'new_host', 'push_protected', 'custom')),
    ADD CONSTRAINT approvals_budget_link_check CHECK (
        (kind = 'raise_budget' AND budget_id IS NOT NULL AND conversation_id IS NULL)
        OR (kind = 'raise_budget' AND budget_id IS NULL AND conversation_id IS NOT NULL)
        OR (kind <> 'raise_budget' AND budget_id IS NULL)
    ),
    ADD CONSTRAINT approvals_workspace_id_id_key UNIQUE (workspace_id, id),
    ADD CONSTRAINT approvals_conversation_fk FOREIGN KEY (workspace_id, conversation_id) REFERENCES conversations (workspace_id, id) ON DELETE CASCADE,
    ADD CONSTRAINT approvals_message_fk FOREIGN KEY (workspace_id, message_id) REFERENCES messages (workspace_id, id) ON DELETE CASCADE;

CREATE UNIQUE INDEX approvals_one_pending_payload
    ON approvals (workspace_id, conversation_id, kind, payload)
    WHERE status = 'pending' AND kind <> 'raise_budget';
CREATE UNIQUE INDEX approvals_one_pending_group_budget
    ON approvals (workspace_id, conversation_id)
    WHERE status = 'pending' AND kind = 'raise_budget' AND budget_id IS NULL;

CREATE TABLE notifications (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id UUID NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('mention', 'approval', 'budget')),
    conversation_id UUID,
    message_id UUID,
    approval_id UUID,
    read_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id),
    FOREIGN KEY (workspace_id, user_id) REFERENCES workspace_members (workspace_id, user_id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, conversation_id) REFERENCES conversations (workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, message_id) REFERENCES messages (workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, approval_id) REFERENCES approvals (workspace_id, id) ON DELETE CASCADE
);

CREATE INDEX notifications_user_created
    ON notifications (workspace_id, user_id, created_at DESC);
