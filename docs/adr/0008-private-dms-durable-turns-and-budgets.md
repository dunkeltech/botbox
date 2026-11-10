# 0008. Private DMs, Durable Turns and Budget Admission

- **Date:** 2026-11-15
- **Status:** Accepted

[ADR-0034](0034-pi-openrouter-cli.md) replaced the Model API runner with Pi
for OpenRouter. Managed execution now uses the sandbox CLI and its proxy routes.

## Context

Phase 4 connects model execution to private user/bot conversations. A sandbox is
shared by the workspace, but a DM must remain private even from other workspace
administrators. Duplicate queue deliveries must not repeat external tool effects.
Workspace budgets must also account for multiple bots sharing the same limit.

## Options

1. Keep messages and scheduling state in process memory.
2. Persist messages and turns in Postgres and use JetStream for durable delivery.
3. Retry every interrupted turn automatically, including external tool actions.
4. Trust model-side budget instructions or independently cached per-bot counters.

## Decision

Use option 2. All DM access checks the authenticated user against the conversation
owner, after workspace membership validation. Composite foreign keys prevent
cross-workspace message, bot, session, usage and budget relationships. Message IDs
provided by clients make successful repeated sends idempotent.

Publish the turn notification before committing the input transaction. A failed
publish rolls back the input. Workers retry notifications not yet visible in the
database. JetStream work-queue subjects include workspace and bot IDs and never
contain message content. Per-bot database locks and a unique running-turn index
prevent concurrent execution. Record the running marker before external actions.
Running turns are not automatically replayed after process failure because their
tool effects may already have happened. A later notification marks a running turn
interrupted after 20 minutes, beyond the worker notification lifetime of 15
minutes, before admitting queued work. Operators must inspect uncertain effects
before explicitly sending the work again.

Serialize turn execution for each workspace while checking and settling spend.
This conservative lock prevents different bots from independently spending the
same remaining workspace budget. Check all applicable daily/monthly limits before
every API provider request. Record integer microdollars, prefer provider-reported
costs, and otherwise price uncached input, cached input and output separately.
Reject budgeted API requests when model prices are unavailable. Unknown monetary
costs remain NULL, not zero. At exhaustion, create a deduplicated budget approval.
An audited administrator budget update resolves pending approvals only if the
new limit exceeds recorded spend. Calendar periods use UTC.

Conversation events use NATS with authorization at the WebSocket boundary.
WebSocket credentials use a request subprotocol, not a URL query string. The
server negotiates only the event subprotocol, limits socket lifetime to access
token expiry, and disconnects slow consumers. The UI reloads persisted history to
recover from missed events. Tool output is rendered as text rather than HTML.

API mode calls the same scoped LLM proxy handler through an in-process streaming
transport. The resolver decrypts `llm-openrouter`, `llm-openai` or `llm-anthropic`
secrets outside the sandbox, preferring bot scope over workspace inheritance.
The API tool loop invokes the compute driver; every tool checks the control lock.
CLI processes use the authenticated streaming agent RPC with fixed executable
names, bounded output, placeholder keys and process-group cancellation. Claude and Codex permission prompts are disabled only inside this sandbox
agent, as required by the Phase 4 execution contract; Docker isolation and egress
policy remain the enforcement boundary.

Pause state is stored per workspace/bot rather than in the worker process. Pause
and takeover share the queue's transaction lock, so a worker cannot claim a new
turn while a human is suspending execution. The worker registers cancellation
before rechecking persisted pause/control state; this closes the window between
claiming and receiving a control notification. Queued notifications are retried
while suspended, so release does not depend on an ephemeral wake-up message.
Resume does not replay an interrupted turn because tools may have side effects.
Both API and CLI execution use the AgentRunner lifecycle and a bounded event
channel; cancellation unblocks event producers before the channel is closed.

Sandbox CA installation writes a combined public trust bundle under the bot home
and configures Chromium's NSS database without sudo. The entrypoint sources the
runtime trust variables before starting supervisord. System trust remains intact;
no privilege escalation is needed for TLS interception.

## Consequences

- API execution retains the conservative workspace turn lock. CLI requests use
  per-request admission and settlement instead (ADR-0009).
- One in-flight provider request can cross a monetary limit; the next request is
  denied. Truncated successful streams without usage create an unpriced API usage record.
  Applicable monetary budgets deny further requests until usage is reconciled.
- Operators must investigate running turns left after a worker crash; blindly
  replaying them can repeat a commit, push or other external side effect.
- Claude/Codex CLI turns require the metered proxy deployment from ADR-0009.
  Per-request accounting and encrypted credentials are wired; agy OAuth exchange
  and automated billing reconciliation remain follow-up work.
- DM privacy does not isolate the bot's filesystem or desktop. Both views disclose
  this distinction explicitly.
