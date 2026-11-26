<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0003. CLI Authentication Without Sandbox Secrets

- **Date:** 2026-11-14
- **Status:** Accepted. Superseded for `agy` by [ADR-0040](0040-agy-gemini-api-key-route.md)

## Context

The [security invariants](../../AGENTS.md#7-security-invariants-never-break-these) keep real API keys, OAuth refresh tokens, and PATs out of bot sandboxes. Every LLM CLI (`claude`, `codex`, `agy`) must operate normally inside the sandbox without seeing real provider credentials (Phase 0 Spike S2).

## Options Considered

### Claude Code (`claude`) and Codex (`codex`)

Both CLIs natively honor custom API base URLs and dummy keys:
- `claude`: `ANTHROPIC_BASE_URL` pointing to `cmd/egress-proxy` + placeholder `ANTHROPIC_API_KEY`.
- `codex`: `OPENAI_BASE_URL` pointing to `cmd/egress-proxy` + placeholder `OPENAI_API_KEY`.

`backend/internal/secrets/llmproxy` routes `/llm/{provider}/*`, strips placeholder `Authorization`, `Proxy-Authorization`, `X-Api-Key`, and `X-Goog-Api-Key` headers, injects the real workspace secret, and scrubs any leaked secret strings from upstream errors and structured logs.

### Option 1 for `agy` — Undocumented API Key / Base-URL Flag

- **Pros:** Symmetric with `claude` and `codex`.
- **Cons:** Inspected `agy --help` and Antigravity CLI documentation: `agy` exposes no `--api-key` or `--base-url` flag. Rejected as unsupported.

### Option 2 for `agy` — Proxy-Held OAuth Refresh Token Exchange

- **Pros:** Preserves the zero-secret sandbox invariant strictly. The sandbox holds only an opaque placeholder token; `backend/internal/secrets/llmproxy` holds the workspace OAuth refresh token outside the sandbox, exchanges it via a `TokenExchanger` for short-lived access tokens in proxy memory, injects `Authorization: Bearer <access-token>` on upstream Google/Antigravity API hosts, and scrubs upstream error responses and logs.
- **Cons:** Requires maintaining OAuth endpoint/header compatibility inside the egress proxy when upstream Antigravity auth flows evolve.

### Option 3 for `agy` — Flagged Workspace Exception

- **Pros:** Escape hatch if an upstream `agy` update temporarily prevents proxy-side token substitution.
- **Cons:** Places scoped credentials inside the bot sandbox, weakening the strict zero-secret boundary.

## Decision

Adopt **Option 2 (Proxy-Held OAuth Refresh Token Exchange)** as the primary mechanism for `agy`, backed by `backend/internal/secrets/llmproxy`, alongside base-URL placeholder injection for `claude` and `codex`.

Retain **Option 3** strictly as an explicit, disabled-by-default workspace opt-in (`allow_sandbox_agy_credentials = false` by default), editable only by a workspace Owner with a visible security warning indicator if needed in a future release.

## Consequences

- The default sandbox configuration keeps real LLM secrets outside the sandbox for `agy`.
- Phase 4 will wire `llmproxy.Handler` to workspace-encrypted DB credentials and Postgres-backed OAuth refresh token storage.
