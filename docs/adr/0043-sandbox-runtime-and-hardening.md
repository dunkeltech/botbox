<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0043. Sandbox runtime options and control-plane read-only roots

- **Date:** 2026-11-26
- **Status:** Accepted

## Context

Phase 8 security hardening requires reviewing sandbox container isolation
(seccomp, capabilities, read-only root filesystems, and host mounts) and
evaluating gVisor (`runsc`) alongside the default `runc` + custom seccomp
runtime adopted in [ADR-0038](0038-split-egress-and-chromium-isolation.md).

Bot sandboxes run XFCE4, Chromium, `supervisord`, and developer CLIs (`claude`,
`codex`, `pi`, `agy`) that write package caches, tool binaries, and temporary
build files inside the container during coding tasks. By contrast, control-plane
containers (`api`, `auth-broker`, `egress-proxy`) have fixed runtime paths.

## Decision

1. **Default runtime remains `runc` with the hardened Chromium seccomp profile**
   (`CapDrop: ALL`, `Privileged: false`, `no-new-privileges:true`, isolated
   internal Docker bridges, no host mounts, no Docker socket).
2. **Add opt-in `BOTBOX_DOCKER_RUNTIME=runsc` (gVisor) support** validated against
   an explicit allowlist (`""`, `"runc"`, `"runsc"`). Operators running Docker
   with the `runsc` runtime configured can enable user-space kernel isolation
   per deployment. Note that Chromium's user-namespace sandbox under gVisor
   depends on the host gVisor version/flags and should be smoke-tested on the
   target host before enabling `runsc`.
3. **Keep bot sandbox rootfs writable** so bots can install project tools and
   build dependencies inside their isolated container, while enforcing in
   `ValidateContainerSpec` that only named Docker volumes (never host paths or
   `docker.sock`) may be mounted.
4. **Enforce read-only root filesystems on control-plane containers:**
   `egress-proxy` relays and workers already run with `ReadonlyRootfs: true`
   ([proxy.go](../../backend/internal/compute/docker/proxy.go)); Compose now
   also sets `read_only: true` with bounded `tmpfs` `/tmp` mounts for `api` and
   `auth-broker`.
5. **Expose egress metrics only on the control bridge.** The Compose monitoring
   overlay enables a worker listener on the control-side address and uses a
   shared, control-network DNS alias for Prometheus discovery. This keeps the
   listener outside the sandbox network and lets Prometheus discover new and
   recreated workers without direct access to the Docker socket.

## Consequences

- `ValidateContainerSpec` rejects any host-path bind mount or unknown OCI runtime.
- Operators gain an optional `BOTBOX_DOCKER_RUNTIME=runsc` defense-in-depth knob
  without breaking developer workflows or existing Chromium namespace isolation.
- Control-plane containers cannot mutate their own binaries or `/etc` at runtime.
