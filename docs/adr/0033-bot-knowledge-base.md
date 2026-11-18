<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0033. Per-bot knowledge base and zero-secret memory tools

**Status** Accepted

[ADR-0034](0034-pi-openrouter-cli.md) replaced the Model API runner with Pi
for OpenRouter. Managed execution now uses the sandbox CLI and its proxy routes.

## Context

Users need bots to remember instructions, conventions, and project facts across
conversations (for example, "Remember that..."). Storing those notes inside
sandbox files alone mixes them with temporary workspace files, makes them hard to
inspect or revoke from the control plane, and risks leaking credentials if a bot
writes sensitive strings into prompts or notes.

## Options considered

- **Sandbox-local Markdown files (`MEMORY.md`):** Simple for CLI bots, but opaque
  to API-mode bots, missing from Bot Settings, and lacking centralized
  secret-guard enforcement and tenant audit logs.
- **External vector database + embedding API:** Adds operational dependencies
  and external API cost on every read/write, and cannot run deterministically in
  local/CI tests without network mocks.
- **Control-plane PostgreSQL knowledge base (`bot_memories`) with dual-mode
  injection and FTS:** Stores structured entries scoped strictly by
  `(workspace_id, bot_id)` with `always` (prompt-injected up to 4,000 tokens)
  and `on_demand` (PostgreSQL full-text + tag search) modes.

## Decision

Store per-bot knowledge base entries in PostgreSQL (`bot_memories`) scoped by
`workspace_id` and `bot_id`, capped at 200 entries per bot and a 4,000-token
`always` budget. Expose four memory tools (`memory_save`, `memory_search`,
`memory_update`, `memory_delete`) uniformly across both Model API turns
(`cmd/api/engine.go`) and CLI turns via the private egress proxy MCP endpoint
(`/memory-mcp`, registered as `botbox-memory` / `botbox_memory` in
`internal/agent/mcpconfig.go`).

Every write passes through a **zero-secret memory guard** (`internal/memory/guard.go`)
that rejects common token/key formats and any decrypted workspace, bot, or MCP
server secret before committing. Chat-authored `memory_save` calls require
`bots.memory_auto_save = true` and also persist a `kind = 'memory'` transcript
row so users see an inline confirmation card with `Edit`, `Undo`, and a link to
`/bots/:id/settings/knowledge`.

Management mutations (`POST`, `PATCH`, `DELETE`, `PUT /memories/policy`) enforce
`ActionManageBot` (workspace `owner`, `admin`, or the bot's `owner_user_id`),
while reads require workspace membership and return `404` across workspaces.

## Consequences

- Bots retain persistent, auditable knowledge across direct messages and group
  chats in the same workspace regardless of whether they run via CLI or API mode.
- Always-in-context prompt growth stays bounded (`4,000` tokens / `16,000` bytes)
  while on-demand entries scale without external embedding services.
- Secret values cannot enter bot prompts or transcripts through memory entries.
- CLI sandboxes require the updated `egress-proxy` and `agent` binaries to use
  `/memory-mcp`.
