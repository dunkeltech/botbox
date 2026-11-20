<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# bot.Box

bot.Box is a self-hosted web app for running AI agents in shared workspaces.
Each bot runs an agent CLI in its own isolated Linux desktop. You can watch the
desktop in your browser, take control, and hand it back to the bot.

- Chat with bots in direct messages or bring people and several bots into a
  group chat.
- Open a bot's desktop, terminal, and files from the browser.
- Use approvals, turn limits, and budgets to control bot activity.
- Set outbound network rules per bot. Provider credentials are stored outside
  the bot computer and added only to matching requests by the egress proxy.

## Run locally with Docker Compose

You need Docker Engine 28 or newer with Linux containers, a local Docker socket,
and Docker Compose. The first build downloads base images and agent CLIs.

Run these commands from the repository root.

```bash
docker compose up --build -d --wait
docker compose exec -T api cat /var/lib/botbox/setup-secret
```

Open <http://127.0.0.1:8000>. Enter the setup secret to create the first workspace
owner. Create a bot, select a provider and model, and start its computer. You
can try the desktop before adding provider credentials. Add them in the bot's
**Secrets & network** settings to run agent turns. See the
[runtime and integrations guide](docs/guides/runtime-and-integrations.md) for
provider and GitLab setup.

Compose starts the API, frontend, Postgres, NATS with JetStream, auth broker,
and Caddy. It also builds the sandbox and egress images used when a bot starts.
Only Caddy publishes a port, bound to `127.0.0.1`. The API uses the host Docker
socket to manage bot containers. Named Docker volumes store the database,
JetStream state, and local keys. Bot home directories have their own volumes.

- If port 8000 is in use, run `export BOTBOX_COMPOSE_PORT=8001` before starting
  Compose and open <http://127.0.0.1:8001>.
- Set `BOTBOX_DOCKER_SOCKET` if your local Docker socket is outside
  `/var/run/docker.sock`.
- If Docker reports exhausted address pools, check the Engine's network
  configuration. Compose needs two bridge subnets. Each bot also needs a
  subnet for its isolated bridge.

Stop active bots in the app before running `docker compose stop`. Resume the
stack with `docker compose up -d --wait`. Before removing the stack with
`docker compose down`, remove its bot computers through the app so their workers
release the control network.

## Development setup

Install [Nix](https://nixos.org/download/) and [devenv](https://devenv.sh/) and
start Docker Engine. The [devenv configuration](devenv.nix) provides Go, Node.js,
Postgres, NATS, Caddy, code generators, and linters.

From the repository root, run `devenv shell`. If you use `direnv`, run
`direnv allow` instead. In that environment, install the frontend dependencies
and build the images before starting the development services.

```bash
npm --prefix frontend ci
make sandbox-image proxy-image auth-broker-image
devenv up
```

Open <http://127.0.0.1:8000>. On first run, look for `ONE-TIME SETUP SECRET` in the
API output, then use it in the setup wizard. The Go API listens on port 8080,
Next.js on 3000, and Postgres on 15432. Use the Caddy URL on port 8000 for the UI,
API requests, and WebSockets. Stop the Compose stack first if you use the same
port.

The devenv setup uses `host.docker.internal` for containers to reach host
services. If your Docker Engine does not provide that address, use Docker
Compose for local runs or see the
[runtime guide](docs/guides/runtime-and-integrations.md#bot-runtime) for service
endpoint settings.

Run these commands inside the devenv shell.

| Command | Purpose |
| --- | --- |
| `make gen` | Regenerate SQL bindings, Protobuf stubs, and OpenAPI types. |
| `make lint` | Check Go and frontend code, types, and formatting. |
| `make test` | Run Go tests with race detection and the frontend tests. |
| `make build` | Build the Go binaries and Next.js frontend. |

Rebuild the affected image when you change sandbox, egress, or broker code.
Restart the affected bot computer after rebuilding the sandbox or egress image.

## Project files and contributing

- [`backend/`](backend/) contains the Go API, sandbox agent, egress proxy, auth
  broker, and database migrations.
- [`frontend/`](frontend/) contains the Next.js and TypeScript app.
- [`images/`](images/) contains Docker build definitions.
- The [technical overview](docs/overview.md), [architecture decisions](docs/adr/),
  and [design guide](design/README.md) describe how bot.Box works.
- Read the [contribution guide](CONTRIBUTING.md), [development rules](AGENTS.md),
  [Code of Conduct](CODE_OF_CONDUCT.md), and [Contributor License Agreement](CLA.md)
  before submitting changes.

## License

bot.Box is licensed under the [GNU Affero General Public License, version 3 only
(AGPL-3.0-only)](LICENSE). Third-party materials keep their own licenses.
