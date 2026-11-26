#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only

set -euo pipefail

usage() {
  echo "Usage: $0 [output_file]" >&2
  echo "Requires DATABASE_URL to be set in the environment." >&2
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

if [[ -z "${DATABASE_URL:-}" ]]; then
  echo "error: DATABASE_URL is required" >&2
  exit 1
fi

out_file="${1:-./backups/botbox-$(date -u +%Y%m%dT%H%M%SZ).dump}"
out_dir="$(dirname "$out_file")"
mkdir -p "$out_dir"

tmp_file="$(mktemp "${out_dir}/.botbox-backup.XXXXXX")"
cleanup() {
  rm -f "$tmp_file"
}
trap cleanup EXIT

pg_dump "$DATABASE_URL" \
  --format=custom \
  --no-owner \
  --no-privileges \
  --file="$tmp_file"

# Verify archive integrity before publishing.
pg_restore --list "$tmp_file" >/dev/null

mv "$tmp_file" "$out_file"
chmod 600 "$out_file"

sha_file="${out_file}.sha256"
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$out_dir" && sha256sum "$(basename "$out_file")" > "$(basename "$sha_file")")
else
  (cd "$out_dir" && shasum -a 256 "$(basename "$out_file")" > "$(basename "$sha_file")")
fi
chmod 600 "$sha_file"

size_bytes="$(wc -c < "$out_file" | tr -d ' ')"
echo "Backup written: ${out_file} (${size_bytes} bytes)"
echo "Checksum written: ${sha_file}"
