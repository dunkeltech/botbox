<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0044. Preserve group history when deleting bots

## Status

Accepted

## Context

Bot deletion is blocked when a bot has authored a message or is a group lead.
Group messages are shared records. Their author IDs also connect replies and
tool events. A bot's direct conversations already follow its lifecycle.

## Options

- Keep deleted bots in the bot table. This would require exclusion checks in
  scheduling, credential management, and every bot lookup.
- Cascade the deletion into group messages or clear their authors. This would
  lose conversation content or break the association between a bot and its posts.
- Keep the historical author ID and validate the author when the ID is written.

## Decision

Use the third option, following the write-time validation for user authors in
[ADR-0037](0037-session-and-infrastructure-authorization.md). Check the bot's
workspace and acquire a key-share lock when a message claims its ID. The lock
serializes new authorship against deletion.

Cascade bot deletion to its direct conversations and group memberships. Clear
the lead of any group it led. Group members retain access to the conversation
and can edit its settings or assign another lead through the group API.

## Consequences

Historical group messages keep their bot ID after deletion. A new message
requires an existing bot in the same workspace. Clients can receive a null
group lead, and no other bot inherits that role automatically.
