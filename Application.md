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

### 4.3 Bot sandbox ("computer")

Each bot gets its own isolated Debian-based container:

- **Desktop:** XFCE with a VNC server on `:0` at a fixed 1280×800 resolution, and websockify bound to the container network. The browser connects through an authenticated relay in the API (short-lived ticket).
- **Software:** Chromium, git, build tools, Python, Node.js, and `claude`, `codex` and `agy` at pinned versions.
- **Rights:**
  - User `bot` with passwordless `sudo` **inside** the container, so it can install with `apt-get`, `pip` and `npm`.
  - No privileged mode and no `NET_ADMIN` / `SYS_ADMIN`; a seccomp profile; gVisor where available.
- **Agent:** a small Go service started by a process supervisor. It offers gRPC for exec (streaming), PTY sessions, files under `/home/bot`, health, and the desktop tools (§4.7), which it also exposes as a local MCP server for the CLIs.
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
  - gRPC for API mode, with adapters for Anthropic computer use, OpenAI computer use, and generic image-returning function tools for OpenRouter.
  - A local MCP server for the CLIs.
- **Browser tool (recommended):** Chromium with remote debugging on localhost, driven via CDP or Playwright. It is more reliable and cheaper than pixel clicks on web pages.
- **Limits per turn:** screenshots, steps and tokens.
- **Takeover:** a human taking control interrupts the bot within 1 s. On hand-back, the bot is told to take a fresh screenshot.
- **Transparency:**
  - The bot's labelled cursor is shown in the viewer.
  - An action timeline (`desktop_actions`) lists every desktop action.
  - Screenshots are kept only for a configurable time (default 7 days).

### 4.8 Team chat & bot-to-bot orchestration

- **Conversations:** direct messages (one user and one bot, private) and groups (several humans and bots).
- **Group settings:** goal, lead bot, bot-to-bot on or off, turn limit, answer policy (`mentioned_else_lead`, `lead_first`, `round_robin`), "ask me before" rules, budget.
- **Orchestrator:**
  - **Router:** decides which bots answer a message.
  - **Context builder:** each bot turn gets the goal, the roster with roles, the rules, the recent messages and a rolling summary.
  - **Handoffs:** mentions in a bot reply schedule the next bot's turn, if bot-to-bot is on.
- **Limits:**
  - The turn counter resets on every human message, and the group pauses at the limit.
  - Loop and ping-pong detection pauses the group early.
  - Per-conversation budgets apply.
  - One turn per bot across all conversations (the work queue from §4.2).
- **Approvals:**
  - Kinds: new outbound host, budget raise, protected push, custom.
  - Who decides: human group members or workspace admins.
  - Enforced in infrastructure where possible (proxy, budgets, token rights), not only by prompting.
- **UI:**
  - Slack-style sidebar listing approvals, activity, group chats and colleagues: bots with live avatar and current activity, humans with presence.
  - Unread counts and mention notifications.
  - A details pane per conversation.

### 4.9 Usage & budgets

- **Usage records per turn:** tokens in and out, cache tokens and cost. Sources are CLI `result` events, API responses and proxy metering. The cost comes from an admin-editable `model_prices` table; OpenRouter's reported cost wins when present. Subscription logins record tokens only.
- **Budgets:** per workspace (monthly), bot (daily, monthly) and group.
  - **80 %:** warning.
  - **100 %:** no new turns, the proxy kill switch blocks LLM calls, and an approval request to raise the budget is created.

### 4.10 Real-time events

- Each browser has one WebSocket with subscriptions to conversations, bot status, presence, control locks, approvals, egress events and stats.
- Internally, events go through NATS, so several API instances can serve the same workspace.
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
-- Also planned: model_prices, notifications, desktop_actions (with screenshot retention).
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
| 6 · Team chat & bot-to-bot | [#8](https://github.com/dunkeltech/botbox/issues/8) | Group chats with humans and bots, orchestrator, approvals, notifications |
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
