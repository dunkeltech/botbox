#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
PG_DATA="$TMP_DIR/pgdata"
PG_SOCK="$TMP_DIR/pgsock"
PG_LOG="$TMP_DIR/postgres.log"
mkdir -p "$PG_DATA" "$PG_SOCK"

cleanup() {
  if [[ -f "$PG_DATA/postmaster.pid" ]]; then
    pg_ctl -D "$PG_DATA" -m immediate stop >/dev/null 2>&1 || true
  fi
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

initdb -D "$PG_DATA" --username=postgres --auth=trust >/dev/null
pg_ctl -D "$PG_DATA" -l "$PG_LOG" -o "-k $PG_SOCK -h ''" -w start >/dev/null

createdb -h "$PG_SOCK" -U postgres botbox_src
createdb -h "$PG_SOCK" -U postgres botbox_dst

SRC_URL="postgresql://postgres@/botbox_src?host=${PG_SOCK}"
DST_URL="postgresql://postgres@/botbox_dst?host=${PG_SOCK}"

for mig in "$ROOT_DIR"/backend/db/migrations/*.sql; do
  awk '/^-- \+goose Down/{exit} {print}' "$mig" | psql "$SRC_URL" -v ON_ERROR_STOP=1 >/dev/null
done

psql "$SRC_URL" -v ON_ERROR_STOP=1 >/dev/null <<'SQL'
WITH u AS (
  INSERT INTO users (id, email, password_hash, display_name)
  VALUES ('11111111-1111-1111-1111-111111111111', 'drill@example.com', 'hash', 'Drill User')
  RETURNING id
),
w AS (
  INSERT INTO workspaces (id, name, slug, sandbox_idle_timeout_minutes)
  VALUES ('22222222-2222-2222-2222-222222222222', 'Drill WS', 'drill-ws', 30)
  RETURNING id
),
b AS (
  INSERT INTO bots (id, workspace_id, name, avatar_seed, llm_mode, llm_provider, llm_model, owner_user_id)
  SELECT '33333333-3333-3333-3333-333333333333', w.id, 'Drill Bot', 'seed', 'cli', 'openrouter', 'openrouter/fixture', u.id
  FROM w, u
  RETURNING id
)
INSERT INTO audit_log (workspace_id, actor_user_id, action, target_type, target_id, metadata)
SELECT w.id, u.id, 'workspace.created', 'workspace', w.id, '{"drill":true}'::jsonb
FROM w, u;
SQL

BACKUP_FILE="$TMP_DIR/botbox-drill.dump"
DATABASE_URL="$SRC_URL" "$ROOT_DIR/scripts/backup.sh" "$BACKUP_FILE"
DATABASE_URL="$DST_URL" "$ROOT_DIR/scripts/restore.sh" "$BACKUP_FILE"

restored_ws_count="$(psql "$DST_URL" -tAc "SELECT count(*) FROM workspaces WHERE slug = 'drill-ws' AND sandbox_idle_timeout_minutes = 30")"
restored_audit_count="$(psql "$DST_URL" -tAc "SELECT count(*) FROM audit_log WHERE action = 'workspace.created'")"

if [[ "$restored_ws_count" != "1" || "$restored_audit_count" != "1" ]]; then
  echo "error: restore verification failed (ws=$restored_ws_count audit=$restored_audit_count)" >&2
  exit 1
fi

echo "Restore drill passed (workspaces=$restored_ws_count audit_log=$restored_audit_count)"
