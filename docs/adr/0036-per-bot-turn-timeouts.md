<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0036. Per-bot turn timeouts

- **Date:** 2026-11-19
- **Status:** Accepted

## Context

Some sandbox tasks take longer than the fixed ten-minute turn deadline. The
worker, agent, and queue each had independent time limits that could end a
longer turn or mark it interrupted while its worker was still running.

## Options

1. Raise the limit globally, increasing the time every bot can hold a worker slot.
2. Remove the execution limit and rely on humans to stop long-running turns.
3. Store a bounded timeout per bot and use it throughout managed execution.

## Decision

Use option 3. Bot managers can set the maximum turn duration to a whole number
from 1 to 120 minutes. Ten minutes remains the default for existing and new
bots. The worker reads the setting when claiming a turn, persists its deadline,
and propagates cancellation to the sandbox over gRPC. The agent retains a
bounded local timeout and terminates the CLI process group on cancellation.
Every execution claim gets a deadline, including a continuation after waiting
for a secret.

Renew the JetStream acknowledgement window while a delivery is active. This
keeps the existing durable consumer configuration and lets it redeliver after
a worker disappears. Treat a running turn as stale after its saved deadline
plus five minutes for cleanup. Turns claimed before the migration retain the
legacy stale-turn threshold. The running marker still prevents replaying a
turn's external effects.

## Consequences

Long turns can occupy a worker slot for up to two hours. Stop, desktop takeover,
and budget enforcement continue to apply during that window. Longer limits
require an updated sandbox image because older agents retain their fixed
ten-minute timeout.
