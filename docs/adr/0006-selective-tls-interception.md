<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0006. Selective TLS Interception and Per-Bot CA

- **Date:** 2026-11-14
- **Status:** Accepted

## Context

Beyond LLM APIs, bots run tools like `git clone`, `gh`, `glab`, and HTTP clients against authenticated upstream endpoints without holding real PATs or OAuth tokens in the sandbox (Phase 0 Spike S5). Unconditional TLS MITM across all traffic breaks certificate pinning on third-party binaries and expands attack surface unnecessarily, while passing plaintext credentials into git credential helpers violates zero-secret sandbox isolation.

## Options Considered

### Option A — Per-Bot Ephemeral CA + Selective MITM on Secret-Bound Hosts Only

- **Pros:** `cmd/egress-proxy` (`backend/internal/secrets/mitmproxy`) enforces host/port allowlists on `CONNECT`, forwards allowlisted non-secret hosts via raw TCP passthrough (`ActionTunnelPassthrough`) with untouched end-to-end TLS, and performs selective TLS MITM (`ActionMITMTokenAdded`) strictly for allowlisted hosts that have a configured `SecretRule`. Each bot gets a short-lived ECDSA P-256 Root CA (`NewBotCA(botID)`) whose private key stays solely in proxy memory; only the public CA cert is installed inside the sandbox.
- **Cons:** Requires installing the bot's public CA into system and runtime trust stores on container startup, and binaries with strict public-key pinning cannot use MITM token injection on pinned hosts.

### Option B — Global Shared MITM CA Across All Sandboxes

- **Pros:** Single CA certificate generated once.
- **Cons:** Compromise of one proxy state or CA key would cross workspace and bot boundaries. Rejected.

### Option C — Always-MITM Proxy Across All Destinations

- **Pros:** Uniform TLS inspection pipeline.
- **Cons:** Breaks certificate-pinned tools and adds unnecessary TLS termination CPU/latency on non-secret traffic.

## Decision

Adopt **Option A** in `backend/internal/secrets/mitmproxy`:

1. **Per-bot ECDSA P-256 Root CA (`ca.go`):** Short-lived (24h CA, 12h cached leaf SAN certs) isolated per `botID`; verified via unit test that Bot B's CA rejects leaf certificates issued by Bot A's proxy.
2. **Allowlist & selective MITM (`proxy.go`):**
   - Non-`CONNECT` requests (`405`), disallowed ports (`403`), explicitly blocked hosts (`403`, overriding `open` mode, allowlists, and secret rules per `ADR-0017`), and non-allowlisted hosts (`403`, `ActionBlockedHost`) are denied immediately.
   - Allowlisted hosts without `SecretRule` entries receive blind TCP `CONNECT` passthrough (`ActionTunnelPassthrough`).
   - Allowlisted hosts with `SecretRule` entries strip placeholder `Authorization`, `Proxy-Authorization`, `X-Api-Key`, `X-Goog-Api-Key`, and `Private-Token` headers, inject `Bearer`, `Basic` (e.g. Git HTTP `oauth2:<token>`), or `Raw` credentials, emit `ActionMITMTokenAdded` audit events without secret values, and scrub secret tokens/Base64 forms from upstream `>= 400` error responses and structured logs.
3. **Sandbox trust-store setup (`images/sandbox/install-bot-ca.sh`):** Verified inside `botbox/sandbox:dev` across:
   - System CA bundle (`/etc/ssl/certs/ca-certificates.crt` via `update-ca-certificates`; `openssl verify` -> `OK`)
   - Chromium NSS database (`sql:/home/bot/.pki/nssdb` via `certutil -L` -> `botbox-bot-ca C,,`)
   - Runtime env exports in `/home/bot/.botbox-ca-env`: `SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`, `GIT_SSL_CAINFO`.

## Consequences

- Tools like `git` (`libcurl`/OpenSSL), Chromium, Node.js, Python `requests`, and Go HTTPS clients trust MITM credentials seamlessly without real secrets ever touching container memory, disk, or env.
- Pinned third-party binaries on non-secret hosts continue working over unmodified TLS passthrough; if a future binary with certificate pinning ever requires secret injection, it must use a supported host-side gateway/webhook rather than MITM.
