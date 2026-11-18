<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0025. Parallel bot turns and current activity

- **Date:** 2026-11-17
- **Status:** Accepted

## Context

Multiple mentions create multiple durable turns, but one synchronous NATS
callback processed them serially. API turns additionally held the workspace
spend lock throughout model calls and tools. CLI proxy requests held that same
lock until settlement. The Team bot list cached its initial status, group live
events did not refresh it, and the group indicator selected only one tool turn.
A lead could remain Thinking while colleagues continued to appear Idle.

Concurrency must preserve one running turn per bot across API instances,
workspace isolation, turn caps, secret waits, cancellation, and budget admission.

## Options

1. Start an unbounded goroutine for every notification and remove spend locks.
   This risks resource exhaustion and admits simultaneous spending against the
   same remaining budget.
2. Keep serial execution and poll the UI more often. This fixes stale snapshots
   but leaves independent bots waiting for unrelated turns.
3. Bound notification processing, reserve supported request costs durably, and
   reconcile UI activity with persisted state.

## Decision

Use option 3. Each API instance accepts 1..64 concurrent turn notifications,
configured with `BOTBOX_TURN_CONCURRENCY` (default 4). Dispatch waits for capacity
before creating a goroutine. Shutdown cancels and joins active processors.
Existing transaction locks and the unique running-turn index serialize each bot;
NATS redelivery never replays running side effects. Claims acquire conversation
locks before bot locks, matching scheduling. The group counter and limit are
checked under that lock before marking a turn running. Already queued work that
cannot start because the group is paused or at its limit becomes interrupted.

API and CLI requests use the same trusted proxy admission and usage accounting.
A short workspace lock covers checking and reserving capacity. Supported
text-only request shapes with an explicit provider output-token cap use a
conservative input bound (four times JSON bytes plus 4096 framing tokens) and
that output cap at configured prices. Unknown extensions, remote context,
multimodal input, hosted tools, multiple completions, and missing/invalid caps
cannot claim a bound. With monetary limits these retain the existing serialized
admission for one provider request, not the complete bot turn. Subscription and
uncapped monetary usage do not retain the lock through the upstream call.

A provisional usage row precedes forwarding. Bounded requests store
`reserved_micros` on that row before releasing admission. All applicable
workspace, bot, and conversation caps count reservations. Reported spend,
warning thresholds, and budget approvals use actual charges; temporary capacity
shortages do not create approvals. Settlement is idempotent and independent of
request cancellation. It replaces the estimate with actual usage and releases
the remainder. Unknown, invalid, or over-bound costs clear the estimate and
remain unpriced, failing closed for applicable monetary budgets. A crash before
settlement leaves a durable reservation or unknown row; it cannot free money.
Reservations are never expired without billing reconciliation. Aggregate runner
usage must not double-charge proxy-metered API or CLI requests.

Live engine events carry `BotID`, `TurnID`, `RunID`, and `Sequence` within a run.
Only worker completion ends a turn's activity; a model-step idle event cannot.
Failures after claim conditionally finalize still-running turns so they do not
remain Thinking in a healthy worker. Waiting-secret and terminal states are not
overwritten by that cleanup.

The UI treats events as invalidation hints and reads current snapshots. The
shared Team bot list also polls every three seconds to cover unopened
conversations and missed events. Conversation sockets reconnect and reload
snapshots. The group indicator lists every active bot, even without tool rows,
and shows queued and secret-wait states separately. Multiple pending turns for
one bot produce one indicator, preferring its running turn. Existing status
colours and translations remain in use. API turn history gains an additive
`botId` property; clients can still read older history without that property.

## Consequences

- A slow bot no longer blocks other bots' tools or entire turns.
- Bounded metered requests can overlap when every relevant budget has room.
  Opaque budgeted requests still serialize at the provider boundary. Broad
  multimodal reservations require reviewed provider-specific bounds later.
- Conservative reservations can require more free capacity than actual billing.
  An abandoned reservation or unknown charge needs reconciliation, not a timer
  that silently assumes the provider charged nothing.
- Subscription usage remains tokens-only. No provider credentials or prompt
  text are added to status events or logs. Conversation authorization and NATS
  workspace scoping are unchanged.
- Queue, PostgreSQL integration, race-detector, UI, and fully mocked browser
  tests cover concurrency, independent status, caps, cancellation, stale events,
  reload, and reconnect. No real LLM call is needed.

Run `make test-e2e` inside devenv against a running frontend (default
`http://127.0.0.1:3000`, override `BOTBOX_E2E_URL`). Its API and WebSocket routes
are fixtures, including authentication, so no real bot or provider is contacted.
The Playwright JavaScript driver and matching browser come from devenv.
