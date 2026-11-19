<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0038. Split egress and require Chromium renderer isolation

- Status: accepted.

## Context

The bot-facing egress sidecar has held the deployment encryption key,
database access and the browser broker token. Application-level scoping
does not constrain a compromised sidecar that can use those credentials
directly. Both Chromium launchers disabled the browser's own sandbox.

## Options

- Keep policy, data access and the bot listener in one container and add
  stricter database roles. This leaves decryption and upstream credentials at
  the ingress boundary.
- Use one shared authenticated egress service with a workload registry.
  This reduces process overhead but adds shared service discovery, registration
  and cross-bot connection management.
- Keep the existing per-bot handler lifecycle in a trusted worker. Put a small
  relay in front of it and constrain the relay at the Docker and TLS boundaries.

## Decision

Use a per-bot relay and worker. The relay and sandbox join an internal Docker
bridge in isolated gateway mode. The worker joins that bridge and the outer
bridge, binds its listener to the isolated interface and disables IP forwarding.
A pair of independent Ed25519 keys pins both endpoints. Each TLS handshake uses
a short-lived certificate. The instance keys last for the lifetime of the pair.
Removing the worker revokes the old pairing and terminates its connections.

Only the trusted worker receives the configured database, key ring, NATS and
broker settings. It continues to execute the existing scoped egress, LLM,
budgeting, MCP, memory and browser-broker handlers. The worker remains a trusted
control-plane component with significant access to shared services. Deployments
can provide separate database and NATS roles via the egress overrides.

The worker publishes its public interception CA to a dedicated volume mounted
read-only into its desktop. Its private signing key is envelope encrypted in a
separate, worker-only volume and rewrapped on restart when the key ring changes.
The CA survives a relay or worker replacement. It is valid for one year. Rotate
the private and public CA volumes during a scheduled bot restart before expiry.

For Chromium, use the non-root namespace sandbox and keep Docker capability
drops and `no-new-privileges`. The Linux Seccomp profile is based on the
[upstream Moby profile at commit 4ebf927](https://github.com/moby/profiles/blob/4ebf927f08370a93ef8eddcfc71370f965a9fb51/seccomp/default.json).
It adds only the namespace operations used by Chromium on amd64 and arm64.
The broker qualifies a renderer on startup and binds subsequent CDP connections
to the verified browser instance. External CDP is configurable through an
operator-managed loopback endpoint and is subject to the same probe.

## Consequences

Build the sandbox, proxy and broker images before upgrading the API. Docker
must support isolated bridge gateway mode. API startup removes legacy
credential-bearing relays and stops affected computers. The next normal
computer start creates the new pair and isolated network and reuses the home
volume. The old container is kept stopped under a retired name for recovery
of files outside the home volume. Remove retired containers after verifying
the upgrade. Rotate the old egress service credentials according to the
deployment's credential management procedure.

The developer broker runs in a Linux Docker container to exercise the same
sandbox on macOS and Linux. Its API port is published to loopback. CDP remains
inside the broker container. A browser that cannot pass the probe prevents the
broker from serving login requests. An incompatible desktop browser exits
while other supervised desktop services remain available.
