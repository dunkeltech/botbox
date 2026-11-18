<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0024. Responsibility for sandbox housekeeping

- **Date:** 2026-11-17
- **Status:** Accepted

## Context

Each bot has a persistent computer, but its shared identity prompt does not
assign responsibility for keeping that computer usable. Bots can therefore ask
humans whether to delete their own obsolete helper scripts even after finding a
working replacement. Routine local decisions unnecessarily interrupt users.

## Options

1. Require confirmation for every deletion. This delegates computer maintenance
   to the human and stalls ordinary work.
2. Authorize all sandbox deletions unconditionally. This risks losing
   user-provided files, shared data and uncommitted work.
3. Assign routine housekeeping to the bot with explicit ownership and
   preservation boundaries, refreshed on every turn.

## Decision

Use option 3. Append responsibility guidance to the common identity prompt used
by API and CLI turns. Fresh turns, resumed CLI sessions, groups, direct messages
and secret continuations receive the same instruction independently of the bot's
configured global role or authentication broker availability.

The bot maintains its own tools and files without asking about routine local
decisions. It may remove its own obsolete scripts, stale copies and caches after
checking active references and ongoing work. When ownership or future use is
uncertain, prefer recoverable cleanup. Ask only if local inspection cannot
resolve that uncertainty and valuable work is at risk. Deleting user-provided
files, shared data or uncommitted work requires authorization.

Local housekeeping grants no authority for external actions such as messaging,
publishing, shared-system changes or costs. Those actions follow the user's
authorization. Secret handling, workspace boundaries, network policy, budgets
and human desktop control remain enforced by existing infrastructure.

## Consequences

- Humans no longer need to decide whether a bot keeps its obsolete helper files.
- Existing sessions receive the policy on their next turn without resetting
  their conversation or changing stored roles.
- This is model guidance, not a filesystem enforcement mechanism or a guarantee
  that generated replies will never contain unnecessary questions.
- Deterministic prompt regression tests cover delivery and preservation
  instructions without calling real LLM APIs.
