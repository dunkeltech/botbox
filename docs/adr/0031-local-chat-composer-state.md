<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0031. Keep chat draft state in the composer

## Context

Group and direct chat views held the draft alongside the transcript. Each
keystroke made React walk every loaded message and parse its Markdown again.
Direct chats can also load several older pages, so the cost grows as users
load history.

## Options

- Memoize each Markdown message. This avoids repeated parsing for stable props
  and still leaves a pass over the transcript on each keystroke.
- Move draft state into the composer. React can then update the input and
  mention picker independently of the transcript.

## Decision

Use dedicated composers for group and direct chat. The group composer also
owns caret position and mention selection. Composers hand submitted text to
their chat view. The chat view owns the conversation request, history refresh,
and scroll position.

## Consequences

The number of loaded messages no longer affects React's render work during
draft edits. History updates and live replies continue to render while a user
is typing. Regression tests count message renders during draft edits and
verify that incoming messages still appear.
