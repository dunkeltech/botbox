<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# 0039. Model prices and subscription reconciliation

## Context

A bot computer could start with an unpriced model and fail later with a generic
CLI retry message. An interrupted subscription request with unknown usage can
block subsequent requests under a monetary budget. Workspace admins could see
the aggregate warning but had no way to resolve the individual request in bot.Box.

## Options

- Check prices only when a model request reaches the proxy.
- Check the saved model when managing the computer and give workspace admins a
  way to resolve an unknown subscription charge.
- Apply a default rate or clear unknown usage automatically after a failed request.

## Decision

Require a workspace rate for the bot's saved provider and exact model before the
computer starts. Serialize lifecycle operations and settings changes on the bot
row. Stop a running computer before committing a change to an unpriced model.
Check existing running computers during API startup. Sandbox home volumes are
preserved. The proxy still checks the effective model when admitting requests
because an interactive CLI can select another model.

Workspace owners and admins can resolve individual budgeted subscription
requests with unknown usage. They identify the provider and the basis for the
token counts. Use the current workspace model rate to compute the internal
charge. Save the adjustment and its audit entry in the same transaction under
the workspace spend lock.

## Consequences

Operators must enter prices for existing bots before starting their computers.
An operator estimate is explicitly identified in the audit log. It settles the
internal budget commitment for that request. ChatGPT and Claude subscription
allowances and provider billing are managed by the respective providers.
