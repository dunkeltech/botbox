# 0019. Live public GitLab domains and explicit bot assignments

- **Date:** 2026-11-17
- **Status:** Accepted & Implemented; extended for API/Git grants by [ADR-0020](0020-self-hosted-gitlab-cli-grants.md)
- **Supersedes:** ADR-0018's operator target pins and implicit bot access through egress alone

## Context

Requiring `AUTH_BROKER_GITLAB_TARGETS` and a Chromium restart for each public GitLab
installation makes application configuration depend on operator-maintained DNS
pins. Public instances can change addresses without a change in trust. Different
bots also need independent subsets of a workspace's approved instances. An open
egress policy must not implicitly grant all registered GitLab destinations.

## Options

1. Retain operator IP pins. Safe for fixed addresses, but requires manual updates
   and restarts and cannot be administered fully through the application.
2. Check DNS when saving a URL or opening a session, then let Chromium connect
   normally. Rejected: a second lookup or later redirect can reach private targets.
3. Manage exact domains and bot assignments in the UI and enforce public-only,
   origin-bound TLS tunnels at actual connection time.

## Decision

Choose option 3. Workspace owners/admins register exact HTTPS origins. Bot managers
(owners/admins, or a member managing their own bot) explicitly assign zero or more
registered origins in Bot settings. New registrations and new bots have no implicit
assignments. An empty selection permits no self-hosted GitLab login. Existing
GitLab.com behavior remains unchanged.

Assignments are stored in `bot_gitlab_instances`, keyed by workspace, bot and
origin with scoped foreign keys. Both the API and standalone broker require a
current assignment and bot egress permission. The registration and assignment are
locked while creating durable sessions. Removing an assignment transactionally
revokes that bot's affected sessions, cancels pending/fulfilling login requests,
updates cards and resumes turns. Other bots retain their assignments. Removing a
workspace instance removes all its assignments and sessions. Re-adding an origin
or assignment never restores old sessions.

Each custom GitLab Chromium BrowserContext has a broker-local HTTPS CONNECT proxy
bound to precisely that origin on port 443. Chromium's implicit loopback bypass is
removed using `proxyBypassList="<-loopback>"`. QUIC and non-proxied WebRTC UDP are
disabled for broker-managed Chromium. Plain HTTP, other ports, IP destinations,
third-party resources and cross-origin redirects are refused by the proxy.

For each new tunnel, the proxy resolves DNS with a bounded deadline, rejects empty,
private, loopback, link-local, carrier-grade NAT, infrastructure, transition-address
and mixed public/private answers, and dials a validated literal IP. Chromium keeps
end-to-end TLS hostname and certificate validation; no MITM or TLS exception is
introduced. A later connection re-resolves and revalidates, allowing public IP
changes without a restart while preventing DNS rebinding. Tunnel lifetime is
bounded by the broker session limit, and revocation closes associated tunnels.

BrowserContexts persist across individual control-plane calls and are explicitly
disposed with the session. Flattened CDP target sessions are reattached for each
websocket, because session identifiers are scoped to their CDP connection.

`AUTH_BROKER_GITLAB_TARGETS` is removed. Domain additions and bot assignment changes
apply live. The broker still needs the shared `DATABASE_URL` and managed Chromium;
external `AUTH_BROKER_CDP_URL` configurations keep custom GitLab disabled because
this process cannot enforce browser launch protections on external Chromium.

## Consequences

- Public GitLab configuration lives entirely in workspace and bot settings.
- Database or DNS outages and cancelled/timed-out checks fail closed; no fallback
  to a cached registration or unvalidated hostname is allowed.
- Migration to explicit assignment revokes earlier custom GitLab sessions. An
  administrator must assign each bot before a fresh login.
- Private-network GitLab, custom CA setup, non-443 ports, subpath installations,
  third-party asset hosts and cross-origin SSO remain unsupported. These require
  separately reviewed network/adapter policies; none is enabled implicitly.
- Authenticated browser profiles, passwords and cookies remain outside sandboxes.
  Workspace registration still means explicit trust in the upstream application;
  being publicly reachable alone does not make a site a trusted secret recipient.
