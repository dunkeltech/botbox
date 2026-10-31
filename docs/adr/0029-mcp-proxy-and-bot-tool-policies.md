# 0029. MCP Proxy and Per-Bot Command Access Control

- **Date:** 2026-11-18
- **Status:** Accepted

## Context

Bots run in both in-sandbox CLI mode (`claude`, `codex`, `agy`) and host-side Model API mode (`openrouter`, `anthropic`, `openai`). Users want to expose external Model Context Protocol (MCP) servers to bots while enforcing two core `bot.Box` invariants (`Application.md` §1):

1. **Credentials never enter a sandbox.** Upstream MCP server authentication headers (`bearer`, `header`, `basic`) must remain envelope-encrypted outside bot containers and must never be written into sandbox MCP config files, environment variables, prompts, or logs.
2. **Least-privilege per-bot command control.** Different bots in the same workspace need independent subsets of MCP servers and per-command permissions (`allow`, `ask`, `deny`), with newly discovered or unassigned tools failing closed (`deny`).

## Options Considered

### Option A — Direct Sandbox Connections with Generic MITM Header Injection

- **Pros:** Minimal MCP protocol parsing in the proxy.
- **Cons:** Bots still receive full `tools/list` catalogs and can invoke destructive tools (`delete_*`, `manage_*`) unless the upstream MCP server itself implements per-bot authorization. Cannot gate individual MCP commands behind human chat approvals or unify policy enforcement for host-side API runners.

### Option B — Central Workspace MCP Registry + Control-Plane MCP Proxy (`backend/internal/mcpproxy`)

- **Pros:** Workspace `owner` and `admin` roles register remote HTTPS MCP servers (`mcp_servers`) once with write-only envelope-encrypted secrets (`secrets.KeyRing`) and discover/cache their tool catalogs (`mcp_server_tools`). Bot managers (`owner`, `admin`, or the owning `member`) explicitly assign servers to individual bots (`bot_mcp_assignments`) and set per-command policies (`bot_mcp_tool_policies`: `allow`, `ask`, `deny`). The control-plane proxy filters `tools/list` (omitting `deny` and unknown tools), enforces `allow` / `ask` / `deny` on `tools/call`, injects credentials outside the sandbox, scrubs reflected credentials, and records `mcp_call_log`.
- **Cons:** Requires an MCP-aware proxy layer in `cmd/egress-proxy` and the API runner.

## Decision

Adopt **Option B**:

1. **Workspace MCP Registry & Discovery:** Owners and admins register canonical `https://...:443/...` MCP endpoints (`mcp_servers`, validated slug `^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$`, auth schemes `none`, `bearer`, `header`, `basic`) and sync tool catalogs via `netguard`-validated public DNS + literal IP dialing (`<= 200` tools, `<= 64 KiB` schema per tool). Registration grants no implicit bot access.
2. **Per-Bot Assignment & Fail-Closed Command Policies:** Bot managers assign servers (`bot_mcp_assignments`, `default_policy = 'deny'`) and configure per-tool overrides (`allow`, `ask`, `deny`) using optimistic revision concurrency (`expectedRevision`). Unconfigured or newly synced tools default to `deny`.
3. **Three-Tier Enforcement (`backend/internal/mcpproxy`):**
   - `deny`: Stripped from `tools/list`; direct `tools/call` attempts fail with JSON-RPC error `-32003` (`mcp.tool_denied`, HTTP `403` semantic) and log `blocked`.
   - `allow`: Proxy decrypts credentials via `KeyRing`, dials upstream via `netguard`, enforces a `2 MiB` response cap and `30 s` timeout, scrubs any reflected secret value, logs `allowed`, and returns the result.
   - `ask`: Exposed in `tools/list`; `tools/call` checks `mcp_tool_grants` for an approved grant. When absent, it opens a deduplicated `kind = 'mcp_tool'` approval card in chat (`approvals` + `mcp_tool_grants`) and rejects/pauses until a human approves or denies the decision.
4. **Protocol Surface Hardening:** `resources/*`, `prompts/*`, and `sampling/*` are rejected (`-32601`) to prevent external prompt injection and unauthorized resource reads.
5. **Dual-Mode Wiring:** `cmd/egress-proxy` serves Streamable HTTP `POST /mcp` and `POST /mcp/{slug}` for in-sandbox CLIs (`claude`, `codex`, `agy` via `InstallBotMCPServers` / `MCPHandler`), while the Model API runner exposes `mcp__<slug>__<tool>` tools backed by the same `mcpproxy.Service`.

## Consequences

- Upstream MCP tokens stay exclusively in the control plane and never enter bot containers or prompts.
- Adding new tools on an upstream server cannot silently expand a bot's capabilities (`default_policy = 'deny'`).
- Both CLI and Model API bots share one policy engine, approval workflow, and `mcp_call_log` audit stream.
