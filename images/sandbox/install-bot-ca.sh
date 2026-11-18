#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only

set -euo pipefail

CA_PATH="${1:-/etc/botbox/ca/bot-ca.crt}"
CA_DIR="${HOME:-/home/bot}/.local/share/botbox"
DEST_CRT="${CA_DIR}/bot-ca.crt"
BUNDLE="${CA_DIR}/ca-bundle.crt"
NSS_DB="${HOME:-/home/bot}/.pki/nssdb"
ENV_FILE="${HOME:-/home/bot}/.botbox-ca-env"

if [[ ! -f "${CA_PATH}" ]]; then
  echo "CA certificate not found" >&2
  exit 1
fi

# Sandboxes cannot elevate privileges. Preserve public roots in a user-owned
# bundle instead of modifying the system store with sudo (see ADR-0008).
mkdir -p "${CA_DIR}" "${NSS_DB}"
cp "${CA_PATH}" "${DEST_CRT}"
: > "${BUNDLE}"
for system_bundle in /etc/ssl/certs/ca-certificates.crt /etc/ssl/cert.pem; do
  if [[ -f "${system_bundle}" ]]; then
    cat "${system_bundle}" >> "${BUNDLE}"
    break
  fi
done
printf '\n' >> "${BUNDLE}"
cat "${DEST_CRT}" >> "${BUNDLE}"

# Chromium reads its NSS store, while language runtimes use the exported bundle.
if [[ ! -f "${NSS_DB}/cert9.db" ]]; then
  certutil -N --empty-password -d "sql:${NSS_DB}"
fi
certutil -D -n "botbox-bot-ca" -d "sql:${NSS_DB}" 2>/dev/null || true
certutil -A -t "C,," -n "botbox-bot-ca" -i "${DEST_CRT}" -d "sql:${NSS_DB}"

# Quote paths for sourcing even when a development home directory contains spaces.
{
  printf 'export SSL_CERT_FILE=%q\n' "${BUNDLE}"
  printf 'export NODE_EXTRA_CA_CERTS=%q\n' "${DEST_CRT}"
  printf 'export REQUESTS_CA_BUNDLE=%q\n' "${BUNDLE}"
  printf 'export GIT_SSL_CAINFO=%q\n' "${BUNDLE}"
} > "${ENV_FILE}"
