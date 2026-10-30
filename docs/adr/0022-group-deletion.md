# 0022. Permanent group deletion with retained usage

## Status

Accepted

## Context

Members can create group chats but cannot remove them. A group has no owner;
membership and workspace role already determine who can change settings.
Deleting a conversation cascades through messages, bot turns, sessions,
approvals, notifications, and secret-related state. The original turn foreign
key also cascades through usage records. Erasing those records would reduce
recorded spend and allow a member to reopen a bot or workspace budget.

A database deletion cannot stop an LLM request, a sandbox tool, or an isolated
authenticated browser. Removing active state would lose the information needed
to cancel those operations and revoke their credentials.

## Options

- Hide the group with a soft-delete flag. This keeps history indefinitely and
  requires every reader and scheduler to understand the flag.
- Delete the group and all dependent records. This removes chat data but erases
  metering and can orphan active execution.
- Delete chat data only when execution and credentials are inactive, retaining
  usage records independently of the deleted turns.

## Decision

Use the third option. `DELETE /api/workspaces/{workspaceID}/groups/{groupID}`
requires a human group member with owner, admin, or member workspace role.
Viewer members get 403; non-members and cross-workspace requests get 404.
The UI requires confirmation and explains the effect on all members.

Lock the conversation and its turns in a transaction. The parent lock blocks
new chat data; turn locks stop a queued worker from claiming work during the
active-state check. Running or waiting-for-secret turns, unexpired broker
sessions, and unrevoked secret leases cause `409 group.busy`. A concurrent
worker claim can cause a database deadlock; PostgreSQL rolls back one transaction
and the failed request can be retried without committing a partial deletion.

The conversation delete cascades through its chat data and queued turns.
The turn foreign key on usage records sets only `turn_id` to null. Workspace,
bot, request identity, tokens, billing kind, and costs remain, so budget checks
and usage deduplication still apply. Bots and computers are not deleted.
Write a `group.deleted` audit entry in the same transaction; an audit failure
rolls back the entire deletion.

## Consequences

Group deletion is permanent and affects every member. Historical usage no
longer links to deleted chat content, but spend totals remain accurate.
Active work must finish or be stopped and credentials revoked before deletion.
Already published queue notifications may outlive their deleted turns; workers
cannot claim the missing database rows or execute their tools.
