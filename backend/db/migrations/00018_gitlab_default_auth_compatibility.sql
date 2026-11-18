-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- GitLab.com retains its existing Bearer support, including OAuth access tokens.
-- PRIVATE-TOKEN formatting applies only to the newly supported custom instances.
UPDATE bot_secrets SET header_name='Authorization',auth_scheme='bearer'
WHERE adapter_id='gitlab_api' AND domain_match='gitlab.com';
UPDATE workspace_secrets SET header_name='Authorization',auth_scheme='bearer'
WHERE adapter_id='gitlab_api' AND domain_match='gitlab.com';
UPDATE secret_leases SET header_name='Authorization',auth_scheme='bearer'
WHERE adapter_id='gitlab_api' AND domain_match='gitlab.com';
