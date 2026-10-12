# 0007. Live CLI Streams and Scoped Proxy Admission

- **Date:** 2026-11-15
- **Status:** Accepted

## Context

Phase 4 needs CLI events before a process exits. The spike parsers collect a
complete stream and read agy stderr after stdout. With real process pipes, stderr
can fill while stdout waits for the process, causing a deadlock. Budget admission
also needs a trusted bot identity before any credentials are resolved.

## Options

1. Replace fixture parsers and all callers with a new asynchronous parser API.
2. Decode bounded records through the existing parsers in a live stream owner.
3. Trust a sandbox-supplied identity header when selecting budgets and secrets.

## Decision

Use option 2. `engine.Stream` owns the stdout and stderr readers, reads them
concurrently, and serializes event delivery. Context cancellation, parsing errors,
and sink failures close both readers. Readers must support interrupting Read with
Close, and event sinks must honor cancellation. One record is bounded to 1 MiB;
the live path never accumulates a complete turn. Existing batch APIs remain for
fixture compatibility. Ordering is preserved within each pipe; no total ordering
between stdout and stderr is claimed.

Use `llmproxy.NewScopedHandler` for per-bot proxy provisioning. Workspace and bot
identity originate from trusted configuration, never from sandbox request headers.
An optional injected budget check executes for every request before resolving
credentials, exchanging OAuth tokens, or calling an upstream. Exhaustion returns
429; other admission failures return 503 without exposing dependency errors.
OpenRouter is an explicit supported route with bearer authentication configured
by the resolver.

## Consequences

These are integration primitives, not a complete Phase 4 deployment. The existing
standalone proxy still uses the legacy handler. Provisioning must adopt scoped
handlers and inject a persistent, atomic budget service before claiming budget
enforcement. Usage metering, approvals, real CLI installation, process RPCs,
JetStream scheduling, DMs, and frontend integration remain separate work.
