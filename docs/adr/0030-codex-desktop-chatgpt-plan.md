<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0030. ChatGPT plan access for interactive Codex

- **Date** — 2026-11-18
- **Status** — Accepted

## Context

People already run Claude Code in the terminal of a bot computer. The same
workflow should support Codex using a ChatGPT plan. A connection uses a person's
plan allowance, and bot.Box runs shared computers whose processes can inspect
their own environment and files.

## Options

1. Use Codex's native sign-in inside the container. That exposes the signed-in
   user's credentials to processes in the bot computer.
2. Embed Codex app-server in the chat engine. This adds a runtime integration for
   chat turns, while the requested workflow uses the interactive terminal.
3. Connect a ChatGPT plan in the control plane and configure the official
   interactive CLI to use the bot's existing metered proxy.

## Decision

Use the third option. A bot manager starts a short-lived, one-use pairing in
**Secrets & network**. The workstation helper opens the system browser with the
documented loopback OAuth flow. The control plane redeems the code, checks the
signed identity and plan permission, and seals the token set using the existing
envelope key ring. A connection is bound to a workspace, bot, and connecting
user. The service serializes refreshes per connection and removes local access
before asking OpenAI to revoke it.

The sandbox launcher starts the official Codex CLI with an internal Responses
provider and a public placeholder. The per-bot egress proxy inserts the actual
OAuth bearer token. Docker supplies the filesystem and network isolation because
the container disallows the nested sandbox's user namespaces. Codex keeps its
interactive approval policy. A request needs the connecting user to hold desktop control.
The route checks pause, membership, explicit host blocks, and budgets before
forwarding. It uses the same request handler and SSE meter as other model routes.
This keeps future proxy guardrails on the common path. Desktop requests are
recorded against the bot and workspace with a null chat turn ID.

The workspace can opt to apply model prices to Claude and Codex subscription
usage. Store the resulting internal charge apart from provider cost and count
both in budget commitments. Capture the request's charge policy and rates when
admitting it. The documented Codex plan route lacks an output cap per request,
so hold the workspace spending lock for a budgeted request through settlement.
An admitted request can cross the limit. The next request checks the updated
commitment. Preserve charges requiring reconciliation across process failures.

## Consequences

- The connected user takes desktop control before using the plan. Handback or
  disconnection denies subsequent model requests on that connection.
- The bot computer retains local Codex sessions. The control plane holds the
  renewable authorization. Self-hosted deployments need an eligible ChatGPT
  account and must follow the documented OpenAI preview scope.
- Bot and workspace budget views show tokens and internal charges. Group
  budgets use chat-attributed usage.
- The developer setup includes `make codex-login` and an interactive sandbox
  contract test with a local fake provider.

[Setup guide](../guides/codex-desktop-setup.md) · [OpenAI plan usage](https://developers.openai.com/siwc/token-sharing-open-source) · [OpenAI preview limitations](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations)
