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
devenv up             # starts Postgres, NATS, Go API, Next.js frontend, and Caddy (:8000)
```

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
