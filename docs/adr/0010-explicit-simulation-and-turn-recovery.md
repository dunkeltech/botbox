# 0010. Explicit simulation and safe turn recovery

- **Date:** 2026-11-15
- **Status:** Accepted

## Context

The executable silently selected the memory compute driver when its driver
variable was unset or mistyped. A bot configured for Claude CLI could therefore
show a running computer even though no CLI or isolated Linux computer existed.
All execution errors then became `engine.turn_failed`, hiding whether the user
needed to start a computer, configure its proxy, or add a provider credential.

## Options

1. Keep simulation as the default and explain its limitations in documentation.
   This leaves the normal development setup unable to run the product's bots.
2. Automatically fall back to simulation when Docker is unavailable. This makes
   infrastructure failures look like successful computer startup.
3. Default to Docker, require explicit simulation, and persist safe recovery
   categories instead of raw dependency errors.

## Decision

Use option 3. The executable defaults to Docker. `BOTBOX_COMPUTE_DRIVER=memory`
is an explicit development choice; unknown driver names fail startup. The
regular devenv configuration selects Docker and its metered proxy. If no proxy
URL override is configured, the executable derives it from `DATABASE_URL`,
translating loopback database hosts to `host.docker.internal` for Docker Desktop.
An explicit `BOTBOX_EGRESS_DATABASE_URL` is preserved for deployment-specific
routing. Parse failures never include credentials from the URL.

Existing fake environment rows are not relabeled as Docker environments. Their
next CLI turn reports that the computer must be restarted. Restart uses the
existing lifecycle to create a real environment, preserving conversation data.
No failed turn or user message is replayed automatically.

CLI preflight verifies computer state, proxy readiness and the encrypted provider
credential before launching a CLI. It keeps model aliases such as `sonnet`
unchanged. Only allowlisted error categories are persisted and translated by the
UI; private causes remain available for error matching but their `Error` text is
not exposed. Chat recovery links point to computer controls or the bot's Secrets
page. Unknown or malformed error codes retain the generic safe message.

## Consequences

- Docker and the two locally built images are needed for normal bot execution.
- Linux deployments with a host-local database must provide a database URL that
  their bridge-connected proxy can reach; loopback rewriting targets Docker
  Desktop's host endpoint, not a universal networking workaround.
- Provider authentication is still required. This change never imports a user's
  host CLI login or puts provider credentials into the sandbox.
- Historical failed turns keep their original codes. New attempts report the
  current recovery category without changing the previous conversation.
