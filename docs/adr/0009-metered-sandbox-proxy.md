<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0009. Metered per-bot sandbox egress

- **Date:** 2026-11-15
- **Status:** Accepted

## Context

A CLI can make several provider requests during one turn. Charging its final
aggregate cannot enforce the budget before its next request. Keeping the API
worker's workspace spend lock across a CLI turn would also deadlock a proxy that
needs that same lock. Unfinished requests must remain visible after a proxy crash.

## Options

1. Charge only the CLI result. This misses intermediate requests and allows
   concurrent CLIs to overspend a shared workspace budget.
2. Keep accounting in worker memory. This loses reservations on process failure.
3. Serialize admission and settlement in a trusted per-bot proxy, with a durable
   provisional record before forwarding. This also keeps credentials outside the
   sandbox and gives uncertain requests a conservative recovery path.

## Decision

Use option 3. With a configured database URL, the Docker driver creates an unprivileged, read-only proxy container per bot. Only this trusted
container receives the proxy database URL and the encryption key configuration.
It joins the default egress bridge and the bot's internal network; the sandbox
joins only its internal network. IP forwarding is disabled in the proxy network
namespace. No Docker socket or private CA key is mounted into either sandbox.

The proxy binds its workspace/bot identity at startup, resolves encrypted
`llm-openai` / `llm-anthropic` credentials using that identity, and applies the
existing database-backed CONNECT policy (with deny-first blocklist precedence,
revision-aware NATS invalidation, and a 1-second fail-closed freshness window for
active passthrough tunnels per `ADR-0017`) for other hosts. Native provider hosts
are denied CONNECT, including with open egress policy. Claude and Codex must use
the explicit metered routes. Google routes/tunnels stay unavailable until the
agy OAuth exchange and native metering are implemented.

Each model request takes the workspace spend advisory lock, checks the active
CLI turn and pause/control state, checks budgets, and inserts an unpriced
`proxy_meter` record before forwarding. The lock stays held until usage is
settled. The record uses a server-generated request ID, actual requested model,
and the trusted running turn. Model changes require an admin-configured price
when a monetary budget applies. Provider-reported cost takes precedence.

The collector reads OpenAI Chat Completions, Responses and Anthropic Messages
usage incrementally with at most one bounded SSE line retained. A complete stream
requires both input/output counts and its terminal event. Provider errors use
fixed responses; upstream credentials, cookies and error details are not sent
back to the sandbox. Interrupted, invalid or unpriced responses retain a null
cost. Known tokens remain recorded even when a model price is missing.

A null cost on metered API/proxy usage blocks all applicable monetary budgets until
reconciled. Proxy cancellation still settles or preserves the reservation with
an independent cleanup context. A crashed proxy loses its session lock when its
connection closes, but its provisional record survives. CLI aggregate usage is
not inserted again when proxy request records already exist; unmetered CLI
aggregates are tokens-only. API mode retains its conservative whole-turn lock.

The sandbox fetches only the public proxy CA on startup and installs its
user-owned trust bundle before starting supervised tools. New metered sandboxes
also receive a generated per-sandbox agent control token. The driver recovers
that token through trusted Docker inspection after an API restart; the agent's
sanitized CLI environment excludes it.

## Consequences

- One in-flight request can cross a limit; the next request is blocked and a
  deduplicated raise-budget approval is created.
- A missing usage report can temporarily stop a budgeted workspace. Operators
  reconcile the provisional record against provider billing rather than deleting
  it to make a retry possible. Automated reconciliation remains follow-up work.
- Older sandboxes must be recreated to receive the proxy environment and agent
  token. Their named home volume is preserved by the normal lifecycle.
- Proxy restarts regenerate their CA. Restart the associated sandbox to reload
  browser/Git trust; base-URL LLM calls themselves use the isolated HTTP link.
- The proxy database URL must be reachable from its egress network. Docker
  Desktop can use `host.docker.internal`; Linux deployments must supply a
  reachable database endpoint. The public API database URL need not change.
- Real Docker tests verify CA installation, blocked provider tunnels, credential
  separation and agent authentication/recovery without calling a real LLM.

Subscription authentication and tokens-only billing are specified separately in
ADR-0011. A subscription record has null cost without representing missing billing.

## Protocol references

- [OpenAI Responses usage and streaming](https://developers.openai.com/api/reference/python/resources/responses/methods/create)
- [Anthropic cache usage fields](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)
