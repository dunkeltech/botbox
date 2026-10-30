# 0026. Bounded group context and visible turn failures

- **Date:** 2026-11-17
- **Status:** Accepted

## Context

A group follow-up could fail immediately while the same bot answered direct
messages. Mentions and scheduling were working. Group context copied the last
50 messages and every member's global role into each CLI prompt without a byte
budget. A group with long planning replies and four substantial bot roles
exceeded the sandbox agent's 64 KiB input limit. The agent rejected the RPC
before starting the CLI. Group chat did not display failed turn codes, making
the failure appear to be a missing mention.

## Options

1. Increase the sandbox input limit. This only postpones failure as history and
   the roster grow, and weakens an existing resource boundary.
2. Remove current messages or bot instructions to fit. This can lose the actual
   question or the responding bot's role.
3. Bound the group brief, prioritize recent context, retain the current request
   and identity, and display safe turn failures.

## Decision

Use option 3. The group brief has a 48 KiB UTF-8 byte budget, leaving space for
the complete accepted 8 KiB human message, the responding bot's 4 KiB role,
identity and desktop guidance within the agent's unchanged 64 KiB limit.
Tests cover the largest legal message and bot fields, fresh and resumed CLI
turns, and long multibyte history.

The brief puts group goal and routing/approval rules before the roster. Group
metadata uses at most half the brief; unusually large rosters are shortened.
The responding bot's full role remains in its separate identity prompt. Recent
messages are selected newest first and emitted in chronological order. Old
messages are omitted before newer messages. If the newest reply alone is too
large, retain its ending and speaker so a concluding question is still visible.
The older extractive summary uses remaining capacity. An explicit marker says
when context was shortened. Handoff guidance is always retained.

This bounds the transient prompt only. Stored messages, extractive summaries,
CLI session files, and conversation authorization are unchanged. Infrastructure
continues to enforce approvals, workspace isolation, and limits independently
of any shortened prompt metadata.

Group chat displays each bot's latest failed, interrupted, or budget-exhausted
turn above the composer. Newer work supersedes an old warning. Group and direct
chat share an allowlisted mapping from failure codes to translated recovery
copy, never raw dependency text. The warning identifies the affected bot and
uses existing amber tokens.

## Consequences

- Long groups can continue replying through mentions without increasing the
  agent's resource limit or truncating the human's current message.
- An older passage can fall out of the prompt; it remains in stored history and,
  for resumed CLI turns, the CLI's existing session history.
- Failures are visible even when no tool event or reply was produced.
- Regression and browser tests use fake providers and mocked routes. Existing
  failed turns are not replayed automatically; replaying arbitrary bot work
  could repeat external side effects.
