# 0016. Zero-context in-chat secret requests (CLI egress adapters and isolated browser broker)

- **Date:** 2026-11-16 (updated 2026-11-17)
- **Status:** Accepted & Implemented (`cli_egress` HTTPS adapters + isolated `cmd/auth-broker` for `browser_login`)

## Context

Bots working in direct messages or group chats regularly encounter tools or web portals that require credentials (for example `gh` talking to `api.github.com` or logging into an internal staging web application). Asking users to paste tokens or passwords into chat breaks bot.Box security principle 1: secrets must never enter the LLM prompt, chat history, sandbox environment, sandbox files, process arguments, tool results, screenshots, or logs.

Earlier designs considered a `BlindFillBrowser` RPC on the sandbox agent (`127.0.0.1:9090`) targeting the sandbox Chromium instance (`127.0.0.1:9222`), and streaming `200 OK` HTTP responses straight back from the egress proxy (`backend/internal/secrets/mitmproxy/proxy.go`). Both approaches fail under scrutiny:

1. **Sandbox Chromium is inside the bot's trust boundary.** `CDPBrowser.Read` (`backend/internal/agent/browser.go`) reads `el.value`, and any process inside the sandbox can connect directly to `127.0.0.1:9222`, read cookies/profiles, run `Runtime.evaluate`, reveal password inputs, or capture frames. Hiding a tool from MCP does not isolate loopback CDP or browser state.
2. **Allowlisting a host is not response mediation.** `mitmproxy` currently scrubs error responses (`status >= 400`), while `2xx`/`3xx` bodies stream through `io.Copy` unscrubbed, and `resolveRuleForPath` (`backend/internal/secrets/mitmproxy/policy.go`) falls back to the connection-open `SecretRule` if dynamic policy reload fails or returns empty rules. An established `CONNECT` tunnel would therefore continue injecting revoked or expired secrets and could echo tokens in `200 OK` responses.
3. **Ephemeral credentials need deterministic expiry and revocation.** A credential granted for one turn must not linger across subsequent queued turns or outlive cancellation on a persistent HTTP/2 or keep-alive connection.

## Options Considered

### Option A — Blind CDP fill in sandbox Chromium + unmediated `200 OK` proxy responses

- **Pros:** Minimal infrastructure; reuses existing sandbox container.
- **Cons:** Violates the security boundary. Any sandbox shell command or CDP client can read injected credentials, cookies, or session tokens. Revoked leases survive on open `CONNECT` tunnels and failed dynamic-policy reloads.

### Option B — Trusted CLI HTTPS adapters with proxy mediation + separate isolated Browser Authentication Broker

- **Pros:** Keeps all real credentials and authenticated browser sessions outside the bot sandbox; supports phased delivery (CLI adapters first, browser login gated behind `unsupported_capability` until the isolated broker is deployed and verified).
- **Cons:** Requires an explicit adapter catalog, full response mediation in `mitmproxy`, fail-closed dynamic-policy evaluation per request, and a separate broker service/container for browser authentication.

## Decision

Adopt **Option B** and reject **Option A**.

### 1. Shared durable request lifecycle (`secret_requests`)

- Bots invoke a dedicated `request_secret` tool (exposed via `MCPHandler` for CLIs and as a built-in engine tool for API mode).
- Creating a request atomically inserts a `secret_requests` row, posts a `secret_request` card message in the conversation (without any plaintext or secret preview), and transitions the originating turn/tool call into `waiting_secret` (`secret_required`).
- Waiting for human input does not expire as a stale running turn (`ExpireStaleBotTurns`) and does not hold workspace spend locks or bot queue locks (`backend/internal/engine/worker.go`).
- Fulfillment uses an exclusive durable claim (`fulfilling`) keyed by an `operation_id` so concurrent `fulfill`, `deny`, `expiry`, and `cancel` operations have exactly one winner and no DB transaction stays open across vault/broker/proxy I/O.
- RBAC (`backend/internal/workspaces/rbac.go`): `owner`, `admin`, and eligible conversation `member` users may fulfill (`ephemeral` or `bot_secret`) or deny a `secret_request`. Persisting a `workspace_secret` is restricted to `owner` and `admin` (`403` for `member`). `viewer` receives `403`; non-members and cross-workspace IDs receive `404`. Fulfilling a secret request never expands the host allowlist (`new_host` approval stays separate).

### 2. CLI HTTPS credential adapters (`cli_egress`)

