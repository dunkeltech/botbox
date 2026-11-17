# 0014. Computer-Use Wiring

- **Date:** 2026-11-16
- **Status:** Accepted

## Context

Phase 0 (ADR-0005) spiked one `DesktopService` behind gRPC and a local MCP server. Phase 5 ([#7](https://github.com/dunkeltech/botbox/issues/7)) has to finish that path: API-mode providers, CLI registration, a browser tool, human takeover within one second, an action timeline, and screenshot retention. The control lock already lives in Postgres (`environments.control_holder_user_id`).

Secrets must stay out of the sandbox, prompts, logs and API error text. Every timeline query is scoped by `workspace_id`.

## Options Considered

### Option A — In-process loopback MCP and a pull/ack journal

- **Pros:** Takeover cancels the same process that runs xdotool. The API remains the only writer of `desktop_actions`, so MCP and gRPC share one idempotent store. Provider transports stay behind the existing chat-completions loop.
- **Cons:** The agent process owns both gRPC and HTTP. OpenAI computer use must be translated because the engine speaks chat completions, not the Responses API.

### Option B — Host-side MCP bridge and provider-native loops

- **Pros:** Each provider keeps its native request shape.
- **Cons:** A second process cannot see the in-flight xdotool context, so takeover cannot meet the one-second bound. ADR-0005 already rejected an external bridge.

## Decision

Keep Option A from ADR-0005 and wire it as follows:

1. The agent serves MCP as `POST http://127.0.0.1:9091/mcp` in the same process as gRPC. Stdio (`--mcp-stdio`) is only a fallback and reads `/tmp/botbox-desktop-control`. gRPC stays on the existing bind.
2. The agent journals actions. The API pulls them over gRPC and upserts `desktop_actions` on `(workspace_id, bot_id, session_id, agent_seq)`. A repeat pull does not clear the stored screenshot.
3. Screenshot bytes expire after `workspaces.screenshot_retention_days` (1..30, default 7). The action row stays. List responses omit the bytes; a separate authenticated GET returns the image. Bytes and typed text are not logged.
4. Anthropic computer use sends tool version `computer_20251124` and beta `computer-use-2025-11-24`. OpenAI computer use is translated to the Responses API only when the computer marker is present. Other providers receive generic function tools whose image results are `image_url` data URLs.
5. Coordinates on a downscaled screenshot are scaled back to the fixed 1280×800 desktop inside the agent, once.
6. Hand-back inserts the exact text `A human used the desktop; take a fresh screenshot.` as a `kind=text` system message and enqueues a bot turn before the release commits. An optional note is a following line with control characters removed. The `desktop_handback` source lets the runner accept the instruction while rejecting other system messages as turn inputs. A duplicate release is deduplicated while that note is still the latest message. After a failed hand-back, a fresh takeover and release can enqueue a new turn.
7. If a running sandbox implements the desktop bridge and rejects the lock, take control returns 503 and releases the database claim.
8. Chromium remote debugging listens on `127.0.0.1:9222` only.

## Consequences

- CLI configs point at the loopback MCP URL (`claude --mcp-config`, Codex `config.toml`, agy `mcp_config.json`). A failed config write does not fail the turn.
- API-mode computer use is capped per turn (steps, screenshots, tokens) and the prompt tells the model to prefer shell and browser tools over pixel clicks.
- Viewers can read the timeline and screenshots. Only the owner can change retention. Cross-workspace ids return 404.
- The timeline UI shows the verb, 1280×800 coordinates and the retention line. It does not render typed parameters.
