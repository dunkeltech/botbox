# bot.Box: Software Requirements & Architecture Specification

> Status: living document. It describes the target architecture. The implementation is planned in phases, which are tracked as GitHub issues (see [§6](#6-roadmap)). Decisions taken during implementation are recorded as ADRs in `docs/adr/` and reflected here.

## 1. Overview & vision

bot.Box is a multi-user web application for creating and running **bot computer agents**. Each bot combines a large language model (LLM) with its own isolated sandbox computer. The computer has a desktop that users can watch and take over in the browser, and the bot has full root access inside it.

Bots behave like **colleagues**. Users talk to them in direct messages and put them together in group chats, where bots hand work to each other through mentions while humans stay in control (turn limits, approvals, budgets).

### Goals
- Create, configure and run bots, each with a sandboxed Linux desktop, browser and agent CLIs.
- Drive bots either through an **agent CLI inside the sandbox** (`claude`, `codex`, `agy`) or through a **model API** (OpenRouter, Anthropic, OpenAI) with tools.
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
|  Auth & workspaces · Bot engine (CLI + API runners) · Orchestrator       |
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
  - JWT access token (EdDSA, 15 min) plus an opaque refresh token. The refresh token is rotated on every use, stored hashed, and **reuse revokes the whole token family**.
  - Cookies are `HttpOnly`, `Secure` and `SameSite=Lax`. Mutating requests are protected against CSRF.
  - Login and refresh are rate-limited, and failed logins go to the audit log.
- **Later:** OpenID Connect / OAuth2, LDAP / Active Directory, SAML 2.0.
- **Workspaces:** a user can belong to several workspaces. Bots, chats, secrets and budgets belong to a workspace.
- **Roles:** `owner`, `admin`, `member`, `viewer`.

  | Action | owner | admin | member | viewer |
  |---|:-:|:-:|:-:|:-:|
  | Manage members, roles, drivers, budgets | ✓ | ✓ | | |
  | Create bots and groups; chat | ✓ | ✓ | ✓ | |
  | Manage a bot's secrets | ✓ | ✓ | bot owner | |
  | View bots, desktops, group chats | ✓ | ✓ | ✓ | ✓ |

- **Invitations:** a link with a token and an expiry, sent by e-mail through SMTP if configured. Accepting adds the user with the invited role.
- **Tenant isolation:** every query is scoped by `workspace_id`. Accessing another workspace's resources returns 404.

### 4.2 Bot engine & LLM integration

A bot runs in one of two modes:

1. **Agent CLI in the sandbox.** The CLI runs inside the bot's computer and acts there directly. The in-sandbox agent starts it, and the API talks to its stdio over gRPC.
2. **Model API.** The bot engine calls a provider API (OpenRouter, Anthropic, OpenAI) and runs the tool loop itself. The tools are executed through the agent: `exec`, `read_file`, `write_file`, `list_files` and the desktop tools (§4.7).

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

- **Permissions:** permission prompts inside the CLI are disabled, because the sandbox is the boundary. For `agy`, scoped `permissions.allow` rules are possible. `agy` soft-denies tools (exit code 0, notice on stderr), so the adapter must surface these notices.
- **One event model** for all runners. Every adapter is tested against recorded stream fixtures.
- **Per-bot work queue** (NATS JetStream). A bot has one computer, so it runs **one turn at a time**. Messages from other conversations are queued, and the UI shows their position.
- **Sessions:** one CLI session per (bot, conversation), so every conversation keeps its own context.
- **Status:** derived from the events (`idle`, `thinking`, `action`, `offline`). It drives the status chips and the avatar mood.
- **Reply language** is a per-bot setting. System prompts are in English.

Claude CLI bots can use a Pro/Max subscription through an encrypted
`llm-anthropic-oauth` secret, prepared on the bot Secrets page. The user completes
`claude setup-token` outside the bot (or uses `make claude-login`), then stores the
token through the write-only form. The sandbox receives only an OAuth placeholder
and no API key; the proxy injects the actual token. Generic API mode ignores this
credential. Subscription request records keep tokens and null cost with an
explicit `billing_kind=subscription`; those records do not represent unknown API
billing and do not require model prices. See
[ADR-0011](docs/adr/0011-claude-subscription-outside-sandbox.md).

Typing `claude` in the bot computer uses that same placeholder and metered
route. The pinned CLI is 2.1.323. Its first-run connectivity check and
workspace trust prompt are skipped there, because provider CONNECT tunnels stay
closed and the desktop cannot answer the prompt. The `sonnet` alias is sent to
the provider as `claude-sonnet-5-5`; a monetary budget needs a price for that
name, while subscription records stay tokens-only. Desktop calls are metered
without a chat turn. Pause still blocks them. Holding the desktop stops engine
turns and does not block that personal CLI. See
[ADR-0012](docs/adr/0012-interactive-claude-in-the-sandbox.md).

The executable defaults to Docker; simulation requires the explicit development
setting `BOTBOX_COMPUTE_DRIVER=memory`. Unrecognized driver names fail startup.
Existing simulated computers require a restart before a CLI turn can run. Chat
uses allowlisted failure categories with links to computer controls or Secrets,
and CLI preflight checks provider credentials before launching a process. Private
dependency errors are never displayed. See
[ADR-0010](docs/adr/0010-explicit-simulation-and-turn-recovery.md).

Phase 4 implementation: private DM APIs persist conversation-local history and
turns. JetStream notifications drive the API worker, and authorized conversation
WebSockets stream normalized events. `/team` is the group chat and still opens
the same DM panel for a colleague. A chat message carries the sender's user or
bot id so a group transcript can name the speaker. A group approval message is a card, and `/team/approvals` is the inbox for the same decisions. Workspace and bot settings
show spend and budget limits. The workspace provides a persisted bot-wide
Pause/Resume control. Pausing cancels the active
turn, keeps queued messages in order, and survives API restarts. Taking desktop
control also blocks new turns until release. Interrupted turns are never replayed;
resuming only permits queued work. Bot managers can change pause state; viewers
can read it. The runner lifecycle owns a bounded event stream and propagates
cancellation to CLI process groups and API requests. API runners
support OpenAI-compatible providers and native Anthropic Messages with bounded
sandbox tools. Credentials named `llm-openrouter`, `llm-openai` and `llm-anthropic`
are resolved from encrypted bot/workspace secrets outside the sandbox.

Budget accounting uses UTC periods and integer microdollars. Execution is
serialized per workspace to protect shared spend. Model prices can be edited via
`PUT /api/workspaces/{workspaceID}/model-prices`; budgeted API turns require a
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
URL derived from `DATABASE_URL` or the `BOTBOX_EGRESS_DATABASE_URL` override. Claude/Codex requests take a workspace budget lock,
create a provisional usage record, and settle usage before the next request.
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
  - Input from viewers is rejected server-side.
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

**Principle:** credentials such as Git tokens and API keys **never** enter the LLM prompt, the bot's environment or its files.

**How the proxy works (one per bot):**
1. All bot traffic must pass the proxy. On Docker, the sandbox is only attached to an internal network; on Kubernetes, see §4.4. The sandbox gets `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY`.
2. **LLM base-URL mode:** the CLIs and the API runner call plain-HTTP proxy endpoints (for example `/llm/anthropic/…`) with placeholder keys. The proxy forwards over TLS and swaps in the real key. No TLS interception is needed.
3. **Forward mode:** `CONNECT` tunnels. Hosts without a secret are tunnelled without inspection, and only the allowlist is checked (CONNECT host and SNI).
4. **Selective TLS interception** only for hosts with a configured secret:
   - Each bot has its own CA, with the key stored encrypted.
   - Leaf certificates are issued on the fly.
   - The CA is installed in the sandbox trust stores: system, NSS for Chromium, Node, Python, git.
5. **Header templates per secret:** `bearer`, `basic` (`user:{secret}`, needed for git over HTTPS) or custom, with an optional path prefix. Placeholder headers sent by the bot are stripped.
6. **Allowlist policy** (default) or open internet. A blocked host gets `403` and an approval request ("allow new host").
7. **Metering:** the proxy reads `usage` from LLM responses, including streaming ones. A **kill switch** blocks LLM calls when a budget is exhausted (§4.9).
8. **Egress log:** time, method, host, path without query, result (`token added`, `passed`, `blocked`), bytes and latency. Stored partitioned by day with a retention period.

**Result:** the bot reaches external repositories and APIs without knowing any secret. Exfiltration through the LLM fails, because there is nothing to exfiltrate and unknown hosts are blocked.

**Storage:**
- Envelope encryption: AES-256-GCM with a data key per secret, wrapped by a key-encryption key from a file or environment variable (Vault or KMS later). KEK rotation is supported.
- The API is **write-only**: secrets can be created, replaced and deleted, but never read back.
- Bot secrets and workspace secrets (inherited by bots).
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
- **One implementation, two interfaces:**
  - gRPC for API mode, with adapters for Anthropic computer use (`computer_20251124`), OpenAI computer use (translated to the Responses API), and generic image-returning function tools for OpenRouter.
  - A local MCP server in the same agent process at `POST http://127.0.0.1:9091/mcp`, registered with the CLIs. Stdio is only the `--mcp-stdio` fallback.
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
- **Orchestrator** (`internal/orchestrator`), used for groups only:
  - **Router:** mentioned bots, otherwise the lead; lead first plus mentions; or round robin.
  - **Context builder:** goal, roster with roles, rules, the last 50 messages, and an extractive summary of older text. The summary does not call a model. The prompt tells the bot to mention the colleague it hands work to. Secrets are not included.
  - **Handoffs:** mentions in a bot reply schedule the next bot through the per-bot queue, only when bot-to-bot is on.
- **Limits:**
  - The turn counter resets on every human message and increments when a group bot turn is claimed. At the limit the group pauses and posts `Turn <limit> of <limit> · paused, waiting for you`.
  - Ping-pong A, B, A, B pauses the group before the limit. A bot is not scheduled to answer itself.
  - A conversation cap is checked on top of the bot and workspace budgets. Unknown cost is not zero. At 100% the group pauses.
  - One turn per bot across all conversations (the work queue from §4.2).
- **Approvals:**
  - Kinds: `new_host`, `raise_budget`, `push_protected`, `custom`.
  - Who decides: a non-viewer human member of the group, or a workspace owner or admin. Viewers can read.
  - `POST /api/workspaces/{workspaceID}/approvals/{approvalID}/decision` stores `approved` or `denied`. A viewer gets 403. Anyone else who is not a member of that group gets 404, except a workspace owner or admin, who may decide. A second decision gets 409.
  - A human chat message clears every pause. Approving an ask-before card clears only an approval pause, queues the waiting bot, and does not reset the turn counter. Denying leaves the pause. A turn-limit pause stays when some other approval is decided.
  - `new_host` is enforced by the egress allowlist. Approval appends that one host. `raise_budget` for a bot or workspace stays on the Phase 4 budget row and still opens and clears through the budget endpoints. A group cap is an approval with `conversation_id` and no `budget_id`. Approving it does not change the number. `push_protected` records a card and does not push. The git host and the token's rights remain the enforcement.
- **UI:**
  - Slack-style sidebar listing approvals, activity, group chats and colleagues. Bots use the existing avatar and status. Humans are listed without a live presence dot.
  - Unread counts come from `last_read_position`. Marking a group read clears that badge and leaves notification rows in place. Mentions, approvals, and budget warnings insert those rows. `notify` is `all`, `mentions`, or `mute`. `GET /api/workspaces/{workspaceID}/notifications` lists the caller's rows, newest first, with a cursor and no message text. `POST .../notifications/{id}/read` marks one. `POST .../notifications/read` marks all of the caller's rows in that workspace. Another user's id and a cross-workspace id return 404. A new row publishes notification ids on the conversation's NATS subject. A missed signal is recovered by reloading the list.
  - A details pane per group: goal, members, how bots talk, and links to their computers.

### 4.9 Usage & budgets

- **Usage records per turn:** tokens in and out, cache tokens and cost. Sources are CLI `result` events, API responses and proxy metering. The cost comes from an admin-editable `model_prices` table; OpenRouter's reported cost wins when present. Subscription logins record tokens only.
- **Budgets:** per workspace (monthly), bot (daily, monthly) and group.
  - **80 %:** warning.
  - **100 %:** no new turns, the proxy kill switch blocks LLM calls, and an approval request to raise the budget is created.

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
    llm_mode VARCHAR(10) NOT NULL CHECK (llm_mode IN ('cli','api')),
    llm_provider VARCHAR(50) NOT NULL,             -- 'claude', 'codex', 'agy', 'openrouter', 'anthropic', 'openai'
    llm_model VARCHAR(100) NOT NULL,
    driver_type VARCHAR(20) NOT NULL DEFAULT 'docker' CHECK (driver_type IN ('docker','kubernetes')),
    reply_language VARCHAR(10) NOT NULL DEFAULT 'en',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

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
    policy VARCHAR(10) NOT NULL DEFAULT 'allowlist' CHECK (policy IN ('allowlist','open')),
    allowed_hosts TEXT[] NOT NULL DEFAULT '{}'
);
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
    kind VARCHAR(10) NOT NULL DEFAULT 'text',      -- text, tool, system, card, approval
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
    period VARCHAR(10) NOT NULL CHECK (period IN ('day','month','run')),
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
| 4 · Bot engine, commands & DMs | [#6](https://github.com/dunkeltech/botbox/issues/6) | `claude`, `codex`, `agy` adapters, API mode, direct messages, usage and budgets |
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
