<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0020. Instance-bound GitLab CLI credentials and actionable request failures

- **Date:** 2026-11-17
- **Status:** Accepted & Implemented
- **Extends:** ADR-0016 and ADR-0019

## Context

ADR-0019 supports custom GitLab browser sessions, while `gitlab_api` and
`gitlab_git` still resolve only to GitLab.com. Consequently a bot can reach a
self-hosted sign-in page and install glab, but its credential request fails before
any secret card is created. An intercepted request failure becomes a generic
`engine.turn_failed`, hiding the recovery action.

Simply replacing an adapter hostname would also let inherited workspace tokens
bypass bot assignments and could reactivate old credentials after reassignment.
The official glab CLI resolves `GITLAB_TOKEN` for its configured host and uses a
`PRIVATE-TOKEN` access-token header; normal `glab auth login` would store real
credentials inside its filesystem or keyring, violating ADR-0016.

## Options

1. Pass the real token to glab or git. Rejected: exposes credentials to the sandbox,
   process environment, command arguments, keyring and filesystem.
2. Allow arbitrary target hosts for every credential adapter. Rejected: loses
   reviewed destination and protocol boundaries.
3. Bind the reviewed GitLab API/Git policies to registered, explicitly assigned
   instances, keeping real tokens in the proxy and local CLI values non-secret.

## Decision

Choose option 3. Shared `gitlabaccess.Check` enforces registration, bot assignment
and egress policy for custom browser/API/Git targets. Creation and fulfillment
lock assignments transactionally; no external vault or browser I/O is done while
holding these locks. Requests without a target retain the GitLab.com default.
Other providers retain fixed origins. Custom origins remain HTTPS DNS hosts on
port 443, with no userinfo, URL payloads, wildcards or subpath configuration.

For custom origins, `gitlab_api` injects the real token as `PRIVATE-TOKEN` with raw formatting only on
reviewed REST `/api/v4/` routes. `gitlab_git` injects Basic `oauth2:<token>` only on
reviewed smart-HTTP Git routes. Encoded traversal, wrong Host headers, methods,
ports and foreign origins cannot receive credentials. Placeholder auth headers
are stripped before injection. CLI requests default to `GITLAB_TOKEN` and return
only a generated placeholder plus safe host configuration guidance. Bots use
`glab api user` to verify access, without performing a real-token auth login.

Saved bot and inherited workspace credentials are available in every
conversation of the same bot. When a turn calls `request_secret` and an eligible
saved rule matches the exact GitLab origin and adapter, the worker continues the
same turn with a non-secret placeholder before asking a human. This lookup
checks metadata and respects per-bot workspace exclusions and egress policy.
The proxy remains responsible for decrypting the credential and enforcing
authorization on each request. The worker tries a saved credential once per
GitLab adapter in a turn so a rejected token can still be replaced through the
normal secret request flow. This keeps a new conversation from pausing just
because its model session has never seen the CLI placeholder.

Previously registered origins are retained in `gitlab_known_origins` until their
workspace is deleted. This is revocation metadata, not an active registration.
It prevents a legacy exact-domain or parent-domain workspace secret from restoring
access after an instance is removed. For these origins the proxy ignores generic
credential templates and secret-derived host allowances: only reviewed GitLab
adapters and the bot's explicit egress policy can grant access. Existing generic
secrets can be replaced through a new authorized reviewed-adapter request; their
values are never migrated into a sandbox.

The egress proxy reloads authoritative assignment and credential exclusions with
every policy snapshot, including within established MITM tunnels. Stored template
fields cannot override reviewed header/scheme/port behavior. Registered custom
GitLab hosts require mediation even when no usable grant exists; a credential-bearing request with a missing,
expired or revoked grant cannot fall back to a raw tunnel. Unauthenticated public
page probes remain available through the same public-DNS-checked MITM connection. Every new upstream
connection uses shared `netguard` public-DNS validation and literal-IP dialing,
with normal upstream TLS certificate verification. DNS failures and mixed or
private/infrastructure answers fail closed. Other egress targets retain their
existing network policy behavior.

Credentialed custom GitLab tunnels are tracked and closed when their host rules
change, are invalidated, or authoritative policy refresh fails. Invalidations
are published after commits; periodic authoritative revalidation provides a
fallback when publication is unavailable. Response mediation remains active for
all HTTP statuses, with the existing size/compression/binary restrictions.

Unassigning a bot deletes its persisted GitLab bot credentials, revokes its leases,
cancels pending/fulfilling requests, and records per-bot exclusions for inherited
GitLab workspace secrets. Other bots can continue using the shared secret. Re-
adding the assignment never removes these exclusions. A new explicit workspace-
secret fulfillment can clear the exclusion for the requesting bot only. Removing
the instance deletes its persisted API/Git credentials in both scopes and revokes
all affected leases and requests. These are proxy authorization revocations, not
remote revocations of the personal access token at GitLab.

Expected request failures persist safe tool results and allowlisted turn codes:
registration missing, assignment missing, host blocked, invalid input, rate limit,
unsupported capability or policy unavailable. The chat translates these into
specific recovery hints. Raw database/dependency errors and credential values
never appear in these results. A failed intercepted request ends that turn with
its recovery hint; no phantom pending request is created.

## Consequences

- Self-hosted glab and Git HTTPS work without moving real tokens into a sandbox.
- Browser, CLI and inherited-workspace access share the same target authorization.
- Reassignment can require the human to provide a credential again; this is
  intentional rather than silently restoring an old grant.
- Migrations normalize custom-instance `gitlab_api` records to PRIVATE-TOKEN
  formatting while preserving GitLab.com Bearer compatibility (including OAuth).
  Encrypted values and their encryption context remain unchanged.
- glab is declared in devenv for deterministic integration tests against a local
  fake GitLab. Tests never call a real LLM or GitLab API.
- REST and smart-HTTP Git are supported. GraphQL, SSH, alternate origins/ports,
  private targets, custom upstream CAs and cross-domain SSO remain outside this
  adapter set. GitLab.com remains backward compatible with its existing policy.

The protocol behavior is documented by GitLab's
[REST authentication](https://docs.gitlab.com/api/rest/authentication/) and
[glab authentication](https://docs.gitlab.com/cli/authentication/) references.
