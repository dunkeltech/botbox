# 0015. Group chat orchestration

- **Date:** 2026-11-16
- **Status:** Accepted

## Context

Phase 6 adds Slack-style groups where humans and bots share a conversation.
Phase 4 already stores a direct message as one human plus one bot.
`bot_turns` and `bot_sessions` reference `conversations(workspace_id, id, bot_id)`,
so a conversation can name only one bot. `approvals` accepts only `raise_budget`
and requires both `bot_id` and `budget_id`. The per-bot JetStream queue, pause,
and workspace or bot budget approvals must keep working.

Groups need several humans, several bots, a turn limit, a loop guard, and
approval kinds that are not budgets. Issue #8 and the sub-issues #11–#20 are
the scope. Threads, reactions, file sharing, and external chat systems are not.

## Options Considered

### Option A — A second table for groups

- **Pros:** Direct-message rows stay unchanged.
- **Cons:** Messages, turns, sessions, and the WebSocket path fork. A bot that
  is in a direct message and a group no longer shares one queue shape.

### Option B — Widen `conversations` and keep one turn queue

- **Pros:** One message log, one session per `(bot, conversation)`, and the
  existing per-bot queue. Direct messages stay a constrained row shape.
- **Cons:** The foreign key that pins a turn to the conversation's single bot
  has to be replaced. Authorization moves from `conversations.user_id` to
  membership for groups.

### Option C — Let the model decide when to stop and who may approve

- **Pros:** Less infrastructure.
- **Cons:** Breaks the rule that risky actions are enforced outside the prompt.
  A loop or a new host would depend on the model cooperating.

## Decision

Use option B, with option C rejected.

Direct messages stay `type = dm` with `user_id` and `bot_id` set. Only that
human can read the row. Groups are `type = group` with both columns null.
Membership is the only read path. A workspace admin who is not a member gets
**404**, the same as a stranger.

Drop the foreign key from `bot_turns` and `bot_sessions` to
`conversations(workspace_id, id, bot_id)`. Point turns at
`conversations(workspace_id, id)` and `bots(workspace_id, id)`. A database
trigger rejects a direct-message turn whose bot is not the conversation bot,
and a group turn whose bot is not a member. One CLI session per
`(bot, conversation)` stays. Group instructions are prepended to that turn.
They are not a second session.

Answer policy is `mentioned_else_lead`, `lead_first`, or `round_robin`.

- `mentioned_else_lead` queues mentioned member bots in mention order. If none
  were mentioned, it queues the lead when that bot is a member.
- `lead_first` queues the lead first, then every other mentioned member bot
  once. If nobody was mentioned, only the lead is queued.
- `round_robin` queues the next bot in stable member order. Mentions do not
  add extra bots.

A bot is never queued to answer itself in the same step. `@Name` matches a
member's display name, case-insensitive, longest name first. Unknown names are
ignored. Human mentions create notifications and do not create turns.

`bot_talk = false` means a bot reply queues nobody. A human message still
routes. Direct messages do not use this router. They keep queueing their one
bot. New and existing direct messages store `bot_talk = false`.

`turns_since_human` increments once per group bot turn that is claimed. A
human chat message sets it back to 0 and clears every pause. At `turn_limit`
(1..100, default 20) the group pauses and posts
`Turn <limit> of <limit> · paused, waiting for you`. No further bot turn is
queued while that pause holds.

Loop guard, with no model call: if the last four bot speakers are A, B, A, B
and the next speaker would be A, pause with reason `loop`.

Pause reasons are `turn_limit`, `loop`, `approval`, and `budget`. Approving an
ask-before card clears only an `approval` pause, queues the waiting bot, and
does not reset the counter. Denying posts a system message and leaves the
group paused. An approval does not clear a turn-limit pause.

The conversation budget is one optional lifetime cap in microdollars. `NULL`
means no cap. Spend is the sum of `usage_records.cost_micros` joined through
`bot_turns` for that conversation. Desktop CLI usage with a null `turn_id`
does not count. Unknown cost is not zero. At 80% post one budget warning. At
100% pause with reason `budget` and create one `raise_budget` approval for
that conversation. Workspace and bot budgets still apply.

The rolling summary does not call a model. Keep the last 50 text messages.
Older text is copied into `conversations.summary`, capped at 8 KiB.

`ask_before` is a JSON array of objects
`{ "kind": "new_host" | "raise_budget" | "push_protected" | "custom", "label": "..." }`.
`label` is required for `custom`. An empty array is valid.

Who may approve: a human member who is not a viewer, or a workspace owner or
admin. Viewer members may read and may not post, change settings, or approve.
Every decision is an `audit_log` row with the actor, the decision, and the
approval id. Secret values are not stored.

`new_host` stays enforced by the egress allowlist. Approval inserts that host
and no other. Denial leaves the list unchanged.

`push_protected` stores a card and does not push. Protected branches stay on
the git host. The bot setup copy says so.

`raise_budget` for a bot or workspace stays on the Phase 4 path (`budget_id`
set, `conversation_id` null). A group cap uses the same table with
`conversation_id` set and `budget_id` null, because the cap is not a row in
`budgets`.

Context sent to a bot is the goal, the roster with roles, the rules, the
summary, and recent messages, plus the sentence "mention the bot you hand work
to". Secret values never enter that prompt, a log line, or an API response.

## Consequences

- Direct-message upserts must target the partial unique index
  `(workspace_id, user_id, bot_id) WHERE type = 'dm'`.
- Group posts do not queue a bot until the orchestrator runs. The Phase 4
  send path stays specific to direct messages.
- A conversation-cap approval cannot reuse `ON CONFLICT (workspace_id, budget_id)`.
  Pending uniqueness for a group cap is `(workspace_id, conversation_id)`.
- Summaries are extracts, not model summaries. A later phase can replace them.
- Human presence dots are not part of this decision. Bot status already exists.
- Phone layout at 390 px stays in Phase 8.
