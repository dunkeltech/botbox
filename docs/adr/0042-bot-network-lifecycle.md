<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0042. Remove bot networks with the computer and reclaim orphans on a full pool

- **Date:** 2026-11-20
- **Status:** Accepted

## Context

The Docker driver gives every bot its own internal bridge network
([ADR-0009](0009-metered-sandbox-proxy.md),
[ADR-0038](0038-split-egress-and-chromium-isolation.md)). Native Antigravity
ACP bots get a second one for their companion
([ADR-0041](0041-native-antigravity-acp.md)). Docker takes a whole subnet for
each network from its default address pools. Those pools hold about 30
networks on both Docker Engine and OrbStack, shared with every other project
on the host.

The driver created these networks but never removed them. Destroyed computers,
containers removed outside bot.Box, and the move from the legacy
`botbox-net-<bot>` network to the metered `botbox-egress-net-<bot>` network all
left their subnet behind. Once the pool was full, Docker refused every new
network with "all predefined address pools have been fully subnetted". No new
computer could start, and the UI blamed an unreachable sandbox agent.

## Options

1. Remove the bot's networks when its computer is destroyed.
2. Sweep unused bot networks periodically or at API startup.
3. Reclaim unused bot networks only when Docker reports a full pool.
4. Create bot networks with explicit small subnets (for example /28) from a
   dedicated range.
5. Leave cleanup to operators (`docker network prune`).

## Decision

Use options 1 and 3, and report a full pool as a typed error.

- `DestroyEnvironment` removes the desktop, ACP and legacy networks of the
  bot after its containers and proxies. A failure is logged and does not fail
  the delete, because the computer is already gone.
- When network creation fails with a full pool, the driver lists networks
  labeled with a bot ID, removes the unused ones and retries once.
- A network is removed only if its name is one of the names of the bot in its
  labels, its workspace and bot labels are set, and no container uses it,
  stopped or created containers included. Removing a network under a stopped
  container would keep that container from ever starting again. Deletion goes
  by network ID, so a network recreated under the same name in the meantime
  stays.
- Networks younger than two minutes stay, because a concurrent start creates
  the network before it attaches the first container.
- If no subnet can be freed, the driver returns `compute.ErrNetworkCapacity`.
  The API answers `503 environment.network_capacity` with a fixed message,
  because the raw Engine error names networks of the operator's host.

Option 2 was rejected because it pays for a host-wide scan on every start or
on a timer, while the leak only matters when the pool is full. Option 3 alone
would also clean up on time, but option 1 frees subnets early and keeps the
host tidy. Option 4 would raise the bot limit per host by an order of
magnitude, but needs a configurable range that does not overlap with other
networks on the host; it remains follow-up work. Option 5 removes networks of
other projects and does not help operators who do not know the cause.

## Consequences

- One Docker host still holds at most one bot network per pool entry for
  every bot with a computer, plus one more per native ACP bot. On OrbStack that
  is fewer than 30 computers in total, shared with other projects on the host.
- Reclaim works across workspaces. It is a host-level resource cleanup that
  reads and deletes only Docker networks, never workspace data, and the label
  and name checks keep it to bot.Box networks.
- A start that hits a full pool costs one list request and one container
  query per bot network before it fails or succeeds.
