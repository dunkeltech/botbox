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
Subscription requests record tokens only; they do not require model prices or
appear as unknown monetary charges. Provider plan limits remain in effect.
Rotate the connection by running setup again and replacing the token in Secrets.
Generic API-mode bots do not use this subscription credential. See ADR-0011.

Build the updated images with `make sandbox-image proxy-image`, then start the
bot computer. `make test-sandbox-cli` verifies the actual pinned CLI and Sonnet
alias against a local fake provider with networking disabled.

### Deterministic engine doubles

`backend/internal/engine/enginetest` provides a scripted HTTP provider and a fake
CLI turn executor. Provider responses contain ordered chunks with optional channel
gates, request validation and dependency status codes. Use `httptest.NewServer`
and assert `Remaining()` and `Errors()` after the test. CLI doubles replay the
recorded streams through the production live parsers and support gates, stderr
soft denials and nonzero exits. Gates synchronize streaming/cancellation tests
without sleeps; neither double calls an LLM or launches an agent process.
