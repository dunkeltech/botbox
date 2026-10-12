#!/usr/bin/env bash
set -euo pipefail

# Retrieve only the public CA over the isolated bot network. No private key or
# provider credential is copied into the sandbox.
if [[ -n "${BOTBOX_CA_URL:-}" ]]; then
  mkdir -p "${HOME:-/home/bot}/.local/share/botbox"
  curl --noproxy '*' --fail --silent --show-error --retry 8 --retry-connrefused --retry-delay 1 --max-time 5 "${BOTBOX_CA_URL}" -o "${BOTBOX_CA_CERT}"
fi

# Install optional per-bot egress CA if mounted before launching Chromium/tools.
if [[ -n "${BOTBOX_CA_CERT:-}" && -f "${BOTBOX_CA_CERT}" ]]; then
  /usr/local/bin/install-bot-ca.sh "${BOTBOX_CA_CERT}"
  # Export trust settings to supervisord and all of its child processes.
  source "${HOME:-/home/bot}/.botbox-ca-env"
fi

# Clean stale X locks after container restarts while preserving /home/bot volume.
rm -f /tmp/.X0-lock /tmp/.X11-unix/X0

# Ensure user directories exist even when a fresh named volume is mounted onto /home/bot.
mkdir -p "${HOME:-/home/bot}/.pki/nssdb" "${HOME:-/home/bot}/workspace"

exec /usr/bin/supervisord -n -c /etc/supervisor/conf.d/botbox.conf
