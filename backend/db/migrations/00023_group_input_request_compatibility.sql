-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Early development databases recorded version 22 before these fields shipped.
-- Existing requests from that schema collected advisory input.
ALTER TABLE group_input_requests
    ADD COLUMN IF NOT EXISTS required BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS resume_turn_id UUID;

ALTER TABLE group_input_requests ALTER COLUMN required DROP DEFAULT;

CREATE UNIQUE INDEX IF NOT EXISTS group_input_resume_turn
    ON group_input_requests (workspace_id, resume_turn_id)
    WHERE resume_turn_id IS NOT NULL;

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'group_input_requests'::regclass
          AND contype = 'f'
          AND pg_get_constraintdef(oid) LIKE 'FOREIGN KEY (workspace_id, resume_turn_id, requester_bot_id)%'
    ) THEN
        ALTER TABLE group_input_requests
            ADD CONSTRAINT group_input_resume_turn_fk
            FOREIGN KEY (workspace_id, resume_turn_id, requester_bot_id)
            REFERENCES bot_turns(workspace_id, id, bot_id) ON DELETE SET NULL (resume_turn_id);
    END IF;
END
$$;
-- +goose StatementEnd
