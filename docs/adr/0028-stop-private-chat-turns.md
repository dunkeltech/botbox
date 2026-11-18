<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# ADR-0028 · Stop running turns from chat

## Context

Bot managers can pause an entire bot. A person talking to a shared bot also
needs a way to stop active work in a direct message or group.

## Options

- Use the bot-wide pause API in chat. Its permissions and effect span the whole
  bot.
- Publish a transient control message for the turn. The signal can arrive
  before the runner registers cancellation or while it is disconnected.
- Persist a stop request on the identified turn and let the worker cancel it.

## Decision

Use a persisted stop request scoped to the workspace, conversation, and running
turn. The direct-message owner and non-viewer group members can make the request.
The worker checks it before execution and
at 500 ms intervals during execution. It cancels through the existing runner
context. Keep the running slot until the runner returns and cleanup finishes
so the next turn cannot share the same computer with the stopping turn.

The stop request and the transition to waiting for a secret compete on the same
database row. If the request wins, creation of the secret request rolls back.
If the secret wait wins, the turn is no longer running. A repeated stop against a
turn that has already left the running state succeeds without changing it.

Group reply finalization locks the group and turn before recording reply
approvals, sending mentions, or planning handoffs. A stop that obtains the turn
lock first finalizes the turn as interrupted and skips those effects. A stop
that follows a successful finalization sees a terminal turn. The reply stays
in the transcript. A savepoint rolls back a failed handoff separately from
reply notifications and approvals.

Reuse the existing bot-wide pause control in bot settings for bot managers.

## Consequences

The chat action can work through a queue outage because the worker reads the
stop request from PostgreSQL. Each active worker adds a bounded periodic read.
The chat reports the pending request from persisted history and a stopped turn
keeps the interrupted status. Queued work retains its order.
