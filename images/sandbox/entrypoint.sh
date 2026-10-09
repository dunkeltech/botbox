#!/usr/bin/env bash
set -euo pipefail

# Install optional per-bot egress CA if mounted before launching Chromium/tools.
if [[ -n "${BOTBOX_CA_CERT:-}" && -f "${BOTBOX_CA_CERT}" ]]; then
  /usr/local/bin/install-bot-ca.sh "${BOTBOX_CA_CERT}"
fi

# Clean stale X locks after container restarts while preserving /home/bot volume.
rm -f /tmp/.X0-lock /tmp/.X11-unix/X0

# Ensure user directories exist even when a fresh named volume is mounted onto /home/bot.
mkdir -p "${HOME:-/home/bot}/.pki/nssdb" "${HOME:-/home/bot}/workspace"

exec /usr/bin/supervisord -n -c /etc/supervisor/conf.d/botbox.conf
