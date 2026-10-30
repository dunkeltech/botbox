# 0004. Unified CLI Stream Event Model

- **Date:** 2026-11-14
- **Status:** Accepted

## Context

Each bot runtime wraps one of three headless coding CLIs (`claude -p --output-format stream-json --verbose`, `codex exec --json`, `agy -p --output-format stream-json`), each producing different NDJSON schemas and failure modes (Issue #2 Spike S3). The bot engine, NATS JetStream publisher, team chat UI, and budget tracker require one consistent, deterministic Go event model rather than per-CLI UI branches.

## Options Considered

### Option A — Normalized `engine.Event` Schema via Recorded-Fixture Parsers

- **Pros:** Decouples UI, chat streaming, and token/cost accounting from CLI vendor churn; deterministic unit tests parse recorded JSONL/stderr fixtures in `backend/internal/engine/testdata/` without calling real LLM APIs; oversized NDJSON lines are bounded (`ErrLineTooLong`) and context cancellation stops parsing promptly.
- **Cons:** New CLI flag/schema revisions require updating the corresponding parser adapter.

### Option B — Pass Raw Vendor JSON Through to NATS and Frontend

- **Pros:** Zero backend translation code.
- **Cons:** Couples React components, database audit tables, and budget trackers to three incompatible vendor formats.

## Decision

Adopt **Option A** in `backend/internal/engine/stream.go`, normalizing all three CLIs onto `engine.Event` with canonical kinds:

- `status`: execution phase updates (`thinking`, `action`, `idle`)
- `text_delta`: user-visible assistant output tokens
- `tool_call` / `tool_result`: tool invocation parameters (`ToolID`, `ToolName`, `ToolInput`) and completion state (`IsError`, `Text`)
- `usage`: `InputTokens`, `OutputTokens`, and `CacheTokens` counts for budget enforcement
- `error`: fatal execution errors, soft-denial errors, or parse failures (`ErrorCode`, `ErrorMessage`)

### `agy` Dual-Stream (`stdout` + `stderr`) Soft-Denial Handling

Observed `agy` behavior exits cleanly with `0` while emitting non-fatal tool/permission rejections (matching `"permission denied"` or `"denied"`) on `stderr` rather than aborting stdout JSON. `ParseAgyStream` reads both `stdout` NDJSON and `stderr`, surfacing stderr denials as structured `Event{Kind: EventKindError, ErrorCode: "tool_soft_denied", ErrorMessage: ...}` events so the orchestrator and team chat UI can surface actionable feedback cleanly.

### Event-specific decoding and format failures (2026-11-17)

A Claude CLI turn aborted because the decoder expected every top-level `message`
to be an object, while the CLI emitted a string. The retained session does not
identify that stream event's type. Decode the type first, then only the fields
needed by that event. Unknown event types are ignored; auxiliary text messages
are not forwarded to chat. Assistant and user events must still contain a message
object with content. Assistant content must be a block list; user content may
also be the echoed prompt string.

Decoding one shared payload schema was rejected because unrelated notices can
use different field types. Ignoring all decode errors was rejected because it
would silently lose assistant responses, tool results or accounting events.

Malformed CLI JSON, invalid known payloads and records over the existing size
limit carry the allowlisted `engine.stream_format_invalid` category through the
live stream and turn runner. The private cause remains available for error
matching, but its text and payload are not included in the public error or log
message. Transport, sink and cancellation errors retain their existing behavior.
Provider error results retain their provider error handling. The chat translates
format failures into a compatibility recovery hint instead of suggesting that
credentials or a budget need to change.

## Consequences

- Adding or updating a CLI runner only touches `backend/internal/engine/stream.go` and its recorded fixtures in `backend/internal/engine/testdata/`.
- Phase 5 (#7) can map `engine.Event` directly to chat streaming messages, approvals, and spend ledger accounting without vendor-specific logic.
