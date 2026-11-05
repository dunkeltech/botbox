-- +goose Up
ALTER TABLE bot_turns ADD COLUMN heartbeat_at timestamptz;
CREATE INDEX bot_turns_team_peer ON bot_turns(workspace_id, conversation_id, bot_id, position DESC);

ALTER TABLE messages DROP CONSTRAINT messages_kind_check;
ALTER TABLE messages ADD CONSTRAINT messages_kind_check
    CHECK (kind IN ('text', 'tool', 'system', 'card', 'approval', 'secret_request', 'progress', 'team_status'));

CREATE INDEX messages_team_updates_by_turn
    ON messages(workspace_id, conversation_id, (metadata->>'turnId'), position DESC)
    WHERE kind IN ('progress', 'team_status');
