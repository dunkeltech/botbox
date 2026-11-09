-- name: LockBotMemory :exec
SELECT pg_advisory_xact_lock(hashtext('memory:' || $1::uuid::text), hashtext($2::uuid::text));

-- name: UpdateBotMemoryPolicy :one
UPDATE bots
SET memory_auto_save = $3,
    updated_at = now()
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: GetBotMemorySummary :one
SELECT
    b.memory_auto_save,
    count(m.id)::bigint AS total_count,
    count(m.id) FILTER (WHERE m.mode = 'always')::bigint AS always_count,
    count(m.id) FILTER (WHERE m.mode = 'on_demand')::bigint AS on_demand_count,
    coalesce(sum(m.token_estimate) FILTER (WHERE m.mode = 'always'), 0)::bigint AS always_tokens
FROM bots b
LEFT JOIN bot_memories m
    ON m.workspace_id = b.workspace_id AND m.bot_id = b.id
WHERE b.workspace_id = $1 AND b.id = $2
GROUP BY b.id, b.memory_auto_save;

-- name: ListBotMemories :many
SELECT *
FROM bot_memories
WHERE workspace_id = $1
  AND bot_id = $2
  AND (sqlc.arg(mode_filter)::text = '' OR mode = sqlc.arg(mode_filter)::text)
  AND (sqlc.arg(source_filter)::text = '' OR source_kind = sqlc.arg(source_filter)::text)
  AND (sqlc.arg(tag_filter)::text = '' OR sqlc.arg(tag_filter)::text = ANY(tags))
  AND (
      sqlc.arg(search_query)::text = ''
      OR to_tsvector('simple', title || ' ' || content) @@ websearch_to_tsquery('simple', sqlc.arg(search_query)::text)
      OR title ILIKE '%' || sqlc.arg(like_query)::text || '%' ESCAPE '\'
      OR content ILIKE '%' || sqlc.arg(like_query)::text || '%' ESCAPE '\'
      OR EXISTS (
          SELECT 1 FROM unnest(tags) AS t(tag)
          WHERE t.tag ILIKE '%' || sqlc.arg(like_query)::text || '%' ESCAPE '\'
      )
  )
ORDER BY updated_at DESC, created_at DESC
LIMIT sqlc.arg(page_limit)::int
OFFSET sqlc.arg(page_offset)::int;

-- name: CountFilteredBotMemories :one
SELECT count(*)::bigint
FROM bot_memories
WHERE workspace_id = $1
  AND bot_id = $2
  AND (sqlc.arg(mode_filter)::text = '' OR mode = sqlc.arg(mode_filter)::text)
  AND (sqlc.arg(source_filter)::text = '' OR source_kind = sqlc.arg(source_filter)::text)
  AND (sqlc.arg(tag_filter)::text = '' OR sqlc.arg(tag_filter)::text = ANY(tags))
  AND (
      sqlc.arg(search_query)::text = ''
      OR to_tsvector('simple', title || ' ' || content) @@ websearch_to_tsquery('simple', sqlc.arg(search_query)::text)
      OR title ILIKE '%' || sqlc.arg(like_query)::text || '%' ESCAPE '\'
      OR content ILIKE '%' || sqlc.arg(like_query)::text || '%' ESCAPE '\'
      OR EXISTS (
          SELECT 1 FROM unnest(tags) AS t(tag)
          WHERE t.tag ILIKE '%' || sqlc.arg(like_query)::text || '%' ESCAPE '\'
      )
  );

-- name: ListBotMemoryTags :many
SELECT DISTINCT tag::text AS tag
FROM bot_memories, unnest(tags) AS t(tag)
WHERE workspace_id = $1 AND bot_id = $2
ORDER BY tag ASC
LIMIT 100;

-- name: ListAlwaysBotMemories :many
SELECT *
FROM bot_memories
WHERE workspace_id = $1 AND bot_id = $2 AND mode = 'always'
ORDER BY created_at ASC, id ASC;

