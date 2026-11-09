-- +goose Up
-- Per-bot knowledge base entries with strict workspace + bot isolation,
-- full-text search indexes, auto-save policy, and inline chat memory cards.

ALTER TABLE bots
    ADD COLUMN memory_auto_save BOOLEAN NOT NULL DEFAULT true;

CREATE TABLE bot_memories (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    bot_id UUID NOT NULL,
    title VARCHAR(160) NOT NULL CHECK (char_length(btrim(title)) BETWEEN 1 AND 160),
    content TEXT NOT NULL CHECK (char_length(btrim(content)) BETWEEN 1 AND 4000),
    mode VARCHAR(16) NOT NULL CHECK (mode IN ('always', 'on_demand')),
    tags TEXT[] NOT NULL DEFAULT '{}',
    source_kind VARCHAR(16) NOT NULL CHECK (source_kind IN ('chat', 'manual')),
    source_label VARCHAR(160) NOT NULL DEFAULT '',
    source_conversation_id UUID,
    source_turn_id UUID,
    created_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    token_estimate INT NOT NULL DEFAULT 0 CHECK (token_estimate >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id),
    UNIQUE (workspace_id, bot_id, id),
    FOREIGN KEY (workspace_id, bot_id) REFERENCES bots(workspace_id, id) ON DELETE CASCADE
);

CREATE INDEX bot_memories_bot_mode_updated
    ON bot_memories (workspace_id, bot_id, mode, updated_at DESC, created_at DESC);

CREATE INDEX bot_memories_fts
    ON bot_memories USING GIN (to_tsvector('simple', title || ' ' || content));

CREATE INDEX bot_memories_tags_gin
    ON bot_memories USING GIN (tags);

ALTER TABLE messages DROP CONSTRAINT messages_kind_check;
ALTER TABLE messages ADD CONSTRAINT messages_kind_check
    CHECK (kind IN ('text', 'tool', 'system', 'card', 'approval', 'secret_request', 'progress', 'team_status', 'memory'));

CREATE INDEX messages_memory_id
    ON messages (workspace_id, (metadata->>'memoryId'))
    WHERE kind = 'memory';

-- +goose Down
DROP INDEX IF EXISTS messages_memory_id;
ALTER TABLE messages DROP CONSTRAINT messages_kind_check;
ALTER TABLE messages ADD CONSTRAINT messages_kind_check
    CHECK (kind IN ('text', 'tool', 'system', 'card', 'approval', 'secret_request', 'progress', 'team_status'));

DROP INDEX IF EXISTS bot_memories_tags_gin;
DROP INDEX IF EXISTS bot_memories_fts;
DROP INDEX IF EXISTS bot_memories_bot_mode_updated;
DROP TABLE IF EXISTS bot_memories;

ALTER TABLE bots
    DROP COLUMN IF EXISTS memory_auto_save;
