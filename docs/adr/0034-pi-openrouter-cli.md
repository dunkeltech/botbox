<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Run OpenRouter bots with Pi inside the sandbox

## Context

The Model API runner executed the provider tool loop in the control plane.
The existing CLI runners give their tool loops to the bot's sandbox. OpenRouter
users need that same sandbox execution with workspace-managed API credentials.
There were no Model API bots when this change was adopted.

## Options

- Continue the control-plane tool loop for OpenRouter. This keeps a separate
  tool implementation and execution path.
- Run Pi with the workspace API key in its configuration. This exposes the key
  to sandbox processes.
- Run Pi inside the sandbox and point its OpenRouter client at the bot's existing
  metered proxy. This reuses the CLI and credential boundaries.

## Decision

Use Pi for OpenRouter. Claude Code, Codex, and agy remain the mappings for
Anthropic, OpenAI, and Google Antigravity. Keep the existing provider field and
the `cli` execution-mode value in the public API. Remove the control-plane model
runner and reject the retired `api` mode on create or update.

Start one Pi process per turn in JSON mode using stdin for the prompt. One-shot
JSON mode fits the existing server-streaming agent RPC and process-group
cancellation. Persist Pi session IDs per conversation and store session files
in the sandbox home. A turn completes when Pi reports that its run settled.

Give managed Pi runs an isolated configuration directory. Generate the
OpenRouter base URL and MCP endpoints there, use placeholder authorization,
disable automatic network activity, and load the built-in MCP extension for
desktop, team, knowledge base, and assigned workspace tools. The proxy resolves
the encrypted bot or workspace credential outside the sandbox. Proxy admission
checks the configured provider, exact OpenRouter model ID, and budgets before
forwarding Chat Completions or Messages. Both routes settle provider usage for
each request.

## Consequences

The sandbox image pins Pi. Existing desktops need the updated image to run Pi.
Pi uses the same time, output, egress, and budget controls as the other managed
CLI runners. The runtime uses Pi's bundled OpenRouter model catalog. The image
size check allows 2.60 GB to accommodate the Pi runtime dependencies. The
Docker contract test runs Pi against loopback fixtures for OpenRouter and a
desktop MCP server to check streaming, tool results with images, and session
resume.

- [Pi CLI integration](https://docs.pi.dev/latest/cli-integration)
- [Pi model configuration](https://docs.pi.dev/latest/models-configuration)
