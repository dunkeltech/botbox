<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0002. Desktop Streaming and Sandbox Host Capacity

- **Date:** 2026-11-14
- **Status:** Accepted

## Context

Each bot in bot.Box requires a full Linux GUI desktop inside an unprivileged container that humans can watch live at low latency in the browser and seamlessly take over without fighting the bot for mouse or keyboard control (Issue #2 Spike S1). We also need concrete host capacity targets before sizing Docker hosts in Phase 2 (#4) and Kubernetes pods in Phase 7 (#9).

## Options Considered

### Option A — Xtigervnc + XFCE4 + `websockify` + Authenticated Go WebSocket Relay + `@novnc/novnc`

- **Pros:** Standard Debian packages (`tigervnc-standalone-server`, `xfce4`, `websockify`), mature `@novnc/novnc` client (`1.7.0`), `Xtigervnc :0` binds strictly to `127.0.0.1:5900` inside the container, and the Go API (`backend/internal/compute/vncrelay`) enforces short-lived HMAC ticket validation and strips client-to-server RFB input events when view-only.
- **Cons:** Higher bandwidth on full-screen video playback compared to WebRTC, acceptable for browser/terminal/GUI automation workloads.

### Option B — KasmVNC

- **Pros:** Built-in web frontend and dynamic encoding.
- **Cons:** Custom upstream build outside Debian `bookworm-slim` apt repositories, duplicates auth/takeover logic that bot.Box must enforce server-side per workspace and bot.

### Option C — WebRTC / Selkies Sidecar

- **Pros:** Smooth video streaming over UDP/TURN.
- **Cons:** Requires STUN/TURN infrastructure, GPU/encoder tuning, and higher container RAM/CPU overhead for multiple desktops per host.

## Decision

Adopt **Option A**: `Xtigervnc :0` (`-geometry 1280x800 -depth 24 -localhost yes -SecurityTypes None`) + `xfce4` + `websockify` (`0.0.0.0:6080 -> 127.0.0.1:5900`), fronted by `backend/internal/compute/vncrelay` and rendered by `frontend/src/components/workspace/VncViewport.tsx` using `@novnc/novnc`.

### Security & Takeover Enforcement

1. **Workspace-scoped WebSocket tickets:** `vncrelay.Signer` issues short-lived HMAC-SHA256 tickets binding `workspace_id`, `bot_id`, and `can_control`. Invalid or expired tickets return HTTP `401`; cross-workspace ticket mismatches return HTTP `404`; unreachable upstream `websockify` instances return HTTP `502`.
2. **Server-side input filtering:** When `canControl == false`, `vncrelay.NewRelayHandler` filters client-to-server binary frames and drops RFB `KeyEvent` (`0x04`), `PointerEvent` (`0x05`), and `ClientCutText` (`0x06`) messages before forwarding upstream, rather than relying only on noVNC's client-side `viewOnly` toggle.

### Measured Spike S1 Benchmarks (`botbox/sandbox:dev`, Docker Engine `29.8.2`)

- **Image size:** `1,092,536,168` bytes (~1.02 GiB, `debian:bookworm-slim` base with `xfce4`, `tigervnc-standalone-server`, `websockify`, `chromium`, `xdotool`, `scrot`, `libnss3-tools`).
- **Container startup to TCP `:6080` readiness:** `850.4 ms`.
- **WebSocket `101 Switching Protocols` + `RFB 003.008` banner handshake:** `7.8 ms`.
- **Idle desktop memory/CPU:** `135.9 MiB` RAM, `0.11%` CPU, `40` PIDs.
- **Active desktop snapshot (`xfce4-terminal` + `scrot` 1280×800 PNG, `39,207` bytes):** `146.5 MiB` RAM, `0.13%` CPU, `47` PIDs (headroom to ~500 MiB–1.2 GiB reserved for multi-tab Chromium workloads).

### Concrete Host Capacity Targets

- **Default per-sandbox limits:** `2 vCPU`, `2 GiB RAM` (`--pids-limit=512`, `--shm-size=512m`).
- **Host sizing:** **8 concurrent active desktops per 16 GB / 8 vCPU host** (**16–20 concurrent active desktops per 32 GB / 16 vCPU host**), with idle sandboxes consuming ~140 MiB RAM before idle stop/reap.

## Consequences

- `botbox/sandbox:dev` starts in under 1 second and sustains interactive VNC sessions with single-digit-millisecond handshake overhead.
- Phase 2 (#4) can use `2 vCPU` and `2 GiB` Docker container limits and expose the `vncrelay` endpoint at `/ws/bots/{workspaceID}/{botID}/vnc`.