- `request_secret` supports `env_placeholder_name` (validated against strict env-var identifier rules and a reserved-variable blocklist). The sandbox only ever receives a non-secret dummy value (`botbox-placeholder-<id>`) in the resumed tool output.
- Only **explicitly reviewed HTTP credential adapters** are permitted (starting with `github_api` for `api.github.com:443`, `gitlab_api` / `gitlab_git` for `gitlab.com:443`, `npm_registry` for `registry.npmjs.org:443`, and `pypi_upload` / `pypi_simple` for `upload.pypi.org:443` / `pypi.org:443`). Arbitrary header templates and arbitrary allowlisted hosts are rejected.
- `mitmproxy` enforces:
  1. **Fail-closed dynamic policy (`resolveRuleForPath`):** When `DynamicPolicy` is configured, never fall back to the `CONNECT`-time `SecretRule` on error, empty rule set, or removed rule. Evaluate expiry and revocation **on every HTTP request**, including inside an already-established `CONNECT`/TLS tunnel. Terminate or refuse passthrough reuse when a new secret rule is added for an active host.
  2. **Per-adapter destination & redirect checks:** Only canonical scheme/host/port, allowed HTTP methods, and allowed path prefixes receive the header; unapproved redirects never receive forwarded credentials.
  3. **All-status response mediation:** Apply the adapter's output policy to `2xx`, `3xx`, `4xx`, and `5xx` responses (headers and bodies), handling `gzip`/`deflate` decoding and streaming/chunk boundaries up to bounded sizes, scrubbing raw and supported encoded secret forms (`base64`, `url-encoded`, auth header), and failing closed on unsupported compressed/binary/malformed responses.
- **Trust boundary note:** Supported upstream applications are trusted credential recipients. Arbitrary malicious upstreams that deliberately transform or cipher an injected token are outside the supported adapter set and are never enabled.

### 3. Ephemeral leases, expiry, and revocation

- Ephemeral grants persist a mandatory bounded `expires_at` (`<= 15m` default or turn end), `revoked_at`, and `turn_id`/`bot_id` binding.
- Ephemeral grants are revoked immediately on turn completion, cancellation, explicit revocation, bot deletion, or expiry (whichever occurs first). While an ephemeral grant is active and per-bot proxy context separation is shared, competing turns for that bot wait in queue until the grant finishes and is revoked.

### 4. Isolated Browser Authentication Broker (`browser_login`)

`browser_login` operates exclusively through the external `cmd/auth-broker` service (`BOTBOX_AUTH_BROKER_URL`, `backend/internal/authbroker`) and remains fail-closed (`unsupported_capability`) whenever unconfigured:

1. **Deployment & isolation:** `cmd/auth-broker` (`images/auth-broker/Dockerfile`) runs outside sandbox containers and networks. Only the control-plane API authenticates over `X-Botbox-Broker-Token` (`crypto/sha256` + constant-time comparison). Chromium DevTools (`127.0.0.1:9223`) and user-data directories live solely inside the broker host/container.
2. **Reviewed login adapters:** Only explicitly reviewed login adapters (`odoo_staging`, `gitlab_web`, `github_web` with fixed canonical HTTPS origin, login/form-action paths, permitted redirect origins, output redaction, and bounded `DefaultBrokerSessionTTL = 30m`) are supported. Generic arbitrary-site login is disabled.
3. **Atomic binding & restricted bot browsing:** Each `secret_request` (`broker_sessions` table, migration `00013_browser_broker.sql`) binds atomically under per-session lock to `(TargetID, FrameID, DocumentID, Origin, FormAction)`; any tab switch, frame swap, document replacement, origin redirect, or target closure aborts with `ErrBindingMismatch` and revokes the session. Subsequent bot browsing uses only `broker_open`, `broker_read`, `broker_click`, and `broker_screenshot` with output sanitization (`SanitizeOutput`), selector blocking, and capture suppression during sensitive/MFA states.
4. **Human-only broker takeover & lifecycle:** MFA/CAPTCHA flows pause and route to `/api/workspaces/{workspaceID}/secret-requests/{requestID}/broker-takeover/{start,frame,input,complete}` gated by user-pinned `takeover_user_id` and scoped HMAC tickets (`broker:<sessionID>`). All bot broker calls return `ErrTakeoverActive` until takeover completes; bot deletion, turn cancellation, denial, and TTL expiry revoke broker sessions cleanly.

## Consequences

- **Positive:** CLI secret requests ship immediately with strong fail-closed proxy enforcement, response mediation, and deterministic ephemeral lease revocation without risking sandbox Chromium exposure.
- **Positive:** `browser_login` cannot leak credentials or session cookies into the sandbox because the API rejects it at capability check until the isolated broker satisfies the ADR invariants.
- **Trade-off:** `mitmproxy` response mediation buffers/inspects bounded response streams for credentialed adapter requests instead of raw zero-copy `io.Copy`, and each new CLI or web login integration requires an explicit reviewed adapter definition.
