-- +goose Up
-- Deleting a conversation must not erase spend and reopen a bot's budget.
-- Keep the bot and workspace link when the turn's chat history is removed.
ALTER TABLE usage_records
    DROP CONSTRAINT usage_records_workspace_id_turn_id_bot_id_fkey;
ALTER TABLE usage_records
    ADD CONSTRAINT usage_records_workspace_id_turn_id_bot_id_fkey
    FOREIGN KEY (workspace_id, turn_id, bot_id)
    REFERENCES bot_turns(workspace_id, id, bot_id)
    ON DELETE SET NULL (turn_id);
