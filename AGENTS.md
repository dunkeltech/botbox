# AGENTS.md

Guidance for coding agents and humans working on this repository.

## Project

bot.Box is a multi-user web app for bot computer agents. Each bot is an LLM plus its own sandboxed Linux desktop; bots work alone or together with humans in a Slack-style team chat.

- **Spec:** [`Application.md`](Application.md). Keep it in sync when behaviour or architecture changes.
- **Roadmap:** GitHub issues, one per phase ([#2](https://github.com/dunkeltech/botbox/issues/2)–[#10](https://github.com/dunkeltech/botbox/issues/10), design hub [#1](https://github.com/dunkeltech/botbox/issues/1)): https://github.com/dunkeltech/botbox/issues
- **Design:** [`design/STYLEGUIDE.md`](design/STYLEGUIDE.md), [`design/tokens.json`](design/tokens.json), screens in [`design/README.md`](design/README.md)
- **Decisions:** ADRs in `docs/adr/`

## Rules

### 1. Tests are part of every change
- Every change comes with tests. A change without tests is not done.
- **Write negative tests, not only the happy path.** For each feature, cover at least:
  - invalid and missing input, wrong types, values out of range
  - unauthenticated (401) and forbidden (403) requests, for every role in the permission matrix
  - **cross-workspace access** (must return 404)
  - error paths of dependencies (database, NATS, Docker, agent, LLM provider down or slow), timeouts and cancellation
  - limits: rate limits, turn limits, budgets, quotas, size limits
  - security invariants: no secret value in responses, logs or the sandbox; blocked hosts stay blocked
- A bug fix starts with a test that fails without the fix.
- **Never call real LLM APIs in tests.** Use the fake provider, the fake CLI and the recorded stream fixtures.
- Tests must be deterministic: no sleeps for synchronisation, no reliance on wall-clock time or test order.

### 2. Comments: simple technical English, explain *why*
- Write comments in short, simple technical English.
- **Document the why, not the what.** Explain intent, constraints, trade-offs, security reasons and links to ADRs or issues. Do not restate what the code obviously does.

  ```go
  // Bad: increment the counter
  turns++

  // Good: the counter resets on every human message, so bots can never
  // run more than turnLimit turns without a human in the loop (see #8).
  turns++
  ```
- Exported Go identifiers and exported TypeScript functions get a doc comment that states their purpose and any non-obvious contract.
- No commented-out code. Remove it; git keeps history.
- Explain workarounds with a link to the upstream issue and the condition for removing them.

### 3. Commits: one line, Conventional Commits, English
- Format: `<type>(<optional scope>): <summary>`, written as **one single line**, in English, imperative mood, lower case after the colon, no trailing period, at most 72 characters.
- **No body and no trailers.** In particular, **no `Co-Authored-By:` line**.
- Types: `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `build`, `ci`, `chore`, `style`, `revert`. Add `!` for breaking changes (`feat(api)!: …`).
- Typical scopes: `api`, `auth`, `engine`, `agent`, `proxy`, `secrets`, `compute`, `chat`, `orchestrator`, `budgets`, `frontend`, `ui`, `sandbox`, `devenv`, `ci`, `design`, `docs`.
- Examples:
  ```
  feat(auth): rotate refresh tokens and revoke family on reuse
  fix(proxy): strip placeholder authorization header before forwarding
  test(api): add cross-workspace access tests for bots endpoints
  docs(adr): record agy authentication decision
  ```
- Reference issues in the PR, not in the commit line ("Closes #3").

### 4. Document the why
- Architectural or security-relevant decisions go into an ADR (`docs/adr/NNNN-title.md`) with context, options, decision and consequences.
- PR descriptions explain **why** the change is needed and which alternatives were rejected, not only what changed.
- When behaviour changes, update `Application.md`, the ADRs and `design/STYLEGUIDE.md` in the same PR.

### 5. English everywhere
Code, identifiers, comments, commit messages, PRs, ADRs and docs are written in English. UI copy is English and goes through `next-intl`; never hard-code user-facing strings.

### 6. All dependencies come from devenv
- Toolchains, CLIs, services and code generators are declared in `devenv.nix`.
- Do not install anything with `brew`, `apt`, global `npm i -g` or `go install` outside devenv. If a tool is missing, add it to `devenv.nix`.
- The only host requirement is a Docker engine for the bot sandboxes.

### 7. Security invariants (never break these)
- Secrets never enter a sandbox (environment, files, process arguments), a prompt, a log line, an error message or an API response. Secret values are write-only.
- Sandboxes are never privileged, never get the Docker socket, and have no network route except their egress proxy.
- Every database query and every real-time channel is scoped by `workspace_id`.
- Risky bot actions are enforced in infrastructure (proxy, budgets, token rights), not only by prompting.

### 8. UI
- Follow `design/STYLEGUIDE.md` and use the tokens from `design/tokens.json`. Do not invent new colours or radii.
- Accessibility: use real buttons, links and labels; text contrast ≥ 4.5:1; 44 px touch targets; visible focus; never colour alone for status.

## Development

```bash
direnv allow            # or: devenv shell
devenv up               # Postgres, NATS, API, frontend, Caddy
make gen                # sqlc, buf, OpenAPI clients
make lint
make test
```

The Makefile targets and the repository layout below are set up in Phase 0 ([#2](https://github.com/dunkeltech/botbox/issues/2)). Until then, follow the issue.

```
backend/            Go module: cmd/api, cmd/agent, cmd/egress-proxy, internal/…, db/, proto/
frontend/           Next.js app (App Router, Tailwind, next-intl)
images/sandbox/     bot computer image
deploy/             Helm chart
docs/adr/           architecture decision records
design/             styleguide, tokens, screens, canvas sources
```

## Code conventions

**Go**
- `gofmt` and `golangci-lint` must pass.
- `context.Context` is the first parameter. Wrap errors with `%w` and add context.
- No `panic` in request paths. Use `slog` with structured fields.
- SQL lives in `db/queries` (sqlc); migrations go in `db/migrations` (goose, forward-only).
- Tests are table-driven. Integration tests use testcontainers (Postgres, NATS, Docker).

**TypeScript**
- `strict` mode; ESLint and Prettier must pass.
- Server components by default. The API types are generated from the OpenAPI spec and never written by hand.
- Unit tests use Vitest and Testing Library; end-to-end tests use Playwright.

**General**
- Keep changes small and focused on one issue.
- Run `make lint test` before every commit. CI must be green before merging.
