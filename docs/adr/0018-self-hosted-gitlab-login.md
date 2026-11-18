<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0018. Workspace-approved self-hosted GitLab browser login

- **Date:** 2026-11-17
- **Status:** Superseded in part by [ADR-0019](0019-live-public-gitlab-and-bot-assignment.md); retained for the original browser-login decision

## Context

ADR-0016 binds reviewed login adapters to fixed SaaS origins. The `gitlab_web`
adapter therefore rejects GitLab installations on an organization's own domain,
even when the bot has egress permission. Simply accepting any `target_origin`
would allow a bot to direct credentials or broker network traffic to arbitrary
services. A host allowlist alone is not permission to receive credentials.

## Options

1. Add a compiled adapter for every domain. This preserves exact destinations but
   requires a release for every installation and duplicates reviewed form rules.
2. Accept arbitrary origins for any adapter. This loses the reviewed destination
   boundary and exposes the broker's network to bot-selected targets.
3. Parameterize only the reviewed GitLab adapter with explicit workspace and
   operator approval, retaining fixed login paths, selectors and redirect policy.

## Decision

Choose option 3. Owners and admins register immutable named HTTPS origins in
`gitlab_instances`, scoped by workspace, with a limit of 20. All workspace roles
can read this non-secret configuration. Removal revokes durable broker sessions,
cancels pending/fulfilling requests, updates their chat cards and resumes waiting
turns without credentials. Re-registration never restores revoked sessions.

`request_secret(kind="browser_login", adapter_id="gitlab_web",
target_origin="https://gitlab.example.org", ...)` resolves the existing reviewed
GitLab form policy at the configured origin. Omitted origins retain GitLab.com
behavior. Other providers and `cli_egress` adapters retain fixed origins.

The standalone broker requires `AUTH_BROKER_GITLAB_TARGETS`, containing comma-
separated `https://hostname=public-IP` entries, and `DATABASE_URL`. Operator pins
are installed in broker-managed Chromium's host resolver, preventing DNS changes
from redirecting an approved hostname into private infrastructure. Raw IP URLs,
private target addresses, URL credentials, query strings, fragments, non-443 ports,
subpath installations and wildcard origins are rejected. Custom targets cannot
be enabled with an externally managed `AUTH_BROKER_CDP_URL`, where this process
cannot enforce resolver pins. TLS verification is never disabled.

The broker checks workspace registration, bot egress policy (including blocks),
and durable session revocation before every custom-session operation, including
credential submission, screenshots and human takeover. Missing policy, database
errors and cancelled or timed-out checks fail closed. Creation rechecks and locks
the registration while persisting the session so concurrent deletion cannot miss
it. The broker token remains control-plane only; bots cannot supply registrations
or authorization decisions through tool arguments.

Only explicitly reviewed GitLab installations are trusted credential recipients,
as with the existing adapters. Operator approval must account for the broker's
network access. IP pins are not a general network firewall for third-party page
resources; deployment network restrictions remain necessary for broker isolation.

## Consequences

- Passwords, cookies and authenticated profiles remain outside bot sandboxes.
- Same-origin GitLab MFA uses the existing human-only takeover. Cross-origin SSO
  needs a separately reviewed identity-provider adapter; no automatic redirect
  approval is introduced.
- Operators must update target pins and restart the broker when an IP changes.
- This first version supports publicly addressed HTTPS instances with valid
  certificates. Private addresses, custom trust roots and subpaths are deferred.
- The broker needs database connectivity for custom-session authorization; a
  policy outage revokes the in-memory session and requires a new login.
- Self-hosted API/Git credential injection is a separate follow-up. It must bind
  saved rules and leases to the chosen instance as well as the adapter.
