<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Runtime and integrations

See the [README](../../README.md) to start bot.Box with Docker Compose or devenv.

## Bot runtime

Set `NATS_URL` to enable durable DM scheduling. The executable and devenv use
Docker by default. `BOTBOX_COMPUTE_DRIVER=memory` explicitly selects the development
simulator, which cannot run CLI bots and is never exposed to model tools. Unknown
driver names fail startup. Build the images with
`make sandbox-image proxy-image auth-broker-image`.

The Docker engine must support isolated bridge gateway mode. When upgrading
from the legacy egress proxy, build all three images before restarting the API.
Startup removes legacy proxies and stops affected computers. Start each computer
to provision its relay and trusted worker. The home volume is reused. The old
container is retained stopped under a `botbox-retired-` name for recovering
files outside the home volume. See [ADR-0038](../adr/0038-split-egress-and-chromium-isolation.md).

Every bot computer uses its own internal Docker network, and native ACP bots use
a second one. Each takes a /24 from the Docker address pools: OrbStack has 30,
shared with every other project on the host. Deleting a computer removes its
networks. When the pool is full, starting a computer first removes bot networks
that no container uses. If that frees nothing, the start fails with
`environment.network_capacity`. Then remove unused networks of other projects
(`docker network ls`, `docker network rm <name>`) or widen the pools with
`default-address-pools` in the Docker daemon configuration. See
[ADR-0042](../adr/0042-bot-network-lifecycle.md).

Configure provider credentials through the write-only Secrets form. The secret's
domain must match the provider host, and any path restriction still applies.
Add the exact configured model under **Workspace settings → Budgets & model prices**.
The bot editor offers the workspace's priced models for the selected provider.
The bot computer requires a matching provider and model price to start or
restart. Saving an unpriced model stops a running computer and releases desktop
control. The API startup check also stops existing computers with unpriced
models. After adding the price, start the computer again. Owners and admins can
edit budgets in the fleet or bot settings. Private DMs are available in the bot
workspace and under `/team`.

The proxy database URL defaults to `DATABASE_URL`, with loopback hosts translated
to `host.docker.internal` for Docker Desktop. The devenv configuration sets this
explicitly. Override `BOTBOX_EGRESS_DATABASE_URL` when the proxy needs a different
Postgres endpoint, especially on Linux or remote Docker deployments.
`BOTBOX_EGRESS_IMAGE` defaults to `botbox/egress-proxy:dev`. The API provisions the
per-bot proxy with its encryption key configuration; do not put database or
provider credentials in the sandbox environment. Existing unproxied sandboxes
must be recreated, retaining their named home volume.

Each CLI request is metered before the next one is admitted. Missing usage blocks
applicable monetary budgets until reconciled. Under **Workspace settings →
Budgets & model prices → Unresolved subscription usage**, owners and admins can
record the input, output, and cached-input tokens from a provider report or
identify the adjustment as an operator estimate. bot.Box applies the matching
workspace rate and audits the adjustment. CLI aggregates do not double charge
proxy-metered requests. See ADR-0008, ADR-0009, and ADR-0039.

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
See ADR-0011.

Build the updated images with `make sandbox-image proxy-image`, then start the
bot computer. `make test-sandbox-cli` verifies the actual pinned CLI and Sonnet
alias against a local fake provider with networking disabled.

### ChatGPT plan for Codex

Follow the [Codex desktop setup guide](codex-desktop-setup.md) to
connect your ChatGPT plan, store the synchronized `llm-openai` OAuth secret at
bot or workspace scope, run Codex in chat or the bot computer terminal, and
review its token usage. Both chat turns (`codex exec`) and interactive desktop
sessions route through `/llm/openai/v1` with subscription metering.
[ADR-0030](../adr/0030-codex-desktop-chatgpt-plan.md)
describes the metered route and the subscription budget policy.

The proxy can recognize a successful ChatGPT plan event stream when the
upstream response omits its content type. Usage still requires the provider's
token report. A complete report in the terminal event remains valid if the
connection closes before EOF. Proxy logs include the upstream status and response format
category for failures. Those logs exclude response bodies and credentials.

The [sandbox Dockerfile](../../images/sandbox/Dockerfile) pins the Codex CLI
version. Rebuild the image with `make sandbox-image` and verify the CLI with
`make test-sandbox-codex`. An existing computer keeps its original image until
it is recreated. Its named home volume can be reused.

