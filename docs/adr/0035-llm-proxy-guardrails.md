<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Workspace-scoped LLM proxy guardrails

## Context

Each bot runs an autonomous CLI (`claude`, `codex`, `pi`, or `agy`) inside a sandboxed Linux desktop that communicates with upstream model providers exclusively through the host-side `llmproxy` inside `egress-proxy` (ADR-0006, ADR-0007, ADR-0009, ADR-0034). Until now, the proxy enforced network allowlists, credential injection, and subscription/API budgets, while prompt and completion payloads flowed uninspected once admitted.

Workspaces need configurable, workspace-scoped content guardrails across all model traffic to prevent credential leakage into prompts or completions, mask or block personal data (PII), detect prompt-injection attempts, and enforce organization-specific patterns (RE2 regex or keyword lists) without trusting sandboxed processes or exposing raw matched secrets in UI logs.

## Options

- **Prompt-only instructions in the bot system prompt.** Trivial to bypass via tool outputs or indirect prompt injection, offers zero enforcement on model responses, and wastes token budget.
- **Standalone guardrail microservice between proxy and upstream providers.** Adds an extra network hop, serialization overhead, and SSE buffering latency in addition to `egress-proxy`.
- **In-process guardrail engine inside `llmproxy`.** Evaluates deterministic detectors and workspace rules before admission (`opts.Admit`) on requests and wraps upstream JSON or SSE responses on the return path inside the existing host-side proxy.

## Decision

Implement an in-process guardrail engine in `backend/internal/guardrails` wired into `llmproxy` (`egress-proxy`) and controlled through workspace settings:

1. **Workspace opt-in policy & preconfigured detectors.** Every workspace gets an opt-in `workspace_guardrail_policies` row (`enabled = false` by default) with `failure_mode` (`fail_closed` default or `fail_open`) and three built-in detectors:
   - Secrets & credentials (`both`, `block` — static key/token prefixes, private key headers, JWTs, plus stored plaintext workspace/bot secret values from the proxy ring, skipping `BOTBOX_SECRET_*` placeholders).
   - PII (`both`, `redact`, configurable entities `['email', 'iban', 'card', 'phone']` using boundary checks, mod-97 IBAN validation, and Luhn checks).
   - Prompt-injection (`input`, `audit`, multi-pattern risk scoring).
2. **Custom workspace rules (RE2 regex & keywords).** Up to 100 custom rules per `workspace_id` in `workspace_guardrail_rules` with `direction` (`input`, `output`, `both`), `action` (`block`, `redact`, `audit`), custom `replacement_token`, and a stateless `/guardrails/test` Quick Test endpoint. Precedence is `block > redact > audit`.
3. **Request enforcement before `Admit` & SSE sliding window.** `llmproxy` checks bounded request bodies before `opts.Admit`; an input `block` returns HTTP `403` immediately with zero upstream provider calls and zero budget settlement. Non-streaming JSON responses are checked before writing headers. SSE responses use a `512`-byte sliding lookahead window (`SSEFilter`) across Anthropic, OpenAI/OpenRouter and Gemini delta schemas (Gemini since [ADR-0040](0040-agy-gemini-api-key-route.md)) whenever an output `block` or `redact` rule is active, catching patterns split across token chunks while passing zero-overhead passthrough when only audit/input rules apply.
4. **Cross-process cache invalidation & sanitized observability.** `egress-proxy` caches compiled policies per workspace (using workspace secret values so stored secret rotation refreshes compiled secret hashes) and invalidates caches across replicas over NATS `guardrails.invalidate.<workspaceID>`. Interventions never persist raw matched secrets or PII: `llm_guardrail_events`, chat `kind = 'guardrail'` cards, and `kind = 'tool'` (`Kind: guardrail_event`) trace steps record only rule slugs, entity labels, match counts, latency, and a scrubbed summary. If an active CLI turn ends in failure after an input/output guardrail block, `backend/cmd/api` maps the failure to `engine.guardrail_blocked`.
5. **RBAC & zero-red UI.** Viewing policy and audit logs is allowed for all workspace members; mutating policy or custom rules requires `manage_guardrails` (`owner` or `admin`). Settings live at `/settings/guardrails` and inline chat/tool feedback uses blue (redacted) and dashed amber (blocked, 403) tokens without red.
   The settings UI keeps the policy draft local to the current workspace. Custom rule writes refresh the saved policy and rules without replacing any unsaved policy edits.

## Consequences

- Input blocks protect subscription and API budgets because requests fail at the proxy boundary before upstream admission or metering.
- Enabling output `block` or `redact` rules adds at most ~512 bytes of tail lookahead buffering on streamed SSE text deltas and no buffering at all when only input or audit rules are enabled.
- Workspace administrators can safely verify rule interventions in chat, the Tool calls drawer, and the `/settings/guardrails` audit stream without exposing raw secret or PII values in databases, logs, or responses.
