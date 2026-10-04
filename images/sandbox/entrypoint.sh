#!/usr/bin/env bash
set -euo pipefail

# Install optional per-bot egress CA if mounted before launching Chromium/tools.
if [[ -n "${BOTBOX_CA_CERT:-}" && -f "${BOTBOX_CA_CERT}" ]]; then
  /usr/local/bin/install-bot-ca.sh "${BOTBOX_CA_CERT}"
fi

# Clean stale X locks after container restarts while preserving /home/bot volume.
rm -f /tmp/.X0-lock /tmp/.X11-unix/X0

# Start TigerVNC headless X server on :0 at fixed 1280x800 resolution.
# SecurityTypes None is safe here because the VNC port is bound to localhost only
# and exposed solely via websockify inside the isolated bot container network.
Xtigervnc "${DISPLAY}" \
  -geometry "${VNC_RESOLUTION}" \
  -depth 24 \
  -rfbport "${VNC_PORT}" \
  -localhost yes \
  -SecurityTypes None \
  -AlwaysShared &
XVNC_PID=$!

# Wait briefly for X socket before launching XFCE desktop session.
for _ in $(seq 1 50); do
  if [[ -S /tmp/.X11-unix/X0 ]]; then
    break
  fi
  sleep 0.1
done

dbus-launch startxfce4 >/tmp/xfce4.log 2>&1 &

cleanup() {
  kill "${XVNC_PID}" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

exec websockify "0.0.0.0:${WEBSOCKIFY_PORT}" "127.0.0.1:${VNC_PORT}"
