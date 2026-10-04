#!/usr/bin/env bash
set -euo pipefail

CA_PATH="${1:-/etc/botbox/ca/bot-ca.crt}"
DEST_CRT="/usr/local/share/ca-certificates/botbox-bot-ca.crt"
NSS_DB="${HOME:-/home/bot}/.pki/nssdb"
ENV_FILE="${HOME:-/home/bot}/.botbox-ca-env"

if [[ ! -f "${CA_PATH}" ]]; then
  echo "CA certificate not found: ${CA_PATH}" >&2
  exit 1
fi

# 1. System trust store (Debian/OpenSSL, curl, Go, git defaults)
if [[ "$(id -u)" -eq 0 ]]; then
  cp "${CA_PATH}" "${DEST_CRT}"
  update-ca-certificates >/dev/null
else
  sudo cp "${CA_PATH}" "${DEST_CRT}"
  sudo update-ca-certificates >/dev/null
fi

# 2. Chromium NSS trust store
mkdir -p "${NSS_DB}"
if [[ ! -f "${NSS_DB}/cert9.db" ]]; then
  certutil -N --empty-password -d "sql:${NSS_DB}"
fi
certutil -D -n "botbox-bot-ca" -d "sql:${NSS_DB}" 2>/dev/null || true
certutil -A -t "C,," -n "botbox-bot-ca" -i "${CA_PATH}" -d "sql:${NSS_DB}"

# 3. Runtime-specific CA environment variables (Node, Python requests, OpenSSL, Git)
cat > "${ENV_FILE}" <<EOF
export SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
export NODE_EXTRA_CA_CERTS=${DEST_CRT}
export REQUESTS_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt
export GIT_SSL_CAINFO=/etc/ssl/certs/ca-certificates.crt
EOF
