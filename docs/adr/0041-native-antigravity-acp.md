<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0041. Run Antigravity ACP with native sign-in and inspected model egress

- **Date:** 2026-11-20
- **Status:** Accepted

## Context

The managed Gemini API route in [ADR-0040](0040-agy-gemini-api-key-route.md)
uses an API key held by the egress worker. Google also documents an
[ACP server](https://antigravity.google/docs/ide/extensions/acp/) that
keeps sign-in in the official CLI. Workspace policies must still apply
when the CLI authenticates directly.

## Options

1. Run ACP in the desktop container. The session would share a filesystem
   and process boundary with agent tools and desktop users.
2. Give the authenticated process a general HTTPS tunnel. Egress rules
   would control destinations, but content and usage checks would have
   no access to model requests.
3. Provision a dedicated ACP runtime with a separate egress pair. This
   preserves the CLI's native sign-in and lets the worker inspect model
   traffic at the network boundary.

## Decision

- The `google-acp` bot provider selects the official `agy acp` stdio
  server. The API drives protocol version 1 over Docker exec. The
  operator signs in from a Docker host terminal with the CLI's own
  `agy acp --login` command.
- The Docker runtime is unprivileged. It has its own named home volume
  and internal network, with no desktop mounts or published ports.
  bot.Box registers a fixed stdio MCP helper when creating or loading
  the ACP session. A private Unix socket and a Docker exec stream carry
  its requests back to the API. The API binds each request to the active
  workspace, bot, turn, and work environment before calling the desktop
  agent. The ACP client approves only validated work computer calls
  once during the active prompt. The two containers have no general-purpose
  network connection.
- The proxy keeps the exact model function declarations generated for
  the work computer MCP server and validates tool names and argument
  shapes before returning model calls. Built-in local executors remain
  disabled in the native runtime. The regular desktop agent executes
  the supported file, terminal, browser, and desktop tools. Terminal
  commands use cancellable streams owned by the active turn and the
  same control gate as desktop actions.
- The dedicated relay and egress worker use the same workspace and
  bot identity as the desktop pair. The ACP runner mounts only the
  worker's public CA. The worker confirms the runner selection in the
  database, applies deny-first host rules, and admits only the following
  native destinations on port 443.
  - `oauth2.googleapis.com` and `www.googleapis.com` use TLS passthrough
    with an exact SNI check. The official browser sign-in happens
    outside the container.
  - `daily-cloudcode-pa.googleapis.com` and `cloudcode-pa.googleapis.com`
    use TLS inspection. Only the observed account and model endpoints
    are admitted. Upstream certificate verification stays enabled.
- The worker forwards the CLI's Bearer header using its original value.
  Guardrail evaluation receives JSON bodies, and provider errors use
  fixed messages. Requests and responses that echo the token are denied.
  Model requests require normal budget admission and subscription
  settlement. The worker buffers bounded responses to detect content
  split across frames before passing it to the CLI.
- The worker refuses unknown destinations, paths, and response formats.
  It has no TLS passthrough path for the approved model hosts. The
  ordinary desktop worker retains its provider CONNECT restrictions.

## Consequences

- Native OAuth credentials are stored by the CLI in the dedicated runtime
  volume. The operator can reconnect or revoke the Google session using
  the provider's tools. A volume that persists after container recreation
  must be handled as credential-bearing data.
- The work desktop retains its own process, filesystem, and network
  boundary. The MCP tool bridge grants access only to that work desktop.
  The desktop agent version must include the streaming terminal RPC.
- The Cloud Code wire format is specific to the pinned CLI. Keep the
  allowlists and synthetic fixtures aligned when bumping the release.
  Deployment and shared use depend on the applicable provider terms.
