<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0021. Broadcast mentions and global bot roles

[ADR-0027](0027-group-input-requests.md) updates how bots request input.
Human broadcasts and global bot roles follow this ADR.

- **Date:** 2026-11-17
- **Status:** Accepted

## Context

A human can ask the bots in a group to discuss an issue, but the default
`mentioned_else_lead` router initially queues only the lead. A lead reply
without a colleague mention queues nobody because self-routing is excluded.
The group context does not identify the responding bot or the lead explicitly.
Resumed CLI turns previously omitted fresh group context and bot instructions.
This makes role confusion and stalled discussions more likely.

Users need arbitrary global responsibilities such as privacy review or system
architecture, shared across all conversations of a bot. The existing
`bots.description` field already stores free-form role instructions; group
`role_text` is separate membership metadata.

## Options

1. Always schedule every bot after every message. This causes unwanted turns
   and spend even when nobody asks for another contribution.
2. Let a model choose recipients. This adds a model call to routing and makes
   recipient selection nondeterministic.
3. Add explicit `@all` broadcasts and refresh identity and role context on each
   turn. Keep ordinary mention policies and infrastructure limits.

For roles, fixed categories and per-group instructions do not meet the user's
requirement. A new global column would duplicate `bots.description`.

## Decision

Use option 3. Reserve case-insensitive `@all` at word boundaries. Expand it to
the current group roster in roster order, deduplicate recipients, and exclude
the sending bot from queued turns. It overrides round robin; lead-first still
orders the lead before colleagues. Ordinary `@Name` behavior stays intact.
Human members receive mention notifications under their existing preferences.
Routing and notifications remain workspace- and conversation-scoped.

Bot-to-bot disabled, existing pauses, turn limits, loop detection, queue limits,
and budgets still gate broadcasts. This is not an unlimited discussion mode.

Reuse `bots.description` for the global free-form role. Clarify the scope in the
bot settings UI. Send the current bot name, role, reply language, and an explicit
no-self-handoff instruction on every API and CLI turn. Refresh the group context
on resumed CLI turns and continuations, including the roster's global roles and
the current lead. Changes take effect on the next turn. Roles guide model
behavior and never grant capabilities or override infrastructure enforcement.

Render global roles in the team chat's direct-message inspector with the
existing safe CommonMark/GFM message renderer. This preserves formatting without
introducing a second HTML or URL policy. Keep long roles in a preview about four
body lines high and offer More/Less only when rendered content overflows. Raw
text loses structure, while showing all instructions by default can push the
computer links and group list out of view. Expanding the preview changes no
stored instructions or prompt context.

## Consequences

- No new role enum, database column, migration, or permission is required.
- A lead can request every colleague's input with one explicit mention.
- Role edits and colleagues' replies reach existing CLI sessions.
- Refreshing bounded group context adds input tokens on resumed turns.
- Self-routing is blocked in code. Avoiding self-mention text is a model
  instruction; generated prose is not rewritten or guaranteed by the router.
- The existing alternating-speaker loop guard can pause a productive two-bot
  discussion after four turns. A future discussion protocol may distinguish
  progress from repeated handoffs.
