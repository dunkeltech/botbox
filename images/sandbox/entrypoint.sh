#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only

set -euo pipefail

# The trusted egress worker publishes the public CA into a read-only
# volume. It starts first, but Docker can open the desktop while it loads.
if [[ "${BOTBOX_CA_CERT:-}" == "/run/botbox/egress-ca/ca.crt" ]]; then
  for _ in $(seq 1 100); do
    [[ -s "${BOTBOX_CA_CERT}" ]] && break
    sleep 0.1
  done
  test -s "${BOTBOX_CA_CERT}"
elif [[ -n "${BOTBOX_CA_URL:-}" ]]; then
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

# Remove stale Chromium singleton locks and crash state left on the /home/bot volume.
/usr/local/bin/chromium-launch.sh --prepare-only

exec /usr/bin/supervisord -n -c /etc/supervisor/conf.d/botbox.conf