-- name: SearchBotMemories :many
SELECT *,
    ts_rank(
        to_tsvector('simple', title || ' ' || content),
        websearch_to_tsquery('simple', sqlc.arg(search_query)::text)
    )::real AS rank
FROM bot_memories
WHERE workspace_id = $1
  AND bot_id = $2
  AND (sqlc.arg(mode_filter)::text = '' OR mode = sqlc.arg(mode_filter)::text)
  AND (sqlc.arg(tag_filter)::text = '' OR sqlc.arg(tag_filter)::text = ANY(tags))
  AND (
      sqlc.arg(search_query)::text = ''
      OR to_tsvector('simple', title || ' ' || content) @@ websearch_to_tsquery('simple', sqlc.arg(search_query)::text)
      OR title ILIKE '%' || sqlc.arg(like_query)::text || '%' ESCAPE '\'
      OR content ILIKE '%' || sqlc.arg(like_query)::text || '%' ESCAPE '\'
      OR EXISTS (
          SELECT 1 FROM unnest(tags) AS t(tag)
          WHERE t.tag ILIKE '%' || sqlc.arg(like_query)::text || '%' ESCAPE '\'
      )
  )
ORDER BY rank DESC, updated_at DESC, created_at DESC
LIMIT sqlc.arg(result_limit)::int;

-- name: GetBotMemory :one
SELECT *
FROM bot_memories
WHERE workspace_id = $1 AND bot_id = $2 AND id = $3;

-- name: CreateBotMemory :one
INSERT INTO bot_memories (
    id,
    workspace_id,
    bot_id,
    title,
    content,
    mode,
    tags,
    source_kind,
    source_label,
    source_conversation_id,
    source_turn_id,
    created_by_user_id,
    token_estimate,
    created_at,
    updated_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
RETURNING *;

-- name: UpdateBotMemory :one
UPDATE bot_memories
SET title = $4,
    content = $5,
    mode = $6,
    tags = $7,
    token_estimate = $8,
    updated_at = $9
WHERE workspace_id = $1 AND bot_id = $2 AND id = $3
RETURNING *;

-- name: DeleteBotMemory :execrows
DELETE FROM bot_memories
WHERE workspace_id = $1 AND bot_id = $2 AND id = $3;

-- name: ClearBotMemories :execrows
DELETE FROM bot_memories
WHERE workspace_id = $1 AND bot_id = $2;

-- name: MarkMemoryMessagesDeleted :exec
UPDATE messages
SET metadata = jsonb_set(metadata, '{deleted}', 'true'::jsonb, true)
WHERE workspace_id = $1
  AND kind = 'memory'
  AND metadata->>'memoryId' = sqlc.arg(memory_id)::text;

-- name: MarkBotMemoryMessagesDeleted :exec
UPDATE messages
SET metadata = jsonb_set(metadata, '{deleted}', 'true'::jsonb, true)
WHERE workspace_id = $1
  AND kind = 'memory'
  AND sender_bot_id = $2;

-- name: UpdateMemoryMessagesSnapshot :exec
UPDATE messages
SET content = sqlc.arg(title)::text,
    metadata = jsonb_set(
        jsonb_set(
            jsonb_set(
                jsonb_set(
                    jsonb_set(metadata, '{title}', to_jsonb(sqlc.arg(title)::text), true),
                    '{content}', to_jsonb(sqlc.arg(content)::text), true
                ),
                '{mode}', to_jsonb(sqlc.arg(mode)::text), true
            ),
            '{tags}', to_jsonb(sqlc.arg(tags)::text[]), true
        ),
        '{tokenEstimate}', to_jsonb(sqlc.arg(token_estimate)::int), true
    )
WHERE workspace_id = $1
  AND kind = 'memory'
  AND metadata->>'memoryId' = sqlc.arg(memory_id)::text;

-- name: GetConversationLabel :one
SELECT type, name
FROM conversations
WHERE workspace_id = $1 AND id = $2;
