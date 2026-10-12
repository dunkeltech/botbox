# 0013. Reach the sandbox agent on a loopback port

- **Date:** 2026-11-16
- **Status:** Accepted

## Context

Chat turns run the CLI through the sandbox agent over gRPC. The API dialed the container's bridge address, for example `192.168.223.3:9090`. On Docker Desktop for macOS that route is interface-scoped (`bridge` + `IFSCOPE`). Go's `connect` then returns `EHOSTUNREACH` (`no route to host`) in a few tens of milliseconds. The UI wraps that as `engine.provider_failed`, which looks like a bad credential. Browsers and other non-Go clients still reach the desktop on the same bridge, so interactive `claude` inside the computer keeps working.

The agent already requires a per-sandbox token. The port does not need to be reachable from any other machine.

## Options Considered

### Option A — Keep dialing the bridge address

- **Pros:** No published port. Linux bridges that are not interface-scoped already work.
- **Cons:** Chat cannot run on the Docker Desktop hosts this project uses for development. The failure is indistinguishable from a provider error. An ad-hoc binary gets `EHOSTUNREACH` for that address even when a system Python or `nc` connects.

### Option B — Publish container port 9090 on 127.0.0.1 only

- **Pros:** The API dials loopback, which this process can route. Docker assigns a free host port, so two bots do not collide. The token check stays. The port is not on `0.0.0.0`.
- **Cons:** Bot networks are internal, so the sandbox has no route except its egress proxy. Docker accepts the binding and then does not allocate a host port. Turning the network off internal would publish the port and would also give the sandbox a path around the proxy.

### Option C — Tunnel gRPC through the Docker socket

- **Pros:** The API already owns the Engine socket. The relay inside the sandbox connects to `127.0.0.1:9090`, so the host never dials the bridge. The network stays internal. No host port is opened. A published loopback port can still be used when an engine actually allocates one.
- **Cons:** Each agent RPC starts a short-lived exec. The sandbox image has to provide the fixed relay interpreter (`/usr/bin/python3`).

## Decision

Option C, with the loopback dial from option B when `NetworkSettings.Ports` shows a real loopback host port. Bindings on any other address are ignored. `BuildContainerSpec` still requests `127.0.0.1` with an empty host port so an engine that publishes internal-network ports can take the direct path. On an internal network the request is unused, and the dialer opens `POST /containers/{id}/exec` with a fixed `/usr/bin/python3` splice. The command has no caller input and no secret.

## Consequences

- Chat on Docker Desktop does not depend on the bridge route or on a host port that internal networks do not allocate.
- An existing computer does not need a recreate for the tunnel. The home volume stays. A computer that was already recreated keeps that volume too.
- The desktop websockify port is unchanged. Browsers already reach it on the bridge.
- The agent token check is unchanged. The tunnel carries the same gRPC metadata.
