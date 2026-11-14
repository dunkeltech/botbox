-- +goose Up
CREATE TABLE refresh_token_families (
    id UUID PRIMARY KEY,
    revoked_at TIMESTAMPTZ
);

-- Rotated tokens already have revoked_at set. An active descendant identifies
-- a family that was still usable before this migration.
INSERT INTO refresh_token_families (id, revoked_at)
SELECT family_id,
       CASE WHEN bool_or(revoked_at IS NULL AND replaced_by IS NULL)
            THEN NULL ELSE max(revoked_at) END
FROM refresh_tokens
GROUP BY family_id;

ALTER TABLE refresh_tokens ADD CONSTRAINT refresh_tokens_family_fk
    FOREIGN KEY (family_id) REFERENCES refresh_token_families(id);

-- Historical authorship must survive revocation. Users and conversations
-- remain referenced by their existing foreign keys.
ALTER TABLE messages DROP CONSTRAINT messages_workspace_id_sender_user_id_fkey;

-- +goose StatementBegin
CREATE FUNCTION check_message_author_membership() RETURNS trigger AS $$
BEGIN
    IF NEW.sender_user_id IS NOT NULL THEN
        PERFORM 1 FROM workspace_members
        WHERE workspace_id = NEW.workspace_id AND user_id = NEW.sender_user_id
        FOR KEY SHARE;
        IF NOT FOUND THEN
            RAISE foreign_key_violation USING MESSAGE = 'message author is outside the workspace';
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER messages_check_author_membership
    BEFORE INSERT OR UPDATE OF workspace_id, sender_user_id ON messages
    FOR EACH ROW EXECUTE FUNCTION check_message_author_membership();

-- Remove any historical group grants left behind by earlier membership
-- deletions and make future removals atomic with revoking those grants.
DELETE FROM conversation_members cm
WHERE cm.member_type = 'user' AND NOT EXISTS (
    SELECT 1 FROM workspace_members wm
    WHERE wm.workspace_id = cm.workspace_id AND wm.user_id = cm.member_id
);

ALTER TABLE conversation_members ADD COLUMN workspace_user_id UUID
    GENERATED ALWAYS AS (CASE WHEN member_type = 'user' THEN member_id END) STORED;

ALTER TABLE conversation_members ADD CONSTRAINT conversation_members_workspace_user_fk
    FOREIGN KEY (workspace_id, workspace_user_id)
    REFERENCES workspace_members(workspace_id, user_id) ON DELETE CASCADE;

CREATE INDEX conversation_members_workspace_user_idx
    ON conversation_members (workspace_id, workspace_user_id)
    WHERE workspace_user_id IS NOT NULL;
