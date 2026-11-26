<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# bot.Box Threat Model & Security Architecture

This document describes the bot.Box security model, trust boundaries, enforced
controls, key lifecycle, and accepted residual risks for single-host Docker
deployments.

## 1. Assets & Security Invariants

1. **Credentials & secrets:** LLM provider keys/OAuth tokens, GitLab tokens,
   MCP server auth tokens, and custom HTTP secrets. **Invariant:** secret
   plaintext values are write-only at the API, encrypted at rest with
   AES-256-GCM envelope encryption, and **never** enter bot sandboxes,
   prompts, logs, error messages, API responses, or audit metadata.
2. **Encryption keys & CA keys:** The Key Encryption Key (KEK) ring
   (`BOTBOX_SECRETS_KEK` / `BOTBOX_SECRETS_KEK_FILE`), the JWT Ed25519 signing
   key (`BOTBOX_JWT_PRIVATE_KEY`), the VNC HMAC ticket key (`BOTBOX_VNC_SECRET`),
   and per-bot MITM CA private keys (`ca.envelope`).
3. **Tenant isolation:** Multi-user workspaces (`owner`, `admin`, `member`,
   `viewer`). Every database query, REST route, and NATS/WebSocket channel is
   scoped by `workspace_id`. Cross-workspace resource references return `404`.
4. **Host & network integrity:** The host OS, Docker daemon socket, PostgreSQL,
   NATS, and private LAN/cloud metadata endpoints (`169.254.169.254`,
   `168.63.129.16`, RFC 1918, loopback).

## 2. Trust Boundaries

| Boundary | Components | Controls |
| --- | --- | --- |
| **Untrusted browser / internet** → **Gateway (`Caddyfile`)** | Caddy → Next.js (`:3000`) & Go API (`:8080`) | Single origin, TLS termination (production), security headers & CSP, CSRF header (`X-BotBox-CSRF`), `HttpOnly`/`SameSite=Lax` refresh cookies, trusted-proxy IP extraction, escalating DB-backed login lockout, WebSocket origin checks. |
| **Control plane** (`api`, `auth-broker`) | Go API, Postgres, NATS, `auth-broker` | `CapDrop: ALL`, `no-new-privileges:true`, `read_only: true` rootfs with bounded `tmpfs`, dedicated `auth-broker` Chromium seccomp + renderer sandbox verification, secret-scrubbing `slog` JSON logs. |
| **Trusted egress worker** ↔ **Bot relay** | `botbox-egress-worker-*` ↔ `botbox-egress-*` | Split egress ([ADR-0038](adr/0038-split-egress-and-chromium-isolation.md)): only the worker sees Postgres/KEK/NATS/auth-broker; relay and worker authenticate with ephemeral Ed25519 mTLS; worker binds solely to the bot's isolated subnet (`ip_forward=0`). |
| **Bot sandbox** (`botbox-bot-*`) | XFCE4, Chromium, `supervisord`, agent CLIs, `botbox-agent` | Unprivileged user `bot` (`uid 1000`), `Privileged: false`, `CapDrop: ALL`, `no-new-privileges:true`, custom Moby+Chromium seccomp (optional `BOTBOX_DOCKER_RUNTIME=runsc`), internal-only Docker bridge (`gateway_mode_ipv4=isolated`), named volumes only (no host binds, no Docker socket), gRPC token auth. |

## 3. Threat Actors & Mitigations

### Compromised bot sandbox (malicious code or prompt injection)
- **Network escape / SSRF:** The sandbox has no default internet route and can
  reach only its local relay (`http://egress:8081`). `netguard.DialPublic`
  resolves DNS once, rejects private/loopback/link-local/metadata/mixed answers,
  and dials the literal validated IP (defeating DNS rebinding). Redirects are
  blocked on LLM and MCP upstream clients.
- **Credential exfiltration:** Real credentials exist only in Postgres (encrypted)
  and the trusted egress worker. Sandboxes receive placeholders; `mitmproxy`
  enforces `Host` == `CONNECT` target on every credential injection, validates
  header names/values against CRLF/routing header injection, and scrubs raw,
  base64, and URL-encoded secret forms from upstream responses.
