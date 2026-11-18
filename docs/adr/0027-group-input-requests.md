<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0027. Group input requests

**Status** Accepted

## Context

Group answer policies applied to every bot reply. An untagged final draft could
activate the lead and a reviewer's mention could wake the author before other
reviews arrived. Concurrent replies made duplicate turns and crossed feedback
common.

## Options considered

- Treat every mention as a request and wait for its targets. References to
  earlier contributions could activate bots and open accidental wait cycles.
- Keep the author turn open until peer responses arrive. A waiting turn would
  occupy a worker slot and block other conversations for that bot.
- Record explicit requests in the group scheduler and resume the author in a
  fresh turn. This gives both API and CLI bots the same lifecycle.

## Decision

Apply answer policies to human posts. A bot can request input by putting the
question in its reply and ending with a control line such as
`Request input: ["Babu Frik", "K-2SO"]`. `["@all"]` selects the other bots in
groups with up to eight eligible recipients. Use `Request required input:`
to surface missing required input to the human as a blocker. Validate targets against the
current group roster, de-duplicate them, and strip the control line from the
visible reply. Human mention notifications from the visible message still
follow the existing preferences.

Persist the fan-out and its recipient turn IDs in the same transaction as
queue admission. Allow one open request per requester and group. Recipients
answer independently and their turns contribute at most one reply to the
round. A recipient turn cannot start a nested round. The requester may ask a
targeted follow-up after collection.

The requester releases its worker slot. A scheduler pass finds rounds whose
recipients finished or whose five-minute deadline elapsed. Under the group
lock, it queues exactly one continuation with a bounded excerpt per reply and
a list of gaps. The original replies remain in the transcript. An enqueue
failure rolls back collection and can be retried. Group budgets, pauses,
turn limits and loop detection still govern bot work.

## Consequences

- Review, planning, and open-ended discussion use the same input request.
- The group transcript exposes progress to current members, including readers.
- A late reply can appear in chat after collection. Queued recipient turns
  cannot start after their round closes or expires.
- Bot-to-bot being disabled stops active rounds. A group paused for budget or
  approval waits for human action before the continuation can be queued.
- Migration 23 upgrades early development databases that recorded version 22
  before the required-input and continuation columns were present. It keeps
  existing requests and queued turns.
