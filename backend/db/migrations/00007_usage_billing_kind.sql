-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- A subscription has no per-request USD charge; null cost is not missing billing.
ALTER TABLE usage_records ADD COLUMN billing_kind TEXT NOT NULL DEFAULT 'metered'
 CHECK(billing_kind IN ('metered','subscription'));
