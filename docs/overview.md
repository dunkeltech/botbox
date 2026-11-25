<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# bot.Box technical overview

bot.Box runs AI agents in isolated Linux desktops. People interact with bots
through direct messages, group chats, and a browser-based desktop viewer. See
the [README](../README.md) for installation and development setup.

## How bot.Box runs

- Caddy serves the Next.js app, the Go API, and WebSockets on the same origin.
  The API manages workspaces, bot turns, and Docker containers.
- PostgreSQL stores application state. NATS JetStream queues bot turns and NATS
  carries live updates. Each bot runs one turn at a time across its conversations.
  Different bots can run concurrently. See [ADR-0008](adr/0008-private-dms-durable-turns-and-budgets.md)
  and [ADR-0025](adr/0025-parallel-bot-turns-and-activity.md).
- A sandbox runs the bot's desktop and agent CLI. Its Go agent provides desktop,
  file, and command access. A Docker volume keeps the bot's home directory across
  restarts. See [ADR-0002](adr/0002-desktop-streaming.md) and
  [ADR-0013](adr/0013-localhost-agent-port.md).
- The API holds the Docker socket. Each bot uses an isolated bridge for its
  sandbox and relay. A trusted egress worker applies network rules and adds
  credentials to matching requests. The sandbox has no direct internet route
  or provider credentials. See [ADR-0038](adr/0038-split-egress-and-chromium-isolation.md)
  and the [security invariants](../AGENTS.md#7-security-invariants-never-break-these).

On an empty database the API requires a one-time secret to create the first
owner and workspace. Compose generates the secret in a named volume. In devenv,
the API prints a generated secret once unless `BOTBOX_SETUP_SECRET` is set. The
API keeps only the secret's digest for verification. After setup, the setup
endpoints return `404`.

## Workspace permissions

Roles apply within a single workspace. This table covers common operations.

| Action | Owner | Admin | Member | Viewer |
| --- | --- | --- | --- | --- |
| Read workspace details, list bots, and view a bot desktop | Yes | Yes | Yes | Yes |
| Read a group chat | In own groups | In own groups | In own groups | In own groups |
| Create bots or groups and send chat messages | Yes | Yes | Yes | No |
| Manage bots and their credentials | Any bot | Any bot | Own bots | No |
| Manage workspace secrets, budgets, GitLab instances, MCP servers, and guardrails | Yes | Yes | No | No |
| Reconcile unresolved subscription usage | Yes | Yes | No | No |
| Manage members and invitations | All roles | Member and viewer roles | No | No |
| Change workspace settings or delete the workspace | Yes | No | No | No |

- Sending a message still requires access to the conversation. Direct messages
  are private to their human participant. Group chat reads require group membership.
- A group member with write access can decide a group approval, and a
  direct-message participant with write access can decide their direct-message
  approvals. Workspace owners and admins can also decide group approvals and
  workspace budget approvals.
- A workspace must keep at least one owner. Deleting a workspace also requires
  the caller to belong to another workspace and the workspace to have no active
  bot work or credential sessions.
- Protected routes return `401` when authentication is missing. Access to a
  workspace the caller does not belong to returns `404`. Routes use `403` when
  the caller can access the resource but lacks permission for an action.

See the [role rules](../backend/internal/workspaces/rbac.go), the
[OpenAPI specification](../backend/api/openapi.yaml), and
[ADR-0037](adr/0037-session-and-infrastructure-authorization.md) for details.

## Where to find details

- The [ADRs](adr/) record architecture decisions and the reasons behind them.
- The [database migrations](../backend/db/migrations/) define the schema.
- The [agent protocol](../backend/proto/agent/v1/agent.proto) defines the gRPC API.
- The [design guide](../design/STYLEGUIDE.md) defines UI and accessibility rules.
- The [runtime and integrations guide](guides/runtime-and-integrations.md) covers
  provider setup, GitLab access, and runtime checks.
- [GitHub issues](https://github.com/dunkeltech/botbox/issues) track planned work,
  including the [Kubernetes driver and Helm chart](https://github.com/dunkeltech/botbox/issues/9)
  and [release hardening](https://github.com/dunkeltech/botbox/issues/10).
