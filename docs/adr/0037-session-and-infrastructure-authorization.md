<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0037. Session and infrastructure authorization

- **Date** 2026-11-19
- **Status** Accepted

## Context

Session rotation and workspace ownership checks need a common lock when
requests affect the same family or workspace. Group history must survive
member removal. The desktop relay and egress sidecar also enforce permissions
after clients leave the normal HTTP API path.

## Options

1. Rely on individual refresh tokens, membership rows and short-lived tickets.
2. Store and check authorization state at the scope of each operation.

## Decision

Use option 2. Keep an authoritative refresh-family row and lock it for rotation,
reuse detection and logout. Return the existing access token when a signed-in
user accepts an invitation. Lock the workspace for membership mutations and
read the actor's role under that lock. Reference historical message authors
independently of their active membership. Check new authorship at write time
and cascade the removal of group grants.

Enforce the desktop ticket expiry on open connections. Recheck workspace
membership periodically and recheck the control holder before forwarding
controller input. Parse the RFB 3.8 byte stream and allow only view-related
messages from spectators.

Use each bot's Docker-managed internal network as its sidecar ingress
identity. Bind to the interface in that network and verify the socket peer.
Resolve outbound CONNECT and credentialed HTTPS destinations to public
addresses and dial those addresses directly. Generate a local development
broker token at startup and bind the broker to loopback by default.

## Consequences

Previously issued refresh tokens remain usable when their families were still
active. Existing group authorship is preserved. The browser obtains a fresh
desktop ticket after a stream ends. Proxy sidecars require the updated image,
and running sandboxes need to reload the replacement sidecar's CA.
