# 0017. Explicit Egress Blocklist, One-Click Activity Triage, and Reliable Enforcement

- **Date:** 2026-11-17
- **Status:** Accepted

## Context

Operators reviewing a bot's egress activity log on the **Secrets & network** page need to allow blocked hosts or block permitted hosts in one click (`Issue #31`), and to maintain an explicit per-bot blocklist that works in both `allowlist` and `open` internet modes.

Implementing this safely introduces four non-trivial requirements:
1. **Deny precedence and wildcard safety:** Explicit blocks must override `open` mode, `allowed_hosts`, and secret-derived hosts. If a host is blocked by a wildcard rule (such as `*.tracking.example`), one-click Allow or a group `new_host` approval must not silently delete or narrow that wildcard and unintentionally unblock sibling subdomains.
2. **Manager-only removal of explicit blocks:** In group chats (`ADR-0015`), eligible non-viewer group members may approve previously unknown hosts when `ask_before` includes `new_host`. However, ordinary group approval rights must not let a non-manager bypass an explicit block set by a bot manager.
3. **Concurrency and pattern-specific Undo:** Triage clicks, Outbound policy form edits, multi-browser tabs, and `new_host` approvals all mutate the same per-bot `egress_rules` row. Naive whole-snapshot Undo or timestamp tokens would overwrite concurrent unrelated rules (for example, `Block A → Block B → Undo A` losing `B`).
4. **Reliable proxy adoption and tunnel revocation:** When a domain is blocked, open passthrough tunnels and in-flight `CONNECT` dials must be closed before claiming `applied` status; bytes already forwarded prior to block cannot be recalled.

## Options Considered

1. **Best-effort NATS invalidation + exact-host `InvalidateHost` + whole-policy Undo snapshots:**
   - *Rejected.* Publishing on NATS without proxy acknowledgement cannot confirm tunnel closure; exact-host `InvalidateHost` misses wildcard blocks (`*.example.com`) and `CONNECT` calls suspended mid-`DialContext`; whole-policy Undo snapshots clobber concurrent changes to unrelated rules.
2. **Serialized DB revisions (`428`/`409`) + deny-first evaluation + durable invalidation intents + proxy acknowledgement + pattern-specific Undo:**
   - *Accepted.*

## Decision

1. **Deny-first evaluation and wildcard protection (`internal/secrets/mitmproxy`, `internal/egresspolicy`):**
   - `egress_rules` adds `blocked_hosts TEXT[] NOT NULL DEFAULT '{}'`, monotonic `revision BIGINT`, `acknowledged_revision BIGINT`, and `acknowledged_at`.
   - `EvaluateHostAccess` checks `blocked_hosts` before `PolicyOpen`, `allowed_hosts`, and active secret rules. Exact and wildcard blocks override secret rules without exposing or deleting secret values.
   - Only identical normalized patterns present in both `allowed_hosts` and `blocked_hosts` are rejected (`400 validation_error`); broader overlaps (`*.example.com` allowed with `api.example.com` blocked) are valid deny exceptions.
   - Wildcard patterns keep existing semantics (`*.example.com` matches subdomains and nested subdomains, never the apex `example.com`). One-click Allow and `new_host` approvals never silently delete or narrow a wildcard block (`409 approval.wildcard_blocked` for approvals; UI directs managers to edit the blocklist rule).
2. **Authorization & serialized writes (`internal/egresspolicy`, `internal/approvals`):**
   - All policy writers (`PATCH .../egress` and `new_host` approvals) acquire a per-workspace/per-bot PostgreSQL advisory lock (`LockBotEgressRule`, covering the implicit `revision = 0` default before a row exists), validate the full resulting policy, increment `revision` only on real changes, and record `bot.egress_updated` in `audit_log` plus a durable row in `egress_invalidation_intents` in one transaction.
   - `PATCH .../egress` requires `expectedRevision` (missing → `428 precondition_required`; stale → `409 policy_conflict`); omitting `blockedHosts` preserves existing blocks while `[]` clears them.
   - Removing an exact explicit block via a `new_host` approval requires bot-management rights (`owner`, `admin`, or `member` owning the bot); other group members receive `403 auth.forbidden` and the card stays pending.
3. **Reliable proxy enforcement (`cmd/egress-proxy`, `internal/secrets/mitmproxy`):**
   - `Handler` tracks pending dials and active passthrough connections under `passthroughMu` so concurrent policy adoptions cancel matching in-flight dials and close affected exact and wildcard passthrough tunnels before invoking `OnPolicyAdopted` (`AcknowledgeEgressPolicyRevision`).
   - Existing MITM connections re-evaluate `resolveRuleForRequest` per request and reject blocked destinations with `403` and `ActionBlockedHost`.
   - Running proxies load the current policy before accepting traffic, subscribe to revision-aware NATS invalidations (`egress.invalidate.<ws>.<bot>`) with request-reply acknowledgement, and run periodic authoritative revalidation with a `1 s` maximum freshness window (closing active passthrough tunnels and failing closed on refresh error or expiry).
   - `PATCH .../egress` returns `200` (`applied`) once the running proxy acknowledges the revision (or `not_running` when stopped, applying before network readiness on next launch), and `202` (`pending`) if acknowledgement does not arrive within the bounded wait.
4. **Frontend triage & pattern-specific Undo (`BotSecretsView`):**
   - Authoritative `savedPolicy` and revision stay separate from unsaved form drafts; log-row triage never implicitly saves or discards form edits and surfaces a rebase prompt if drafts conflict.
   - `Undo` stores only the inverse delta for the single affected normalized pattern and applies it with `expectedRevision` against current state, refusing to overwrite if that pattern changed concurrently (`409` or rule divergence) while preserving unrelated newer rules.
   - Uncertain outcomes (timeouts/lost responses) reload `GET .../egress` before enabling further mutations and lock writes visibly if reconciliation fails.

## Consequences

- `botbox-egress-<bot>` containers now receive `NATS_URL` (`BOTBOX_EGRESS_NATS_URL`) so running proxies receive revision-aware invalidations directly.
- All first-party callers of `PATCH .../egress` must pass `expectedRevision`.
- Security boundary remains explicit: new requests obey current policy and affected passthrough tunnels are closed on adoption, while bytes already forwarded before enforcement cannot be recalled.
