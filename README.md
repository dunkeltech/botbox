# bot.Box

Multi-user web application for creating and running bot computer agents. Each bot combines an LLM with its own isolated sandboxed Linux desktop that humans can watch, take over, and collaborate with in direct messages and team group chats.

- **Specification:** [`Application.md`](Application.md)
- **Design system & screens:** [`design/STYLEGUIDE.md`](design/STYLEGUIDE.md), [`design/tokens.json`](design/tokens.json), [`design/README.md`](design/README.md)
- **Architecture decisions:** [`docs/adr/`](docs/adr/)
- **Coding & security rules:** [`AGENTS.md`](AGENTS.md)

## Prerequisites

- [Nix](https://nixos.org/download/) and [devenv](https://devenv.sh/) (all language toolchains, Postgres, NATS, Caddy, code generators, and linters come from `devenv.nix`)
- Optional: [`direnv`](https://direnv.net/) for automatic shell activation
- A running **Docker Engine** on the host for bot sandboxes (`docker` CLI is provided by `devenv`)

## Quick start

```bash
direnv allow          # or: devenv shell
cp .env.example .env  # optional local overrides
devenv up             # starts Postgres (:15432), NATS, Go API, Next.js frontend, and Caddy (:8000)
```

Postgres listens on `127.0.0.1:15432`. Port 5432 stays free for other local projects.

With the environment active, use `Makefile` targets for development and checks:

```bash
make dev              # alias for: devenv up
make gen              # generate sqlc queries and protobuf Go stubs
make lint             # run golangci-lint, ESLint (incl. i18n check), tsc, and Prettier
make test             # run Go (-race) and Vitest unit tests
make build            # build backend binaries (api, agent, egress-proxy) and Next.js frontend
make sandbox-image    # build local botbox/sandbox:dev Docker image
```

Local same-origin entry point when `devenv up` is running: <http://127.0.0.1:8000> (Caddy routes `/api/*` and `/ws/*` to the Go API on `:8080`, and all other paths to Next.js on `:3000`).

## Repository layout

```text
backend/            Go module: cmd/api, cmd/agent, cmd/egress-proxy, internal/…, db/, proto/
frontend/           Next.js app (App Router, strict TypeScript, Tailwind, next-intl, TanStack Query)
images/sandbox/     Bot sandbox Linux desktop image
deploy/             Deployment manifests and Helm chart (Phase 7)
docs/adr/           Architecture decision records
design/             Styleguide, tokens.json, 1440 px screen PNGs, canvas sources
```

## Phase 4 runtime

Set `NATS_URL` to enable durable DM scheduling. The executable and devenv use
Docker by default. `BOTBOX_COMPUTE_DRIVER=memory` explicitly selects the development
simulator, which cannot run CLI bots and is never exposed to model tools. Unknown
driver names fail startup. Build both images with `make sandbox-image proxy-image`.

When upgrading proxy ingress, build the new egress image before starting the
updated API. Startup replaces legacy sidecars with bot-network-bound sidecars.
Restart already-running sandboxes so their clients load the new sidecar CA.

API-mode bots resolve write-only secrets named `llm-openrouter`, `llm-openai` or
`llm-anthropic`, first in bot scope and then in workspace scope. The secret's domain
must match the provider host; its path restriction is preserved. Configure model
prices through `PUT /api/workspaces/{workspaceID}/model-prices` before applying
monetary budgets. Owners and admins can edit budgets in the fleet or bot settings.
Private DMs are available in the bot workspace and under `/team`.

The proxy database URL defaults to `DATABASE_URL`, with loopback hosts translated
to `host.docker.internal` for Docker Desktop. The devenv configuration sets this
explicitly. Override `BOTBOX_EGRESS_DATABASE_URL` when the proxy needs a different
Postgres endpoint, especially on Linux or remote Docker deployments.
`BOTBOX_EGRESS_IMAGE` defaults to `botbox/egress-proxy:dev`. The API provisions the
per-bot proxy with its encryption key configuration; do not put database or
provider credentials in the sandbox environment. Existing unproxied sandboxes
must be recreated, retaining their named home volume.

Each CLI request is metered before the next one is admitted. Missing usage or
prices leave an unpriced record and block applicable monetary budgets until
reconciled. CLI aggregates do not double charge proxy-metered requests. agy OAuth
exchange and live-provider acceptance runs remain open. See ADR-0008 and ADR-0009.

After building both images, run the optional real-Docker check with
`cd backend && BOTBOX_DOCKER_SMOKE=1 go test -race ./internal/compute/docker -run TestProxyRealDockerIsolation -count=1`.
It starts an isolated test database and per-bot containers, validates public CA
startup and authenticated agent recovery, and verifies that native LLM tunnels
are blocked. It never calls a real LLM. This local check uses Docker Desktop's
`host.docker.internal` to reach the temporary database.

### Recovering a failed CLI chat

If a previously created computer has driver `fake`, use **Restart** in its bot
workspace to provision a real computer. Existing messages are kept and failed
turns are not replayed automatically. Configure the provider credential on the
bot's **Secrets** page or at workspace scope: Claude uses `llm-anthropic` restricted
to `api.anthropic.com`; Codex uses `llm-openai` restricted to `api.openai.com`.
Enter the secret value only in the write-only Secrets form. A host-side CLI login
is not automatically shared with a sandbox. Model aliases such as `sonnet` are
passed to the CLI unchanged.

New failures distinguish computer startup/recreation, proxy readiness, missing
credentials and provider failures. Chat links to the relevant controls; no raw
provider error or credential is included. See ADR-0010.

### Claude Pro / Max login

For a Claude CLI bot, open its **Secrets & network** page and choose **Configure
Claude Pro / Max**. Run `claude setup-token` in your own terminal and complete the
Claude login. If no local Claude CLI is installed, run `make claude-login` from a
devenv terminal instead. This uses a temporary trusted helper, outside the bot,
and prints a manual browser link. Paste any returned authorization code into that
terminal, then put the generated token into **Secret value** in the prepared form.
Never send the token through chat.

The prepared secret is `llm-anthropic-oauth`, restricted to `api.anthropic.com`.
The real token stays encrypted in bot.Box and is injected only by the external
proxy. The official CLI receives a placeholder, with no API-key fallback.
`claude` typed in the bot computer uses the same route. Rebuild the sandbox and
proxy images, then restart the computer, after changing that launcher.
Subscription requests record tokens. Enable **Count subscription usage toward
budgets** in workspace settings to apply configured model prices as internal
charges. Provider plan limits remain in effect.
Rotate the connection by running setup again and replacing the token in Secrets.
Generic API-mode bots do not use this subscription credential. See ADR-0011.

Build the updated images with `make sandbox-image proxy-image`, then start the
bot computer. `make test-sandbox-cli` verifies the actual pinned CLI and Sonnet
alias against a local fake provider with networking disabled.

### ChatGPT plan for interactive Codex

Follow the [Codex desktop setup guide](docs/guides/codex-desktop-setup.md) to
connect your ChatGPT plan, start `codex` in the bot computer, and review its token
usage. The [implementation plan](docs/plans/codex-desktop-subscription.md)
describes the metered route and the subscription budget policy.

### Deterministic engine doubles

`backend/internal/engine/enginetest` provides a scripted HTTP provider and a fake
CLI turn executor. Provider responses contain ordered chunks with optional channel
gates, request validation and dependency status codes. Use `httptest.NewServer`
and assert `Remaining()` and `Errors()` after the test. CLI doubles replay the
recorded streams through the production live parsers and support gates, stderr
soft denials and nonzero exits. Gates synchronize streaming/cancellation tests
without sleeps; neither double calls an LLM or launches an agent process.

## Self-hosted GitLab access

Public GitLab installations are configured through the UI. No per-domain
environment variable, IP address entry or broker restart is needed.

1. As a workspace owner or admin, open Workspace settings and register the
   instance under **Self-hosted GitLab**, using its exact HTTPS origin, such as
   `https://gitlab.example.org`. Register only installations trusted to receive
   credentials.
2. Open each bot's settings, select its allowed instances under **GitLab access**,
   and save. Different bots can use different instances; multiple selections are
   supported. No selection means no self-hosted GitLab access.
3. Allow the domain in that bot's egress settings. Explicit network blocks still
   take precedence, and assigning an instance does not expand network permission.
4. For browser access, the bot calls `request_secret` with `kind="browser_login"`,
   `adapter_id="gitlab_web"` and `target_origin="https://gitlab.example.org"`.
   Enter credentials only in the write-only secret dialog. The bot continues with
   `broker_open`, `broker_read`, `broker_click` and `broker_screenshot`.

The auth broker needs the same `DATABASE_URL` as the API and broker-managed
Chromium (`AUTH_BROKER_CDP_URL` unset). These are service settings, configured once;
individual GitLab domains are read live from the database. The previous
`AUTH_BROKER_GITLAB_TARGETS` setting is no longer used. Keep the broker database and
control plane isolated from bot sandboxes.

The development process listens on loopback and generates a per-project broker
token under `DEVENV_STATE` for the API and broker. If the Docker engine cannot
reach host loopback, configure a private broker interface reachable only by
the API and the trusted egress sidecars.

For every new connection, a broker-local proxy checks the domain's DNS answers and
dials only a validated public IP, while Chromium verifies the HTTPS certificate.
A public IP change needs no configuration update. Private/mixed DNS answers,
internal infrastructure addresses, other origins and non-443 ports are blocked.

Removing bot access revokes its browser sessions, API/Git leases and saved bot
credentials, cancels pending requests, and excludes its use of existing inherited
workspace credentials. Other bots retain their grants. Removing a workspace
instance revokes access and removes its saved credentials for all assigned bots.
Re-adding access requires a fresh login or explicit credential fulfillment. After upgrading from IP pins, assign instances to bots and sign in
again; previous custom sessions are revoked by migration.

Same-origin MFA uses human takeover. Cross-domain SSO, third-party asset domains,
private target IPs, custom CA setup, alternate ports, subpath installations and
GraphQL and SSH remain unsupported. See
[ADR-0019](docs/adr/0019-live-public-gitlab-and-bot-assignment.md) and
[ADR-0020](docs/adr/0020-self-hosted-gitlab-cli-grants.md).

### glab and Git HTTPS credentials

For CLI access, use the same workspace registration, bot assignment and egress
permission as for browser login. The browser broker is not required for CLI access.
The bot requests the reviewed adapter with an explicit origin:

```json
{
  "kind": "cli_egress",
  "adapter_id": "gitlab_api",
  "target_origin": "https://gitlab.example.org",
  "env_placeholder_name": "GITLAB_TOKEN",
  "reason": "Read GitLab projects with glab"
}
```

Submit the real token only through the write-only secret modal. The bot receives
a generated `botbox-placeholder-...` value, which it can use with `GITLAB_HOST`,
`GITLAB_API_HOST` and HTTPS protocols to run `glab api user`. The proxy removes the
placeholder header and injects the real token as PRIVATE-TOKEN only on approved
REST API requests. Do not run `glab auth login` with a real token, save one in a
file/keyring, or put one in the sandbox environment or command arguments.

For Git HTTPS, request `gitlab_git`; the proxy injects Basic authentication only
on smart-HTTP Git endpoints. API and Git grants are separate and can coexist for
the same instance. Existing response mediation and limits remain in force.

Temporary grants end on turn completion/cancellation, revocation or expiry. Saved
bot/workspace grants remain outside the sandbox. A workspace token is inherited
only while the bot has instance and network permission, and has not had that
credential revoked for it. Re-adding an assignment does not restore old grants;
provide credentials again through a new secret request. Legacy generic or parent-
domain secrets cannot bypass these checks. Previously registered domains retain
an internal credential boundary after removal; they must be registered, assigned
and explicitly granted reviewed GitLab credentials before authenticated use.

The chat now distinguishes missing registration, missing bot assignment, network
blocks, invalid requests, limits and unavailable policy services. Fix the indicated
setting and retry. Upstream token rejection is returned as an HTTP authentication
failure; no token values are included in diagnostics.

After updating this feature, rebuild with `make sandbox-image proxy-image` and
restart the bot computer so both its agent and egress proxy use the new code.
Restarting the API applies the database migration. The existing glab installation
in the bot's persistent home can be reused. Host integration tests use the glab
package declared in `devenv.nix`; run `devenv shell -- make lint test`.
