# bot.Box: Software Requirements & Architecture Specification

> Status: living document. It describes the target architecture. The implementation is planned in phases, which are tracked as GitHub issues (see [§6](#6-roadmap)). Decisions taken during implementation are recorded as ADRs in `docs/adr/` and reflected here.

## 1. Overview & vision

bot.Box is a multi-user web application for creating and running **bot computer agents**. Each bot combines a large language model (LLM) with its own isolated sandbox computer. The computer has a desktop that users can watch and take over in the browser, and the bot has full root access inside it.

Bots behave like **colleagues**. Users talk to them in direct messages and put them together in group chats, where bots request each other's input while humans stay in control through turn limits, approvals, and budgets.

### Goals
- Create, configure and run bots, each with a sandboxed Linux desktop, browser and agent CLIs.
- Drive bots through an **agent CLI inside the sandbox** using Claude Code for Anthropic, Codex for OpenAI, agy for Google Antigravity, or Pi for OpenRouter.
- Let bots run commands **and** operate the desktop (computer use).
- Multiple users per workspace, with roles.
- A Slack-style team chat with direct messages and bot-to-bot group chats.
- Run sandboxes on a single Docker host first and on Kubernetes later.

### Security principles
1. **Credentials never enter a sandbox.** They are not in its environment, its files, the prompt or the logs. An egress proxy adds them to matching outbound requests.
2. **The sandbox is the security boundary.** A bot is root inside it and nothing outside it: no privileged containers, no Docker socket, no direct internet route.
3. **Least privilege for tokens.** Use repository tokens without rights to protected branches, and allowlists for outbound hosts.
4. **Workspaces are isolated.** Every query and every real-time channel is scoped by workspace.
5. **Humans stay in charge.** Bots can always be paused or taken over, and risky actions require approval.

### Non-goals (for now)
SSO (the auth design is ready for it), threads and reactions in chat, integrations with external chat tools, a custom Kubernetes operator.

---

## 2. Tech stack

| Area | Choice | Why |
|---|---|---|
| Backend | Go: `chi`, `pgx` + `sqlc`, `goose`, `slog`, `coder/websocket`, gRPC (`buf`), `oapi-codegen` | Performance, simple concurrency for streams and WebSockets, first-class Docker and Kubernetes clients, type-safe SQL without an ORM |
| Frontend | Next.js (App Router, TypeScript), Tailwind, `next-intl`, TanStack Query, noVNC, xterm.js, `lucide-react` | Server rendering, typed API client from OpenAPI, i18n from day one |
| Database | PostgreSQL | Relational data with JSONB where needed; partitioning for logs |
| Messaging | NATS with JetStream | Event fan-out across API instances and durable per-bot work queues |
| Sandboxing | Docker Engine API, Kubernetes `client-go`, gVisor as the target runtime | One compute abstraction with two drivers |
| Dev environment | devenv (Nix) for **all** dependencies; Caddy as a same-origin proxy | Reproducible setup; no globally installed tools |
| Language | English for code, comments, docs, commits and UI | One language for the whole project; the UI is translatable |

---

## 3. Architecture overview

```
+--------------------------------------------------------------------------+
|                          Frontend (Next.js)                              |
|  Fleet dashboard · Bot builder · Bot workspace (noVNC, terminal, files)  |
|  Team chat (DMs, groups, approvals) · Secrets & egress · Settings        |
+-----------------------------------+--------------------------------------+
                                    | REST / WebSocket (same origin via Caddy)
+-----------------------------------v--------------------------------------+
|                              Backend API (Go)                            |
|  Auth & workspaces · Bot engine (CLI runners)       · Orchestrator       |
|  Compute driver manager · Secrets · Budgets · Chat · VNC/PTY relays      |
+-----------+------------------------+------------------------+------------+
            |                        |                        |
     +------v------+          +------v------+       +---------v----------+
     | PostgreSQL  |          | NATS / JS   |       | Compute driver     |
     +-------------+          +-------------+       | Docker | Kubernetes|
                                                    +---------+----------+
                                                              |
                       per bot  +-----------------------------v-----------+
                                |  Sandbox ("computer")                   |
                                |   XFCE + VNC + Chromium + CLIs          |
                                |   agent (gRPC: exec, pty, files,        |
                                |          desktop; MCP server)           |
                                +-------------------+---------------------+
                                                    | internal network only
                                +-------------------v---------------------+
                                |  Egress proxy (per bot)                 |
                                |   allowlist · secret injection ·        |
                                |   LLM metering · egress log             |
                                +-------------------+---------------------+
                                                    |
                                                Internet
```

### Key flows
- **Chat message → bot:**
  1. The API stores the message and publishes it.
  2. The orchestrator picks the responding bots.
  3. Each bot's work queue starts one turn at a time.
  4. The runner streams events, which become chat messages, status changes and usage records.
- **Secret injection:**
  1. The bot runs `git push https://gitlab.com/…` without a token.
  2. The request goes to the egress proxy, which matches `gitlab.com`.
  3. The proxy adds the header from the encrypted secret and forwards the request.
  4. The request is logged as "token added".

---

## 4. Module specifications

### 4.1 Authentication, users & workspaces

The authentication is modular, so other identity sources can be added without re-engineering.

```go
type AuthProvider interface {
    Authenticate(ctx context.Context, credentials Credentials) (*User, error)
    ValidateToken(ctx context.Context, token string) (*Claims, error)
}
```

- **Local provider (first):**
  - **First-run setup wizard:** when the database has zero users, the server generates a cryptographically random single-use setup secret on startup (or reads `BOTBOX_SETUP_SECRET` when explicitly configured), stores only its SHA-256 digest, and prints the secret once to server stdout. The browser redirects to `/setup` where an operator enters that secret (constant-time comparison, rate-limited per IP) to create the initial `owner` user and first workspace (`POST /api/setup/complete`). Once complete, the setup digest is cleared and `/api/setup/*` permanently returns `404`.
  - Passwords are hashed with Argon2id, and the hash is upgraded on login when the parameters change.
  - JWT access token (EdDSA, 15 min) plus an opaque refresh token. Refresh tokens are stored hashed and rotated on use. Rotation, reuse detection and logout serialize on the session family. Reuse or logout revokes the whole family. Accepting an invitation as an existing user preserves the submitted access token and its expiry.
  - Cookies are `HttpOnly`, `Secure` and `SameSite=Lax`. Mutating requests are protected against CSRF.
  - Login and refresh are rate-limited, and failed logins go to the audit log.
- **Later:** OpenID Connect / OAuth2, LDAP / Active Directory, SAML 2.0.
- **Workspaces:** a user can belong to several workspaces. Bots, chats, secrets and budgets belong to a workspace. Owners can update workspace profile (`name`, `defaultDriver`, `defaultLocale`, `screenshotRetentionDays` 1–30 via `PATCH /api/workspaces/{workspaceID}`), transfer ownership by promoting another member to `owner`, or permanently delete an idle workspace (`DELETE /api/workspaces/{workspaceID}` when the user belongs to at least two workspaces and no bot turn, secret request, lease, or broker session is active; active work returns `409 workspace.busy` and deleting the last workspace returns `400 workspace.last_workspace`). Workspace settings in the UI are organized into `/settings/general`, `/settings/members`, `/settings/budgets`, `/settings/secrets`, `/settings/integrations`, and `/settings/guardrails` under a shared grouped sub-navigation.
- **Roles:** `owner`, `admin`, `member`, `viewer`.

  | Action | owner | admin | member | viewer |
  |---|:-:|:-:|:-:|:-:|
  | Manage members, roles, drivers, budgets | ✓ | ✓ | | |
  | Create bots and groups; chat | ✓ | ✓ | ✓ | |
  | Manage a bot's secrets | ✓ | ✓ | bot owner | |
  | View bots, desktops, group chats | ✓ | ✓ | ✓ | ✓ |

- **Invitations:** a link with a token and an expiry, sent by e-mail through SMTP if configured. Accepting adds the user with the invited role.
- **Tenant isolation:** every query is scoped by `workspace_id`. Accessing another workspace's resources returns 404.
- **Membership revocation:** role changes and removals serialize per workspace and recheck the actor's role while holding the lock. Removal revokes group access and preserves authorship of existing group messages. See [ADR-0037](docs/adr/0037-session-and-infrastructure-authorization.md).

### 4.2 Bot engine & LLM integration

Each bot runs an agent CLI inside its computer. The sandbox agent starts the CLI and streams its output to the API over gRPC. Bot settings select a CLI and its model. The API uses `llmMode: cli` and derives the CLI from `llmProvider`. See [ADR-0034](docs/adr/0034-pi-openrouter-cli.md).

Bot settings also set the maximum duration of each turn in whole minutes, from 1 to 120. The default is 10 minutes. The worker uses the current setting when it claims a turn and passes the deadline to the sandbox agent. Time spent queued or waiting for human input does not count toward an execution. A turn that reaches its deadline is interrupted. See [ADR-0036](docs/adr/0036-per-bot-turn-timeouts.md).

```go
type AgentRunner interface {
    Start(ctx context.Context, session Session) error
    Send(ctx context.Context, msg Message) error
    Events() <-chan Event // text_delta, message, tool_call, tool_result, status, usage, error, approval_needed
    Interrupt() error
    Stop() error
}
```

| CLI | Headless invocation | Sessions | Credentials |
|---|---|---|---|
| `claude` | `claude -p --input-format stream-json --output-format stream-json` | Resume per conversation | Placeholder `ANTHROPIC_API_KEY`; `ANTHROPIC_BASE_URL` points at the egress proxy |
| `codex` | `codex exec --json` | Resume | Placeholder key; the provider base URL points at the egress proxy |
| `agy` | `agy --input-format stream-json --output-format stream-json --conversation <id> --print-timeout <d>`. Events: `init`, `step_update` (`text_delta`, `tool_info`, `subagent_info`), `result` | `--conversation <id>` | **Open:** the CLI documents only cached credentials from an interactive login. A spike decides how this fits principle 1 (§7). |
| `pi` | `pi --mode json --provider openrouter --model openrouter/<model-id>` with the prompt on stdin | `--session-id <id>` per conversation | Placeholder API key; OpenRouter requests use the metered bot egress route |

- **Permissions:** permission prompts inside the CLI are disabled, because the sandbox is the boundary. For `agy`, scoped `permissions.allow` rules are possible. `agy` soft-denies tools (exit code 0, notice on stderr), so the adapter must surface these notices.
- **One event model** for all runners. Every adapter is tested against recorded stream fixtures.
- **Per-bot work queue** (NATS JetStream). A bot has one computer, so it runs **one turn at a time** across groups and direct messages. Different bots execute concurrently. Each API instance has a bounded dispatcher (`BOTBOX_TURN_CONCURRENCY`, default 4, range 1..64); duplicate deliveries cannot replay a running turn. Conversation locks precede bot locks, and the group turn cap is rechecked atomically when claiming queued work. See [ADR-0025](docs/adr/0025-parallel-bot-turns-and-activity.md).
- **Sessions:** one CLI session per (bot, conversation), so every conversation keeps its own context.
- **Status:** persisted running turns and tool activity determine each bot's current status. Private live events carry bot, turn, and execution ids plus a sequence within that execution. Provider step completion cannot mark an ongoing bot turn idle. The Team bot list refreshes on live activity and every 3 seconds, including work in other conversations. Reconnecting reloads snapshots rather than applying stale event status. Group chat shows one indicator per active bot, including thinking before the first tool call, queued work, and secret waits.
- **Reply language** is a per-bot setting. System prompts are in English.
- **Bot knowledge base ([ADR-0033](docs/adr/0033-bot-knowledge-base.md)):** Each bot owns a workspace- and bot-scoped knowledge base (`bot_memories`, max 200 entries) supporting `always` (injected on every turn up to a 4,000-token budget) and `on_demand` (queried via PostgreSQL full-text and tag search) entries. CLI runners expose `memory_save`, `memory_search`, `memory_update`, and `memory_delete` through `/memory-mcp` on the bot egress proxy. Writes enforce a zero-secret memory guard rejecting credential patterns and decrypted bot/workspace/MCP secrets, respect `bots.memory_auto_save`, and record `kind = 'memory'` inline chat cards with `Edit`/`Undo` for bot managers alongside Bot Settings (`/bots/:id/settings/knowledge`).
- **Responsibility for the bot computer:** every CLI turn, including
  resumed sessions and continuations, reminds the bot to maintain its own
  persistent sandbox. Bots decide routine local housekeeping themselves,
  including removing their own obsolete helper scripts and stale working copies
  after checking references and ongoing work. Uncertainty favors recoverable
  cleanup; unresolved risk to valuable work warrants a question. User-provided
  files, shared data and uncommitted work require authorization before deletion.
  External actions still follow the user's authorization, and secret handling,
  workspace isolation, network rules, budgets and human control remain enforced.
  See [ADR-0024](docs/adr/0024-sandbox-housekeeping-responsibility.md).

Claude CLI bots can use a Pro/Max subscription through an encrypted
`llm-anthropic-oauth` secret, prepared on the bot Secrets page. The user completes
`claude setup-token` outside the bot (or uses `make claude-login`), then stores the
token through the write-only form. The sandbox receives only an OAuth placeholder
and no API key; the proxy injects the actual token. Subscription records keep
tokens with `billing_kind=subscription`.
The optional workspace policy uses model prices to add an internal budget
charge while preserving the separate provider cost. See
[ADR-0011](docs/adr/0011-claude-subscription-outside-sandbox.md).

Typing `claude` in the bot computer uses that same placeholder and metered
route. The pinned CLI is 2.1.323. Its first-run connectivity check and
workspace trust prompt are skipped there, because provider CONNECT tunnels stay
closed and the desktop cannot answer the prompt. The `sonnet` alias is sent to
the provider as `claude-sonnet-5-5`; a monetary budget needs a price for that
name. Desktop calls are metered without a chat turn. Pause still blocks them. Holding the desktop stops engine
turns and does not block that personal CLI. See
[ADR-0012](docs/adr/0012-interactive-claude-in-the-sandbox.md).

Interactive Codex uses a ChatGPT plan connection for the bot computer. A bot
manager completes workstation sign-in with `make codex-login`, then takes
desktop control and types `codex` in the bot computer. The CLI uses a placeholder
and the per-bot `/llm/openai-plan/v1/responses` route. The control plane stores and
renews the OAuth grant. The proxy checks the connection owner, current desktop
holder, pause, explicit host blocks, and budget. Usage is attributed to the bot
and workspace outside chat. See [ADR-0030](docs/adr/0030-codex-desktop-chatgpt-plan.md)
and the [setup guide](docs/guides/codex-desktop-setup.md).

The executable defaults to Docker; simulation requires the explicit development
setting `BOTBOX_COMPUTE_DRIVER=memory`. Unrecognized driver names fail startup.
Existing simulated computers require a restart before a CLI turn can run. Chat
uses allowlisted failure categories with links to computer controls or Secrets,
and CLI preflight checks provider credentials before launching a process. Private
dependency errors are never displayed. See
[ADR-0010](docs/adr/0010-explicit-simulation-and-turn-recovery.md).

Claude stream decoding selects the event type before reading its payload, so
auxiliary notices with a text `message` cannot interrupt assistant or tool output.
Malformed assistant/user messages and oversized CLI records still fail the turn
with `engine.stream_format_invalid`, shown as a response compatibility problem
rather than a provider, credential or budget failure. Raw payloads remain private.
See [ADR-0004](docs/adr/0004-cli-stream-formats.md).

Phase 4 implementation: private DM APIs persist conversation-local history and
turns. JetStream notifications drive the API worker. Re-enqueueing a turn after
secret fulfillment always publishes a new notification, even within JetStream's
duplicate window; persisted worker claims prevent duplicate execution.
Authorized conversation
WebSockets stream normalized events. `/team` is the group chat and still opens
the same DM panel for a colleague. A chat message carries the sender's user or
bot id so a group transcript can name the speaker. A group approval message is a card, and `/team/approvals` is the inbox for the same decisions. Workspace and bot settings
show spend and budget limits. The workspace header and bot settings provide a
persisted bot-wide Pause/Resume control. Pausing cancels the active
turn, keeps queued messages in order, and survives API restarts. Taking desktop
control also blocks new turns until release. Interrupted turns are never replayed;
resuming only permits queued work. Bot managers can change pause state; viewers
can read it. The runner lifecycle owns a bounded event stream and propagates
cancellation to CLI process groups and API requests. In a private DM, **Stop
response** cancels the identified running turn for the caller's conversation.
Later messages remain queued and may run after cleanup.
See [ADR-0028](docs/adr/0028-stop-private-chat-turns.md). The proxy resolves
`llm-openrouter`, `llm-openai`, and `llm-anthropic` from encrypted bot/workspace
secrets outside the sandbox. For Pi, an owner or admin can create the
`llm-openrouter` workspace secret for `openrouter.ai` with Bearer authentication
and path prefix `/api/v1`. Pi uses the OpenRouter model identifier as published
in the catalog, for example `openrouter/auto`. Pi uses OpenRouter Chat
Completions or Messages according to the model. Both routes are metered.

Budget accounting uses UTC periods and integer microdollars. Execution is
serialized per workspace to protect shared spend. Model prices can be listed via
`GET /api/workspaces/{workspaceID}/model-prices` and edited via
`PUT /api/workspaces/{workspaceID}/model-prices`; budgeted model requests require a
configured price. At exhaustion the next API request is denied and a deduplicated
budget approval is created. Raising a budget above current spend resolves its
pending approval with audit logging.

CLI turns use the authenticated streaming agent RPC and persist vendor resume IDs
per bot/conversation. agy stderr soft-denial notices are persisted as system
messages so they remain visible after reconnecting. Complete CLI messages and
text deltas both update the live chat, without duplicating the final response.
Tool rows stay in the conversation as the CLI wrote them. The chat shows a short
action sentence instead of the tool name and JSON. A screenshot Claude saved under
that bot's `tool-results` directory is loaded only for the conversation owner,
and only when the path is one image file in that directory. The browser asks for
it by message id. The sandbox path is not an image URL.
Claude and Codex skip their additional CLI permission checks only inside the
isolated sandbox; processes are terminated as a group on cancellation.
The sandbox installs a provided public proxy CA into its user-owned combined
trust bundle and NSS store; supervised tools inherit the CA environment without
sudo. The Docker driver provisions one trusted proxy per bot, using the database
URL derived from `DATABASE_URL` or the `BOTBOX_EGRESS_DATABASE_URL` override. API and CLI model requests share a workspace-scoped admission lock. For supported
text-only requests with an enforced output-token limit, admission reserves a
conservative cost bound in `usage_records.reserved_micros` and releases the lock
before forwarding. Workspace, bot, and group caps include pending reservations;
spend displays and warnings use settled charges only. Settlement releases unused
capacity, including after cancellation. Missing, invalid, or over-bound billing
leaves unknown cost and blocks applicable monetary budgets. Requests with opaque
inputs, hosted tools, or no enforceable output limit retain serialization for
that provider request when monetary caps apply. Subscription requests and requests
without monetary caps release the lock before forwarding. No lock spans a whole
bot turn.
Native provider CONNECT tunnels cannot bypass this route. Provider-reported cost
wins over configured prices; CLI summaries do not double charge proxy records.
Missing usage/prices on metered requests block applicable monetary budgets until reconciliation.
New metered sandboxes use generated agent control tokens, recovered through
trusted Docker inspection after an API restart. agy OAuth exchange and automated
crash/billing reconciliation remain integration requirements. No live-provider
acceptance run is implied by fixture or Docker tests. See
[ADR-0007](docs/adr/0007-live-cli-streams-and-proxy-admission.md),
[ADR-0008](docs/adr/0008-private-dms-durable-turns-and-budgets.md) and
[ADR-0009](docs/adr/0009-metered-sandbox-proxy.md).

### 4.3 Bot sandbox ("computer")

Each bot gets its own isolated Debian-based container:

- **Desktop:** XFCE with a VNC server on `:0` at a fixed 1280×800 resolution, and websockify on `127.0.0.1:6080` inside the sandbox. The browser connects through an authenticated relay in the API (short-lived ticket). The relay reaches websockify through the Docker socket, because the API process cannot route the container bridge address. Caddy and Next forward that socket, so the page origin and the API host differ; the relay allows the site origin after the ticket check. The machine can report running before websockify accepts; the viewer retries that ticket and renews it before the 60 second lifetime ends, so starting a machine does not need a reload.
- **Software:** Chromium, git, build tools, Python, Node.js, and `claude`, `codex` and `agy` at pinned versions.
- **Rights:**
  - User `bot` with passwordless `sudo` **inside** the container, so it can install with `apt-get`, `pip` and `npm`.
  - No privileged mode and no `NET_ADMIN` / `SYS_ADMIN`; a seccomp profile; gVisor where available.
- **Agent:** a small Go service started by a process supervisor. It offers gRPC for exec (streaming), PTY sessions, files under `/home/bot`, health, and the desktop tools (§4.7), which it also exposes as a local MCP server for the CLIs. The API dials a published loopback port when Docker allocated one. Bot networks are internal, so that publish usually does not exist, and the API then tunnels the RPC through the Docker socket to `127.0.0.1:9090` inside the sandbox ([ADR-0013](docs/adr/0013-localhost-agent-port.md)).
- **Persistence:** a volume per bot for `/home/bot`. It survives stop, start and image upgrades.
- **Control lock:** exactly one controller per desktop, the bot or one human.
  - "Take control" pauses the bot; "View only" releases the lock.
  - The relay allows only view-related RFB messages from viewers. Each connection ends at ticket expiry. The relay rechecks workspace membership and control ownership during the session, including before forwarding controller input. The desktop requests a fresh ticket when an established stream ends.
  - The UI shows the presence of everyone watching.
- **Network:** no direct route to the internet. The only way out is the bot's egress proxy (§4.5).

### 4.4 Compute driver abstraction

```go
type ComputeDriver interface {
    CreateEnvironment(ctx context.Context, cfg BotConfig) (EnvID, error)
    StartEnvironment(ctx context.Context, id EnvID) error
    StopEnvironment(ctx context.Context, id EnvID) error
    DestroyEnvironment(ctx context.Context, id EnvID) error
    Status(ctx context.Context, id EnvID) (EnvStatus, error)
    Stats(ctx context.Context, id EnvID) (EnvStats, error)
    GetVNCStreamURL(ctx context.Context, id EnvID) (string, error)
    ExecCommand(ctx context.Context, id EnvID, cmd []string) (ExecResult, error)
    Reconcile(ctx context.Context) error
}
```

- **Docker driver (first):**
  - Uses the Docker Engine API. Only the API process talks to the Docker socket, which is never mounted into a sandbox.
  - Per bot: an `internal` network shared only by the sandbox and its egress proxy (the proxy is also on an egress network), resource limits, a named volume and ownership labels.
  - A reconcile loop brings the database and the actual containers back in sync after a restart.
- **Kubernetes driver (later):**
  - A namespace per workspace (or per bot), a pod with the sandbox, a PVC for the home volume, `ResourceQuota` / `LimitRange`, a default-deny egress `NetworkPolicy`, a gVisor `RuntimeClass`, and scale-to-zero for stopped bots.
  - NetworkPolicies cannot tell containers in one pod apart, so the egress topology is decided in an ADR: either an init container with iptables (the proxy UID is excluded) or a separate proxy pod per bot.
- Several clusters can be registered per workspace. The dashboard shows driver health.

### 4.5 Secrets management & egress security proxy

The **Secrets & egress** section of bot settings opens with a short overview of credentials and network access.

**Principle:** credentials such as Git tokens and API keys **never** enter the LLM prompt, the bot's environment or its files.

**How the proxy works (one per bot):**
1. All bot traffic must pass the proxy. On Docker, the sandbox is only attached to an internal network; on Kubernetes, see §4.4. The sandbox gets `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY`.
2. **LLM base-URL mode:** the CLIs call plain-HTTP proxy endpoints (for example `/llm/anthropic/…`) with placeholder keys. The proxy forwards over TLS and swaps in the real key. No TLS interception is needed.
3. **Forward mode:** `CONNECT` tunnels. The proxy checks the requested host against its policy. Hosts without a secret are tunnelled without TLS interception.
   The proxy validates DNS results and dials only public IP addresses for both passthrough and credentialed HTTPS. The bot-facing listener binds only to the bot's verified internal Docker interface and admits connections from that network.
4. **Selective TLS interception** only for hosts with a configured secret:
   - Each bot has its own CA, with the key stored encrypted.
   - Leaf certificates are issued on the fly.
   - The CA is installed in the sandbox trust stores: system, NSS for Chromium, Node, Python, git.
5. **Header templates per secret:** `bearer`, `basic` (`user:{secret}`, needed for git over HTTPS) or custom, with an optional path prefix. Placeholder headers sent by the bot are stripped.
6. **Allowlist & explicit blocklist policy ([ADR-0017](docs/adr/0017-egress-blocklist-triage-and-enforcement.md)):** `allowlist` (default) or `open` internet, paired with an explicit `blocked_hosts` list (`<= 200` entries per list). Explicit blocks (exact hosts or `*.domain` subdomain-only wildcards) are evaluated **first** and override `open` mode, `allowed_hosts`, and secret-derived rules without exposing or altering secret values. Identical normalized allow/block patterns are rejected (`400`), while narrower deny exceptions under broader allows (e.g. `*.example.com` allowed with `api.example.com` blocked) are valid. `PATCH /api/workspaces/{workspaceID}/bots/{botID}/egress` requires `expectedRevision` (`428` missing, `409` stale); omitting `blockedHosts` preserves stored blocks while `[]` clears them. Policy mutations serialize under a per-bot transaction lock, persist an audit row and durable invalidation intent atomically, and return `200` (`applied`) once the running proxy adopts the revision and closes affected passthrough tunnels (or `202` `pending` when ack times out, and `200` `not_running` when stopped). Running proxies enforce a `1 s` fail-closed policy freshness window for active passthrough tunnels and reject subsequent requests on open MITM connections when blocked. On the bot **Secrets & network** page, bot managers can triage domains in one click from the egress log (`Allow`/`Block`) with pattern-specific `Undo` that preserves unrelated concurrent rules and unsaved form drafts. A blocked host gets `403` and an approval request ("allow new host"); removing an exact block via approval requires bot-management rights (`403` otherwise), and wildcard blocks never narrow or clear through approval (`409`).
7. **Metering:** the proxy reads `usage` from LLM responses, including streaming ones. A **kill switch** blocks LLM calls when a budget is exhausted (§4.9).
8. **Egress log:** time, method, host, path without query, result (`token added`, `passed`, `blocked`), bytes and latency. Stored partitioned by day with a retention period.
9. **Credentialed response sizes ([ADR-0023](docs/adr/0023-git-response-size.md)):** The reviewed `gitlab_git` adapter accepts responses up to 64 MiB on GitLab.com and assigned self-hosted origins; other adapters retain a 2 MiB cap. Both wire and decoded gzip/deflate bodies are bounded and fully inspected before forwarding. Oversized responses return a safe `502` identifying the byte limit; body read failures return a separate generic error. Packfile MIME-type checks and credential-reflection rejection remain enforced.

**Result:** the bot reaches external repositories and APIs without knowing any secret. Exfiltration through the LLM fails, because there is nothing to exfiltrate and unknown hosts are blocked.

**Storage:**
- Envelope encryption: AES-256-GCM with a data key per secret, wrapped by a key-encryption key from a file or environment variable (Vault or KMS later). KEK rotation is supported.
- The API is **write-only**: secrets can be created, replaced and deleted, but never read back.
- Bot secrets and workspace secrets (inherited by bots), plus bounded ephemeral `secret_leases` created by in-chat `request_secret` fulfillment.
- **In-chat secret requests ([ADR-0016](docs/adr/0016-zero-context-secret-requests.md)):** Bots invoke `request_secret` (`cli_egress` with a reviewed HTTPS credential adapter and optional validated `env_placeholder_name`, or `browser_login` when the external `auth-broker` is configured via `BOTBOX_AUTH_BROKER_URL`). For GitLab CLI access, an eligible saved secret for the bot, exact origin and adapter is tried using a non-secret placeholder before requesting human input ([ADR-0020](docs/adr/0020-self-hosted-gitlab-cli-grants.md)). When human input is needed, the turn pauses as `waiting_secret` and eligible workspace owners, admins and conversation members submit via the write-only modal (`POST .../secret-requests/{id}/fulfill`). Group secret dialogs identify the bot that sent the request. The `mitmproxy` evaluates lease expiry, revocation, adapter routes, fail-closed dynamic policy reload, and all-status response mediation per request on every `CONNECT`/TLS tunnel. For `browser_login`, the standalone `cmd/auth-broker` service owns credential-bearing Chromium targets/cookies outside sandbox containers with atomic `(target, frame, document, origin, form)` binding, restricted bot browsing (`broker_open`, `broker_read`, `broker_click`, `broker_screenshot`), and dedicated human-only broker takeover (`POST .../broker-takeover/*`). When unconfigured (`AuthBroker == nil`), `browser_login` fails closed with `422 unsupported_capability`.
  When fulfillment fails after the request has been claimed, the request and waiting turn become failed. A separate pending request keeps the group paused. A browser session whose submission could have taken effect is revoked on failure.
- **Self-hosted GitLab access ([ADR-0019](docs/adr/0019-live-public-gitlab-and-bot-assignment.md), [ADR-0020](docs/adr/0020-self-hosted-gitlab-cli-grants.md)):** Workspace owners/admins register up to 20 named HTTPS origins through `GET/POST/DELETE .../gitlab-instances`. Bot managers select origins through `GET/PUT .../bots/{botID}/gitlab-instances`; new registrations grant no implicit access. Reviewed `gitlab_web`, `gitlab_api` and `gitlab_git` adapters use the exact assigned origin and independent egress permission. Real API/Git tokens remain envelope-encrypted outside sandboxes; `glab` receives only a generated `GITLAB_TOKEN` placeholder and host guidance, while the proxy injects PRIVATE-TOKEN on REST `/api/v4/` or Basic on smart-HTTP Git routes. Public DNS checks and literal-IP dialing apply to each custom upstream connection; all-status response mediation and live assignment checks remain enforced. Removing access revokes sessions/leases and saved bot credentials, cancels requests, and records per-bot exclusions of existing inherited workspace tokens; reassignment does not resurrect them. Removing an instance removes its saved adapter tokens globally within the workspace. Retained `gitlab_known_origins` metadata prevents legacy generic/wildcard secrets or secret-derived host allowances from bypassing registration, assignment and explicit network policy after removal. Expected failures produce safe tool results and actionable localized turn codes. Default GitLab.com behavior remains. GraphQL, SSH, private targets, custom upstream CAs, alternate ports, subpaths and cross-origin SSO are deferred.
- **MCP Proxy & per-bot command access control ([ADR-0029](docs/adr/0029-mcp-proxy-and-bot-tool-policies.md)):** Workspace owners/admins register remote HTTPS MCP servers (`GET/POST /api/workspaces/{workspaceID}/mcp-servers`, `PATCH/DELETE .../mcp-servers/{serverID}`, `POST .../sync`) with optional write-only envelope-encrypted credentials (`none`, `bearer`, `header`, `basic`) and `netguard` public-DNS/literal-IP validation. New servers grant no implicit bot access. Bot managers explicitly assign servers (`GET .../bots/{botID}/mcp`, `PUT .../bots/{botID}/mcp/{serverID}` with `expectedRevision`) and set three-state per-command policies (`allow`, `ask`, `deny`; new tools fail closed as `deny`). The egress proxy (`POST /mcp`, `POST /mcp/{slug}`) enforces `tools/list` filtering, `tools/call` execution with `2 MiB` caps and credential-reflection scrubbing, `kind = 'mcp_tool'` chat approval cards for `ask` commands, and `mcp_call_log` audit records while keeping upstream credentials strictly outside sandboxes.
- **LLM proxy guardrails ([ADR-0035](docs/adr/0035-llm-proxy-guardrails.md)):** Each workspace has an opt-in guardrail policy (`enabled = false` by default, configurable via `/api/workspaces/{workspaceID}/guardrails{,/policy,/rules,/rules/{ruleID},/test,/events}` and `/settings/guardrails`) enforced in `llmproxy` for all workspace bots (`anthropic`, `openai`, `openai-plan`, `openrouter`, `google`). Policies configure `failure_mode` (`fail_closed` default or `fail_open`), three built-in detectors (Secrets `both/block` combining static patterns and stored proxy ring values while skipping `BOTBOX_SECRET_*` placeholders; PII `both/redact` over configurable `email`, `iban`, `card`, `phone`; Prompt-Injection `input/audit`), and up to 100 custom RE2 regex or keyword rules with `block > redact > audit` precedence. Input checks run before `Admit` so input blocks return HTTP `403` without upstream requests or budget settlement; non-streaming JSON responses are scanned before writing headers, and active output `block`/`redact` rules filter SSE text deltas through a `512`-byte sliding lookahead window (passing streams straight through when none apply). NATS `guardrails.invalidate.<workspaceID>` invalidates cached proxy policies across replicas. Interventions record sanitized metadata (`llm_guardrail_events`, inline chat `kind = 'guardrail'` notices, and `kind = 'tool'` `guardrail_event` trace rows) without persisting raw secret or PII values, and blocked active CLI turns map to `engine.guardrail_blocked`.
  The workspace settings keep unsaved policy edits when a custom rule is changed and the rule list reloads. Use **Save policy** to apply the draft.
- Every change goes to the audit log.

### 4.6 Robot avatars

Every bot gets a unique, animated robot avatar, generated deterministically from `avatar_seed`. There are no human faces.

- **Generation:** our own SVG generator (no third-party avatar library).
  - `h = fnv1a32(seed)` selects head (4), sensors (4), antenna (3), mouth (3) and palette (6), which gives 864 robots.
  - Seeds match `[a-z0-9-]`, so the Go and TypeScript implementations agree.
- **Animation:** SVG SMIL, no per-frame JavaScript. The mood follows the bot status: `idle` (slow breathing sensors), `thinking` (visor scan, gear spin, thought bubble), `action` (fast blink, signal rings) and `offline` (dimmed, still).
- `prefers-reduced-motion` renders a still pose.
- Details: [`design/STYLEGUIDE.md` §8](design/STYLEGUIDE.md) and [`design/tokens.json`](design/tokens.json).

### 4.7 Computer use

- **Desktop tools in the agent:** `screenshot`, `cursor_position`, `mouse_move`, `click` (left, right, middle, double), `drag`, `scroll`, `type`, `key`, `wait`. They are implemented with xdotool and X11 capture. Coordinates are scaled when screenshots are downscaled.
- **CLI interface:** the agent runs a local MCP server at `POST http://127.0.0.1:9091/mcp`. Desktop tools, including image-returning screenshots, are registered with the CLIs. Stdio is available as a `--mcp-stdio` fallback.
- **Browser tool (recommended):** Chromium with remote debugging on `127.0.0.1:9222` only, driven via CDP. It is more reliable and cheaper than pixel clicks on web pages.
- **Limits per turn:** screenshots, steps and tokens.
- **Takeover:** a human taking control interrupts the bot within 1 s and pauses the turn. On hand-back the bot receives the exact system note `A human used the desktop; take a fresh screenshot.` and the turn resumes.
- **Transparency:**
  - The bot's labelled cursor is shown in the viewer.
  - An action timeline (`desktop_actions`) lists every desktop action.
  - Screenshots are kept only for a configurable time (default 7 days).

### 4.8 Team chat & bot-to-bot orchestration

Direct messages stay private to one human and one bot. Groups are a second
conversation type on the same tables. A group has no single owner column.
Membership is the only read path, including for workspace admins. See
[ADR-0015](docs/adr/0015-group-chat-orchestration.md).

- **Group settings:** name, goal, lead bot, bot-to-bot on or off, turn limit
  (default 20), answer policy (`mentioned_else_lead`, `lead_first`,
  `round_robin`), "ask me before" rules, and an optional lifetime budget in
  microdollars.
- **Group HTTP:** members list and read a group, mark it read, and (except
  viewers) create it, replace its settings, and post. A human post stores the
  message and queues the bots the orchestrator selects. A failed publish rolls
  the post back. Someone who is not a member, including a workspace admin, gets 404.
- **Delete group:** a non-viewer group member can permanently delete the group
  for everyone through `DELETE /api/workspaces/{workspaceID}/groups/{groupID}`.
  The chat header opens a confirmation dialog with Cancel focused first.
  Messages, memberships, queued turns, sessions, group approvals, and notifications
  are removed together. Bots, computers, and metered usage remain. Running turns,
  secret waits, and active broker sessions or secret leases block deletion with
  `409 group.busy`; finish or stop that work before retrying. The operation records
  a `group.deleted` audit entry. See [ADR-0022](docs/adr/0022-group-deletion.md).
- **Orchestrator** (`internal/orchestrator`), used for groups only:
  - **Router:** human messages use mentioned bots, otherwise the lead; lead first plus mentions; or round robin. A human's case-insensitive `@all` token addresses all group members and queues each member bot once, overriding round robin. Human recipients use the existing mention notification preferences.
  - **Context builder:** the responding bot's identity, the lead, goal, roster with global bot responsibilities and group metadata, rules, up to the last 50 messages and progress updates, and an extractive summary of older text. The group brief is bounded to 48 KiB so the current input, bot identity, and desktop guidance fit the agent's 64 KiB CLI input limit. Newer messages take priority over the old summary; oversized recent replies keep their ending. Very large roster metadata is limited to half the brief. Truncation is UTF-8-safe, identified in the prompt, and never deletes stored history. See [ADR-0026](docs/adr/0026-bounded-group-context-and-visible-turn-failures.md). The summary does not call a model. The prompt describes how to request input explicitly. Secrets are not included.
  - **Global bot roles:** the free-form "Global role & responsibilities" field in bot settings (`bots.description`, up to 4000 bytes) applies across groups and direct messages. It supports Markdown; the team chat's direct-message details render it with the same safe CommonMark/GFM renderer as messages. Long roles show a preview about four body lines high, with "More" to expand and "Less" to collapse. It is prompt context, not a fixed role enum or permission grant. The current name, role, and reply language are sent on every CLI turn, including resumed CLI sessions and continuations. Group context is refreshed on every turn so colleagues' replies and role edits are visible. Existing bot-management permissions govern edits. See [ADR-0021](docs/adr/0021-broadcast-mentions-and-global-bot-roles.md).
  - **Requests for input:** with bot-to-bot on, a bot can ask up to eight other member bots for input on a question. It ends its turn while recipients answer in parallel. The request message shows who is waiting, who replied, and whose input was unavailable. There is one open request per requester per group. When recipients finish or the five-minute deadline passes, the scheduler queues the requester once with the collected replies and any gaps. The requester can ask for required input and report a blocker if that input is missing. Human messages can still steer the discussion. Group pauses, budgets, and turn limits govern the continuation. See [ADR-0027](docs/adr/0027-group-input-requests.md).
  - **Live coordination:** `post_team_progress` publishes a bot-authored milestone or blocker immediately in its group while its turn continues. `get_team_status` reads another member bot's state in the same group when bot-to-bot is on. Its result and transcript entry include the latest progress for the selected turn when available. A worker heartbeat distinguishes active work from an unverified running row. The team tools never schedule turns. The requester also gets group-scoped status for missing input when a round is collected. See [ADR-0032](docs/adr/0032-live-team-updates.md).
- **Stop response:** a non-viewer group member can stop a running bot turn through
  `POST /api/workspaces/{workspaceID}/groups/{groupID}/turns/{turnID}/stop`.
  Each running bot has its own button beside its activity above the composer.
  Other bots continue, and queued turns keep their order. A stop recorded before
  reply finalization prevents the stopped reply from handing off work or opening
  an approval or input request. See [ADR-0028](docs/adr/0028-stop-private-chat-turns.md).
- **Failed turns:** the group composer shows the latest failed turn per bot, labelled with its name and a safe translated recovery message. Starting a new turn or completing a later one replaces the old warning. Raw provider errors are never displayed.
- **Limits:**
  - The turn counter resets on every human message and increments when a group bot turn is claimed. At the limit the group pauses and posts `Turn <limit> of <limit> · paused, waiting for you`.
  - Ping-pong A, B, A, B pauses the group before the limit. A bot is not scheduled to answer itself.
  - A conversation cap is checked on top of the bot and workspace budgets. Unknown cost is not zero. At 100% the group pauses.
  - One turn per bot across all conversations (the work queue from §4.2).
- **Approvals:**
  - Kinds: `new_host`, `raise_budget`, `push_protected`, `custom`, `mcp_tool`.
  - Who decides: a non-viewer human member of the group, or a workspace owner or admin. Viewers can read.
  - `POST /api/workspaces/{workspaceID}/approvals/{approvalID}/decision` stores `approved` or `denied`. A viewer gets 403. Anyone else who is not a member of that group gets 404, except a workspace owner or admin, who may decide. A second decision gets 409.
  - A human chat message clears every pause. Approving an ask-before card clears only an approval pause, queues the waiting bot, and does not reset the turn counter. Denying leaves the pause. A turn-limit pause stays when some other approval is decided.
  - `new_host` is enforced by the egress allowlist. Approval appends that one host. `raise_budget` for a bot or workspace stays on the Phase 4 budget row and still opens and clears through the budget endpoints. A group cap is an approval with `conversation_id` and no `budget_id`. Approving it does not change the number. `push_protected` records a card and does not push. The git host and the token's rights remain the enforcement.
- **UI:**
  - Slack-style sidebar listing approvals, activity, group chats and colleagues. Bots use the existing avatar and status. Humans are listed without a live presence dot.
  - Both direct messages (Bot Workspace and Team DMs) and group chats persist complete transcripts and paginate all the way to the earliest message via `?before=` and `ChatHistory.hasMore` (using both the `Load older messages` button and automatic loading when scrolling to the top, preserving scroll position and backfilling past tool-only pages). Transcripts render local calendar-day dividers (`Today`, `Yesterday`, or localized date) at each day boundary and accessible hover/focus tooltips on message timestamps (`<time>`) showing the full date and time.
  - Group and direct chat composers own their draft state so typing and mention navigation do not re-render the loaded transcript. Incoming messages continue to update the transcript while a draft is open. See [ADR-0031](docs/adr/0031-local-chat-composer-state.md).
  - Unread counts come from `last_read_position`. Marking a group read clears that badge and leaves notification rows in place. Mentions, approvals, and budget warnings insert those rows. `notify` is `all`, `mentions`, or `mute`. `GET /api/workspaces/{workspaceID}/notifications` lists the caller's rows, newest first, with a cursor and no message text. `POST .../notifications/{id}/read` marks one. `POST .../notifications/read` marks all of the caller's rows in that workspace. Another user's id and a cross-workspace id return 404. A new row publishes notification ids on the conversation's NATS subject. A missed signal is recovered by reloading the list.
  - A details and tool-trace aside per group and direct message (`Details` | `Tool calls`): bot tool calls stay out of the main chat stream and render as one compact turn chip under the bot reply (`4 tool steps · 1.9 s →`) plus a live activity bar while running. Clicking the chip opens the `Tool calls` sidebar (or the `Tool calls` tab in the Bot Workspace computer pane) with paired invocation + outcome cards starting collapsed by default; expanding a card reveals formatted key/value parameters, up to the last 5 lines of output in a dark terminal block (with hidden-line count when truncated and `[Image: source: ...]` markers stripped), screenshots, and error states.
  - Group tool screenshots are served via `GET /api/workspaces/{workspaceID}/groups/{groupID}/messages/{messageID}/tool-image`, scoped to group members and the sending bot's sandbox.

### 4.9 Usage & budgets

- **Usage records per turn:** tokens in and out, cache tokens and cost. Sources are proxy metering and CLI `result` events. The cost comes from an admin-editable `model_prices` table; OpenRouter's reported cost wins when present. Subscription logins record tokens by model and period. **Count subscription usage toward budgets** optionally applies the workspace model prices to new requests and shows the resulting internal charge separately from provider spend.
- **Budgets:** per workspace (monthly), bot (5-hour rolling window, daily UTC, weekly Mon–Sun UTC, monthly UTC) and group. Individual bot window caps can be updated via `PUT /api/workspaces/{workspaceID}/budgets` and removed via `DELETE /api/workspaces/{workspaceID}/budgets`.
  - **80 %:** warning.
  - **100 %:** no new turns, the proxy kill switch blocks LLM calls, and an approval request to raise the budget is created. For Codex plan traffic, an in-flight request can cross the limit because the Responses preview does not accept a per-request output cap. The next request is checked after settlement.
  - **Fleet dashboard:** the workspace **Spend** card shows combined daily (UTC) and weekly (Mon–Sun UTC) spend (provider spend plus internal subscription charges when enabled) alongside the optional workspace monthly cap meter.

### 4.10 Real-time events

- Each browser has one WebSocket with subscriptions to conversations, bot status, presence, control locks, approvals, egress events and stats.
- Internally, events go through NATS, so several API instances can serve the same workspace. A notification insert publishes ids only on `chat.{workspace}.{conversation}`. The payload has no message text. The conversation WebSocket already checks membership. A slow client drops the signal and reloads the list.
- VNC and terminal streams use their own authenticated WebSocket relays.

### 4.11 UI, design system & internationalisation

- The look follows dunkel.tech. [`design/STYLEGUIDE.md`](design/STYLEGUIDE.md) and [`design/tokens.json`](design/tokens.json) are the source of truth, and the screens are in [`design/README.md`](design/README.md).
- All UI strings go through `next-intl`. English ships first, and the UI is ready for more languages.
- The API returns stable error codes, which the frontend translates.
- **Accessibility:** contrast ≥ 4.5:1, 44 px touch targets, visible focus, statuses always labelled.

---

## 5. Data model (PostgreSQL, schema draft)

All tables have `created_at timestamptz NOT NULL DEFAULT now()` unless noted. IDs are `uuid DEFAULT gen_random_uuid()`.

```sql
-- Identity and tenancy
CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email VARCHAR(255) UNIQUE NOT NULL,            -- stored lower-case
    password_hash VARCHAR(255) NOT NULL,           -- Argon2id, PHC string
    display_name VARCHAR(100) NOT NULL,
    locale VARCHAR(10) NOT NULL DEFAULT 'en',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE workspaces (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(100) NOT NULL,
    slug VARCHAR(100) UNIQUE NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE workspace_members (
    workspace_id UUID REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id UUID REFERENCES users(id) ON DELETE CASCADE,
    role VARCHAR(10) NOT NULL CHECK (role IN ('owner','admin','member','viewer')),
    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, user_id)
);

CREATE TABLE invitations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    email VARCHAR(255),
    role VARCHAR(10) NOT NULL,
    token_hash BYTEA NOT NULL,
    created_by UUID REFERENCES users(id),
    expires_at TIMESTAMPTZ NOT NULL,
    accepted_at TIMESTAMPTZ
);

CREATE TABLE refresh_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    family_id UUID NOT NULL,                       -- revoked as a whole on reuse
    token_hash BYTEA NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    replaced_by UUID
);

-- Bots and their computers
CREATE TABLE bots (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    owner_user_id UUID REFERENCES users(id),
    name VARCHAR(100) NOT NULL,
    description TEXT,
    avatar_seed VARCHAR(100) NOT NULL CHECK (avatar_seed ~ '^[a-z0-9-]+$'),
    llm_mode VARCHAR(10) NOT NULL CHECK (llm_mode IN ('cli','api')), -- current bots use 'cli'
    llm_provider VARCHAR(50) NOT NULL,             -- canonical providers 'anthropic', 'openai', 'google', 'openrouter'
    llm_model VARCHAR(100) NOT NULL,
    driver_type VARCHAR(20) NOT NULL DEFAULT 'docker' CHECK (driver_type IN ('docker','kubernetes')),
    reply_language VARCHAR(10) NOT NULL DEFAULT 'en',
    turn_timeout_minutes INTEGER NOT NULL DEFAULT 10 CHECK (turn_timeout_minutes BETWEEN 1 AND 120),
    memory_auto_save BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- bot_memories (migration 00029): per-bot knowledge base entries (workspace_id, bot_id, title, content, mode IN ('always','on_demand'), tags TEXT[], source_kind, token_estimate).

CREATE TABLE environments (
    bot_id UUID PRIMARY KEY REFERENCES bots(id) ON DELETE CASCADE,
    driver VARCHAR(20) NOT NULL,
    env_id VARCHAR(255),                           -- container or pod reference
    status VARCHAR(20) NOT NULL,                   -- creating, running, stopped, error
    image VARCHAR(255) NOT NULL,
    cpu_millis INT NOT NULL,
    memory_mib INT NOT NULL,
    volume_ref VARCHAR(255),
    control_holder_user_id UUID REFERENCES users(id), -- NULL = the bot has control
    started_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Secrets and egress (values are envelope-encrypted, never returned by the API)
CREATE TABLE bot_secrets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
    domain_match VARCHAR(255) NOT NULL,            -- e.g. 'gitlab.com'
    match_subdomains BOOLEAN NOT NULL DEFAULT false,
    path_prefix VARCHAR(255),
    header_name VARCHAR(100) NOT NULL,             -- e.g. 'Authorization'
    header_template VARCHAR(20) NOT NULL,          -- 'bearer', 'basic', 'custom'
    secret_value_encrypted BYTEA NOT NULL,
    data_key_encrypted BYTEA NOT NULL,
    last_used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- workspace_secrets: same columns with workspace_id instead of bot_id; inherited by bots.

CREATE TABLE egress_rules (
    bot_id UUID PRIMARY KEY REFERENCES bots(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    policy VARCHAR(20) NOT NULL DEFAULT 'allowlist' CHECK (policy IN ('allowlist','open')),
    allowed_hosts TEXT[] NOT NULL DEFAULT '{}',
    blocked_hosts TEXT[] NOT NULL DEFAULT '{}',
    revision BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0),
    acknowledged_revision BIGINT NOT NULL DEFAULT 0 CHECK (acknowledged_revision >= 0),
    acknowledged_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- egress_invalidation_intents: durable per-bot target_revision cleared on proxy ack.
-- egress_log: partitioned by day (time, bot_id, method, host, path, result, bytes, latency_ms).

-- Conversations and messages
CREATE TABLE conversations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    type VARCHAR(5) NOT NULL CHECK (type IN ('dm','group')),
    name VARCHAR(100),                             -- groups only
    goal TEXT,
    lead_bot_id UUID REFERENCES bots(id),
    bot_talk BOOLEAN NOT NULL DEFAULT true,
    turn_limit INT NOT NULL DEFAULT 20,
    answer_policy VARCHAR(30) NOT NULL DEFAULT 'mentioned_else_lead',
    ask_before JSONB NOT NULL DEFAULT '[]',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE conversation_members (
    conversation_id UUID REFERENCES conversations(id) ON DELETE CASCADE,
    member_type VARCHAR(5) NOT NULL CHECK (member_type IN ('user','bot')),
    member_id UUID NOT NULL,
    role_text VARCHAR(100),                        -- e.g. 'Reviewer · migrations'
    last_read_message_id UUID,
    PRIMARY KEY (conversation_id, member_type, member_id)
);

CREATE TABLE messages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    sender_type VARCHAR(10) NOT NULL CHECK (sender_type IN ('user','bot','system')),
    sender_user_id UUID REFERENCES users(id),
    sender_bot_id UUID REFERENCES bots(id),
    kind VARCHAR(10) NOT NULL DEFAULT 'text',      -- text, tool, system, card, approval, secret_request, progress, team_status, memory, guardrail
    content TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE bot_sessions (                        -- CLI session per (bot, conversation)
    bot_id UUID REFERENCES bots(id) ON DELETE CASCADE,
    conversation_id UUID REFERENCES conversations(id) ON DELETE CASCADE,
    runner VARCHAR(20) NOT NULL,
    external_session_id VARCHAR(255),
    PRIMARY KEY (bot_id, conversation_id)
);

-- Usage, budgets, approvals, audit
CREATE TABLE usage_records (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
    conversation_id UUID REFERENCES conversations(id) ON DELETE SET NULL,
    source VARCHAR(20) NOT NULL,                   -- cli_result, api_response, proxy_meter
    billing_kind VARCHAR(20) NOT NULL DEFAULT 'metered', -- metered, subscription
    model VARCHAR(100),
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cache_tokens BIGINT NOT NULL DEFAULT 0,
    cost_micros BIGINT,                            -- NULL when unknown (subscription logins)
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE budgets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    scope VARCHAR(15) NOT NULL CHECK (scope IN ('workspace','bot','conversation')),
    scope_id UUID,
    period VARCHAR(10) NOT NULL CHECK (period IN ('5h','day','week','month','run')),
    limit_micros BIGINT NOT NULL
);

CREATE TABLE approvals (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    conversation_id UUID REFERENCES conversations(id) ON DELETE SET NULL,
    bot_id UUID REFERENCES bots(id) ON DELETE CASCADE,
    kind VARCHAR(20) NOT NULL,                     -- new_host, raise_budget, push_protected, custom
    payload JSONB NOT NULL,
    status VARCHAR(10) NOT NULL DEFAULT 'pending',
    decided_by UUID REFERENCES users(id),
    decided_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE audit_log (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID REFERENCES workspaces(id) ON DELETE CASCADE,
    actor_user_id UUID REFERENCES users(id),
    action VARCHAR(100) NOT NULL,
    target_type VARCHAR(50),
    target_id UUID,
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- model_prices is implemented (migration 00005).
-- Group columns, approval kinds, and notifications are specified in ADR-0015 and added by the Phase 6 migration.
-- desktop_actions is implemented (migration 00009): one row per agent action, screenshot bytes expire after workspaces.screenshot_retention_days (1..30, default 7) while the row stays.
-- workspace_guardrail_policies, workspace_guardrail_rules, llm_guardrail_events, and messages.kind = 'guardrail' are implemented in migration 00030 (see ADR-0035).
```

---

## 6. Roadmap

Each phase is a GitHub issue with a detailed task list, designs and acceptance criteria. The design hub is issue [#1](https://github.com/dunkeltech/botbox/issues/1).

| Phase | Issue | Outcome |
|---|---|---|
| 0 · Foundation & spikes | [#2](https://github.com/dunkeltech/botbox/issues/2) | Monorepo, devenv, CI, frontend skeleton; spikes for desktop streaming, CLIs without secrets, stream formats, computer use and TLS interception |
| 1 · Auth, workspaces & app shell | [#3](https://github.com/dunkeltech/botbox/issues/3) | Multi-user workspaces with roles, local auth, app shell, bot CRUD, avatar component |
| 2 · Sandbox & Docker driver | [#4](https://github.com/dunkeltech/botbox/issues/4) | Sandbox image, agent, Docker driver, VNC/terminal relays, control lock |
| 3 · Egress proxy & secrets | [#5](https://github.com/dunkeltech/botbox/issues/5) | Proxy with allowlist and secret injection, encrypted write-only secrets, egress log |
| 4 · Bot engine, commands & DMs | [#6](https://github.com/dunkeltech/botbox/issues/6) | Sandbox CLI adapters, direct messages, usage and budgets |
| 5 · Computer use | [#7](https://github.com/dunkeltech/botbox/issues/7) | Desktop tools via gRPC and MCP, provider adapters, takeover, action timeline |
| 6 · Team chat & bot-to-bot | [#8](https://github.com/dunkeltech/botbox/issues/8) | Group chats with humans and bots, orchestrator, approvals, notifications. Decisions in [ADR-0015](docs/adr/0015-group-chat-orchestration.md) |
| 7 · Kubernetes driver | [#9](https://github.com/dunkeltech/botbox/issues/9) | Kubernetes driver with isolation and quotas, Helm chart |
| 8 · Hardening & release | [#10](https://github.com/dunkeltech/botbox/issues/10) | Security review, observability, backups, e2e/a11y/i18n checks, docs, release |

---

## 7. Open decisions

| Topic | Decided in |
|---|---|
| How `agy` authenticates without credentials in the sandbox | [ADR-0003](docs/adr/0003-cli-auth-without-secrets.md) — proxy-held OAuth refresh token exchange (`llmproxy`), disabled-by-default workspace opt-in fallback |
| Kubernetes egress topology (init container with iptables vs. separate proxy pod) | ADR in [#9](https://github.com/dunkeltech/botbox/issues/9) |
| Whether the agent supports MCP for `agy` | [ADR-0005](docs/adr/0005-computer-use-protocol.md) — **Yes**, via `~/.gemini/config/mcp_config.json` (Stdio & Streamable HTTP `2025-03-26`) |
| Concrete capacity targets (desktops per host) | [ADR-0002](docs/adr/0002-desktop-streaming.md) — `2 vCPU` / `2 GiB RAM` default per sandbox; **8 active desktops per 16 GB / 8 vCPU host** (16–20 per 32 GB / 16 vCPU host) |
| Selective TLS interception vs. full MITM & CLI stream normalization | [ADR-0006](docs/adr/0006-selective-tls-interception.md) (per-bot ECDSA CA + secret-only MITM) & [ADR-0004](docs/adr/0004-cli-stream-formats.md) (`engine.Event` unified parser) |
