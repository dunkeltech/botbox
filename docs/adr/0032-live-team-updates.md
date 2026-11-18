<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0032. Live team updates during a bot turn

**Status** Accepted

## Context

Group input rounds have a five-minute deadline. A bot can keep working beyond
that deadline and give its normal response later. In the meantime the requester
needs to distinguish pending work from a missing result. The working bot also
needs to report a milestone or blocker while it still owns the execution slot.

## Options considered

- Queue a normal reply turn for a status question. This waits behind the same
  ongoing work that the requester is asking about.
- Stream provider text directly to chat. Text chunks can be incomplete and
  providers report intermediate messages differently.
- Use explicit tools for runtime status and bot-authored updates. Both can
  complete independently of the target bot's execution slot.

## Decision

Expose `get_team_status` and `post_team_progress` in model API group turns and
via the private egress MCP endpoint for CLI group turns. The API binds its tool
execution to the worker's session. The CLI endpoint derives workspace and bot
identity from the dedicated proxy and checks the call's turn ID against both.
The current group roster, running turn, stop state and worker heartbeat must
allow the operation. Status checks also require bot-to-bot to be enabled.

The worker refreshes a database heartbeat every ten seconds while the group
execution is active. A heartbeat older than 45 seconds, or a running row from
before the migration with no heartbeat, yields an unknown state. Status reads
are scoped to the group and include only the selected turn's latest update.
Missing input in a collected round includes the observed status so a requester
can tell the human that work is pending and avoid duplicate assignments.

Write updates to the transcript before signaling readers. Status reads may
refresh their result without posting again within 30 seconds for the same peer,
and publish up to twelve status entries per requesting turn. Each working turn
may publish four routine updates a minute apart and up to three distinct
blockers immediately. Progress text is capped at 600 characters. Status and
progress go through the group member history API and are excluded from normal
reply routing and the tool trace. Signals carry only conversation and notification
IDs, with transcript polling recovering missed signals.

## Consequences

- Progress text can appear while the bot continues using its existing CLI or
  model session. Other bots can read it in future turns or ask for current state.
- Status is a timestamped observation. A current worker heartbeat confirms that
  a turn is active even during silent tool use. Milestone text describes task progress.
- Both tools share the existing turn and its model and execution budgets. Their
  messages are written directly to the transcript.
- CLI support requires the updated sandbox agent and egress proxy images.
