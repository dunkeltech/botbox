#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only

set -euo pipefail

usage() {
  echo "Usage: $0 <backup_file>" >&2
  echo "Requires DATABASE_URL to be set in the environment." >&2
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" || $# -lt 1 ]]; then
  usage
  exit $([[ $# -lt 1 ]] && echo 1 || echo 0)
fi

if [[ -z "${DATABASE_URL:-}" ]]; then
  echo "error: DATABASE_URL is required" >&2
  exit 1
fi

backup_file="$1"
if [[ ! -f "$backup_file" ]]; then
  echo "error: backup file not found: $backup_file" >&2
  exit 1
fi

sha_file="${backup_file}.sha256"
if [[ -f "$sha_file" ]]; then
  backup_dir="$(dirname "$backup_file")"
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$backup_dir" && sha256sum -c "$(basename "$sha_file")" >/dev/null)
  else
    (cd "$backup_dir" && shasum -a 256 -c "$(basename "$sha_file")" >/dev/null)
  fi
fi

pg_restore --list "$backup_file" >/dev/null

pg_restore \
  --dbname="$DATABASE_URL" \
  --clean \
  --if-exists \
  --no-owner \
  --no-privileges \
  --single-transaction \
  "$backup_file"

echo "Restore completed from: ${backup_file}"
