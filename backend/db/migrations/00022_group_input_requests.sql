-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
CREATE TABLE group_input_requests (
    workspace_id UUID NOT NULL,
    conversation_id UUID NOT NULL,
    message_id UUID NOT NULL,
    requester_bot_id UUID NOT NULL,
    deadline TIMESTAMPTZ NOT NULL,
    required BOOLEAN NOT NULL,
    resume_turn_id UUID,
    state TEXT NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting', 'collected', 'stopped')),
    PRIMARY KEY (workspace_id, conversation_id, message_id),
    FOREIGN KEY (workspace_id, conversation_id, message_id)
        REFERENCES messages(workspace_id, conversation_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, resume_turn_id, requester_bot_id)
        REFERENCES bot_turns(workspace_id, id, bot_id) ON DELETE SET NULL (resume_turn_id)
);

CREATE UNIQUE INDEX group_input_resume_turn ON group_input_requests (workspace_id, resume_turn_id)
    WHERE resume_turn_id IS NOT NULL;

CREATE UNIQUE INDEX group_input_one_waiting
    ON group_input_requests (workspace_id, conversation_id, requester_bot_id)
    WHERE state = 'waiting';

CREATE TABLE group_input_recipients (
    workspace_id UUID NOT NULL,
    conversation_id UUID NOT NULL,
    request_message_id UUID NOT NULL,
    bot_id UUID NOT NULL,
    bot_name TEXT NOT NULL,
    turn_id UUID,
    reply_message_id UUID,
    PRIMARY KEY (workspace_id, conversation_id, request_message_id, bot_id),
    UNIQUE (workspace_id, turn_id),
    FOREIGN KEY (workspace_id, conversation_id, request_message_id)
        REFERENCES group_input_requests(workspace_id, conversation_id, message_id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, turn_id, bot_id)
        REFERENCES bot_turns(workspace_id, id, bot_id) ON DELETE SET NULL (turn_id),
    FOREIGN KEY (workspace_id, conversation_id, reply_message_id)
        REFERENCES messages(workspace_id, conversation_id, id) ON DELETE SET NULL (reply_message_id)
);