- **Container escape:** Capabilities are dropped (`ALL`), `no-new-privileges`
  and seccomp block dangerous syscalls, PIDs (`512`) and RAM/CPU are bounded,
  and `ValidateContainerSpec` forbids host bind mounts and `docker.sock`.

### Malicious workspace member / cross-tenant attacker
- **Authorization:** `loadWorkspaceMembership` enforces membership before any
  resource lookup and returns `404` for non-members. `workspaces.Can` enforces
  RBAC across `owner`, `admin`, `member`, and `viewer`.
- **Direct messages & VNC:** DM conversations are bound to their human
  participant (`GetPrivateConversation`). VNC WebSockets require short-lived
  HMAC tickets tied to `(workspaceID, botID, userID, canControl)` and
  server-side RFB input filtering drops mouse/keyboard frames unless the user
  holds the desktop control lock.

### Network / Web attacker
- **CSRF & XSS:** Mutating routes require `X-BotBox-CSRF: 1`; Markdown messages
  disallow raw HTML and external image fetching; strict CSP (`frame-ancestors
  'none'`, `object-src 'none'`) protects authenticated pages.
- **Brute force & token replay:** Sliding-window IP/email rate limits plus
  persistent 5-failure account lockout (`15 min`). Locked accounts use the same
  public response and dummy password verification as unknown accounts. Refresh tokens rotate on
  every use and revoke the entire token family on reuse detection.

## 4. Key & Certificate Lifecycle

- **Per-bot MITM CA:** Private keys are stored envelope-encrypted (`0600`) in
  worker-only volumes (`botbox-egress-ca-private-*`); only the public cert
  (`ca.crt`) is mounted read-only into the sandbox.

### Rotate the KEK

1. Deploy `BOTBOX_SECRETS_KEK="v2:<new>,v1:<old>"` to the API and recreate
   egress workers with both keys before rotating database rows. This lets workers
   read both versions while writers use `v2`.
2. Run `kek-rotate` with the same key ring. It rewraps DEKs in
   `workspace_secrets`, `bot_secrets`, `secret_leases`, `mcp_servers`, and
   `codex_plan_connections` without decrypting secret values. Run
   `kek-rotate --dry-run` afterward and confirm the pending total is zero.
3. Inventory every `botbox-egress-ca-private-*` volume, including stopped bots
   and Agy ACP companions. Start each worker with both keys so it can rewrap its
   CA envelope as described in [ADR-0038](adr/0038-split-egress-and-chromium-isolation.md).
   From the trusted Docker host, verify that the `KeyID` field in each existing
   `ca.envelope` is `v2`. Read only the key ID during verification.
4. Retire `v1` only after the database check passes and every existing CA envelope
   is verified. Keep both keys deployed while any volume is pending or unavailable.

## 5. Accepted Residual Risks

- **API holds Docker socket:** The `api` container requires `/var/run/docker.sock`
  to manage per-bot containers and networks. Mitigation: `api` runs with
  `CapDrop: ALL`, `no-new-privileges:true`, `read_only: true`, and strict
  `ValidateContainerSpec` invariants; sandboxes never see the socket.
- **Writable sandbox rootfs:** Bot sandboxes keep a writable root filesystem so
  coding agents can install packages and build artifacts inside their container.
  Mitigation: `bot` has no host mounts, no `CAP_*`, PID/RAM limits, and isolated
  egress; optional `BOTBOX_DOCKER_RUNTIME=runsc` adds gVisor user-space kernel
  isolation.
- **Single-host Docker Engine:** Resource starvation or kernel vulnerabilities on
  a shared host affect co-located containers. Mitigation: host capacity guards
  (`BOTBOX_MAX_RUNNING_SANDBOXES`, `BOTBOX_MAX_SANDBOX_MEMORY_MIB`), idle
  auto-stop, and optional gVisor (`runsc`).

## 6. Reporting Vulnerabilities

Report suspected security issues privately to `security@dunkel.tech` rather than
opening a public GitHub issue.
