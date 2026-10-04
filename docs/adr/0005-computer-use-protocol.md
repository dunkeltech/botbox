# 0005. Computer-Use Protocol and Local MCP Server

- **Date:** 2026-11-14
- **Status:** Accepted

## Context

Bots need reliable GUI desktop interaction (`screenshot`, `click`, `type`, `key`) on `DISPLAY=:0` (1280×800) across two execution styles (Issue #2 Spike S4, `Application.md` §4.8 and §7):
1. Host-orchestrated API loops (`Anthropic` / `OpenAI` computer-use calls driven by the backend engine).
2. In-sandbox agent CLIs (`claude` and `agy`) calling desktop tools directly inside the sandbox.

We also needed to determine whether `agy` supports MCP servers for computer-use tools (`Application.md` §7).

## Options Considered

### Option A — Dual Interface in `cmd/agent` (Host gRPC + In-Sandbox MCP)

- **Pros:** Shares one validated `agent.DesktopService` (`1280x800` coordinate bounds checks, button allowlist, `MaxTypeBytes` = `4096`, NUL rejection, `MaxKeyBytes` = `128`) across both host gRPC (`agent.v1.AgentService`: `Ping`, `Screenshot`, `Click`, `Type`, `Key`) and an embedded JSON-RPC 2.0 MCP server (`2025-03-26`, over Stdio and Streamable HTTP `POST /mcp`). Verified via Antigravity CLI documentation that `agy` natively configures both Stdio (`command`/`args`/`env`) and Streamable HTTP (`url`) MCP servers in `~/.gemini/config/mcp_config.json`.
- **Cons:** Maintains two thin transport adapters over `DesktopService`.

### Option B — Host-Only gRPC + External Python/Node MCP Bridge

- **Pros:** Single `cmd/agent` transport.
- **Cons:** Adds an extra Node/Python runtime dependency and IPC hop per desktop click/screenshot inside the container.

## Decision

Adopt **Option A**: implement shared `DesktopService` validation in `backend/internal/agent/desktop.go` backing both:

1. **Host-driven gRPC (`backend/proto/agent/v1/agent.proto`, `backend/internal/agent/server.go`):** paired with `engine.ComputerUseRunner` (`MaxSteps` turn cap and `ErrTakeoverConflict` human-takeover preemption checked before observation and again right before mutating actions, mapping to HTTP `409`).
2. **Embedded MCP server (`backend/internal/agent/mcp.go`):** exposing `screenshot`, `click`, `type`, and `key` over Stdio and Streamable HTTP `/mcp` using protocol version `2025-03-26` for both `claude` and `agy`.

## Consequences

- `Application.md` §7's open question ("Whether the agent supports MCP for `agy`") is resolved: confirmed **yes** via `~/.gemini/config/mcp_config.json` (Stdio and Streamable HTTP `2025-03-26`).
- All desktop coordinates and inputs undergo centralized bounds and character sanitization in `DesktopService` regardless of whether gRPC or MCP invokes them.
- Human takeover deterministically blocks host-driven computer-use mutations before sending X11 clicks or keystrokes.
