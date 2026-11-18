<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0023. Separate response size limits for Git and API credentials

- **Date:** 2026-11-17
- **Status:** Accepted & Implemented
- **Extends:** ADR-0016 and ADR-0020

## Context

The `gitlab_git` adapter already permits smart-HTTP packfile MIME types, but
inherits the 2 MiB response cap used for JSON APIs. Ref discovery succeeds while
clone/fetch responses exceeding that cap fail with a generic mediation-policy
502. Shallow clones can also exceed the cap. A body read failure produces the
same error, making an operational limit look like a destination-policy denial.

The proxy buffers and inspects the entire credentialed response before sending
any body bytes to the sandbox. Its Docker container has a 256 MiB memory limit.

## Options

1. Stream packs without mediation or retrieve archives through another adapter.
   Rejected: bypasses the existing response credential boundary.
2. Remove all size limits or raise every adapter's cap. Rejected: unbounded
   buffering or unnecessary memory exposure on unrelated API routes.
3. Give the reviewed Git adapter a bounded 64 MiB cap and retain the 2 MiB API cap.
4. Stage larger packs in proxy-owned storage before inspection. Deferred: the
   proxy has a read-only root filesystem and no writable staging mount; this
   requires a separate storage, quota and cleanup design.

## Decision

Choose option 3 for both GitLab.com and assigned self-hosted GitLab origins.
The 64 MiB limit applies separately to the wire body and decoded gzip/deflate
body, on every response status. All other adapters retain 2 MiB. Destination,
method, path, assignment, revocation, content-type and credential-reflection
checks remain enforced. Binary packs reflecting supported credential forms
are rejected before any body bytes are returned.

Return a specific 502 message with the byte cap when either size limit is
exceeded. Return a separate generic read-failure message for interrupted or
malformed body reads; dependency error text must never expose credentials.

## Consequences

- Git responses between 2 MiB and 64 MiB can complete without an API workaround.
- Larger responses still fail closed. Supporting larger repositories requires
  the staging design rather than an unlimited in-memory cap.
- The memory bound is per response, not a total concurrency guarantee. Large
  concurrent responses and decoding increase pressure on the container budget.
- The proxy image must be rebuilt and running proxy containers replaced for
  the code change to take effect; workspace host permissions need no change.
- Regression tests reproduce the previous failure and cover default/custom
  origins, decoded size limits, read/cancellation errors, API limits and
  credential/binary/redirect rejection.
