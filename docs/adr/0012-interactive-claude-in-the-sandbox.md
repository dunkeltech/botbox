# 0012. Interactive Claude in the sandbox

- **Date:** 2026-11-15
- **Status:** Accepted

## Context

The pinned Claude CLI is 2.1.323, a native binary installed by the npm package
postinstall. On an interactive first start it connects to `api.anthropic.com`
and `platform.claude.com`, then asks whether the working directory is trusted.
The bot computer only has the egress proxy. ADR-0009 denies CONNECT to provider
hosts, so the connectivity check fails before the stored Claude token is used.
The trust prompt has nobody to answer it. Chat turns avoid both screens because
the agent starts `claude -p` with `ANTHROPIC_BASE_URL` and the matching
placeholder. A person typing `claude` in the computer does not.

## Options

1. Allow CONNECT to `api.anthropic.com` and `console.anthropic.com`. Rejected.
   A tunnel would bypass metering and could carry the real token into the sandbox
   only if the CLI logged in there, which ADR-0011 forbids.
2. Answer the two probe URLs inside the proxy and let the CLI log in. Rejected.
   The login would still try to obtain a real token inside the computer.
3. Keep provider tunnels closed. For a desktop launch, select the placeholder
   that matches the stored credential, point `ANTHROPIC_BASE_URL` at the metered
   route, and skip the official probe. Meter the calls without a chat turn.

## Decision

Use option 3. `GET /llm/anthropic/auth-mode` returns `oauth`, `api_key`, or
`none` and never a secret. The sandbox `claude` command uses that answer only
when the agent has not already set a placeholder, so a running turn cannot
change billing mode. It also marks onboarding complete and marks the current
directory trusted, which is what 2.1.323 checks before opening the prompt.
Without `HTTP_PROXY`, the command execs the official native binary, so
`make claude-login` can still run `claude setup-token`. A text stub at the
binary path fails the image build and the launcher.

Desktop requests still pass pause, budget, provider, and auth-mode checks.
Holding the desktop stops new engine turns; it does not block the person's own
CLI, because that is when they can type. Usage is recorded with a null
`turn_id` when no chat turn is running. Subscription rows stay tokens-only.

## Consequences

- Sandbox and proxy images must be rebuilt, and existing computers restarted,
  before `claude` in the desktop picks up the launcher.
- A paused computer still cannot call the provider. Desktop control stops the
  engine and leaves the person's own `claude` on the metered route.
- Opening the desktop does not by itself start a chat turn or show those
  requests in a conversation.
