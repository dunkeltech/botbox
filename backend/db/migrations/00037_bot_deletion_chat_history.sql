-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- A deleted bot remains the author of its group messages. Validate the
-- workspace when authorship is written, as for historical user authorship.
ALTER TABLE messages
    DROP CONSTRAINT messages_sender_bot_id_fkey,
    DROP CONSTRAINT messages_workspace_id_sender_bot_id_fkey;

-- +goose StatementBegin
CREATE FUNCTION check_message_bot_author() RETURNS trigger AS $$
BEGIN
    IF NEW.sender_bot_id IS NOT NULL THEN
        PERFORM 1 FROM bots
        WHERE workspace_id = NEW.workspace_id AND id = NEW.sender_bot_id
        FOR KEY SHARE;
        IF NOT FOUND THEN
            RAISE foreign_key_violation USING MESSAGE = 'message bot author is outside the workspace';
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER messages_check_bot_author
    BEFORE INSERT OR UPDATE OF workspace_id, sender_bot_id ON messages
    FOR EACH ROW EXECUTE FUNCTION check_message_bot_author();

-- Clear only the lead so the rest of the group can keep using the conversation.
ALTER TABLE conversations DROP CONSTRAINT conversations_lead_bot_fk;
ALTER TABLE conversations ADD CONSTRAINT conversations_lead_bot_fk
    FOREIGN KEY (workspace_id, lead_bot_id) REFERENCES bots(workspace_id, id)
    ON DELETE SET NULL (lead_bot_id);
CREATE INDEX conversations_workspace_lead_bot_idx
    ON conversations (workspace_id, lead_bot_id) WHERE lead_bot_id IS NOT NULL;

-- Earlier deletions of non-lead bots without messages could leave stale grants.
DELETE FROM conversation_members cm
WHERE cm.member_type = 'bot' AND NOT EXISTS (
    SELECT 1 FROM bots b
    WHERE b.workspace_id = cm.workspace_id AND b.id = cm.member_id
);

ALTER TABLE conversation_members ADD COLUMN workspace_bot_id UUID
    GENERATED ALWAYS AS (CASE WHEN member_type = 'bot' THEN member_id END) STORED;
ALTER TABLE conversation_members ADD CONSTRAINT conversation_members_workspace_bot_fk
    FOREIGN KEY (workspace_id, workspace_bot_id)
    REFERENCES bots(workspace_id, id) ON DELETE CASCADE;
CREATE INDEX conversation_members_workspace_bot_idx
    ON conversation_members (workspace_id, workspace_bot_id)
    WHERE workspace_bot_id IS NOT NULL;