### Google Gemini with agy

Choose **agy · Google Antigravity** for the pinned CLI with a Gemini API
key (ADR-0040). To set one up:

1. Create a Gemini API key on a Google Cloud project with billing enabled.
   Unpaid keys let Google use prompts to improve its products, and users in
   the EEA, Switzerland and the UK may only use paid keys.
   - **Google AI Pro or Ultra.** These plans include monthly API credits:
     10, 40 or 100 USD. Set up prepaid billing in AI Studio with at least
     5 USD, then apply the credits on the Google Developer Program benefits
     page. Keys of that billing account use the credits first.
   - **Who may use the key.** Only the subscriber may use these credits, so
     store such a key at bot scope on that person's own bot.
   - **Sign-in quota.** This route uses Gemini API billing. See the ACP
     setup below for an account that permits native Antigravity use.
2. Store it in **Secrets & network** as `llm-google`, at bot or workspace
   scope, with the domain `generativelanguage.googleapis.com`.
3. Set the bot model to a Gemini API ID such as `gemini-3.8-flash`, not a
   display name.
4. Under **Workspace settings → Budgets & model prices**, price that model
   and every other Gemini model agy may call. agy's subagents can use other
   models than the bot model, and a budgeted request for an unpriced model is
   refused.

The agent gives `agy` only the placeholder `GEMINI_API_KEY=botbox-placeholder`
and points `GOOGLE_GEMINI_BASE_URL` at the metered `/llm/google` route. The
proxy sets the real key and meters each request from its `usageMetadata`.
After a guardrail blocks a Gemini response, the proxy refuses agy's automatic
retry, so the turn ends after one upstream call. `make test-sandbox-agy` runs
the pinned binary against a local Gemini fixture with networking disabled.

### Antigravity ACP with Google sign-in

Choose **agy · ACP with Google sign-in** and enter a model ID available to
the account. Add a workspace model price under the **Google** provider
for the exact ID before starting the computer. Check the terms of the
account's subscription or license
before granting other workspace members or automated tasks access to it.
The official ACP client contract covers native sign-in. Deployment terms
depend on the account and how its session is used.

1. Build and deploy the updated API and egress proxy image. The development
   image target is `make proxy-image`. Use a sandbox image containing the
   pinned `agy` CLI.
2. Start the bot computer and send a chat message. This creates the
   separate ACP runtime. Its first turn reports that sign-in is required.
3. On the Docker host, run `docker exec -it botbox-agy-acp-<bot-id> agy acp --login`.
   Open the displayed URL in your browser and enter the resulting code in
   that terminal. Bot settings show the command with the bot's ID.
4. Retry the chat message. The CLI retains its own session in its private
   Docker volume. Use the same sign-in command to reconnect after expiry.

ACP chat supports text only. The authenticated runtime has no access to
the bot desktop's files or tools. Model replies enter the chat after the
proxy validates the complete bounded response. The proxy records usage
under the Google provider with subscription accounting. Explicit host
blocks in **Secrets & network** also apply to this runtime.

See [ADR-0041](../adr/0041-native-antigravity-acp.md) and the
[Antigravity ACP guide](https://antigravity.google/docs/ide/extensions/acp/).

### Deterministic engine doubles

`backend/internal/engine/enginetest` provides a scripted HTTP provider and a fake
CLI turn executor. Provider responses contain ordered chunks with optional channel
gates, request validation and dependency status codes. Use `httptest.NewServer`
and assert `Remaining()` and `Errors()` after the test. CLI doubles replay the
recorded streams through the production live parsers and support gates, stderr
output, soft denials and nonzero exits. Gates synchronize streaming/cancellation tests
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

The development broker runs in Docker with the Chromium Seccomp profile.
It publishes the HTTP API on host loopback and keeps DevTools private inside
the container. `devenv` generates a per-project broker token under `DEVENV_STATE`.
If the Docker engine cannot reach host loopback, configure a private broker
interface reachable by the API and the trusted egress workers. Run
`make test-auth-broker-sandbox` to verify the Chromium sandbox on this engine.

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
[ADR-0019](../adr/0019-live-public-gitlab-and-bot-assignment.md) and
[ADR-0020](../adr/0020-self-hosted-gitlab-cli-grants.md).

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
