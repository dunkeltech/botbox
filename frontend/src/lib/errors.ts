// SPDX-License-Identifier: AGPL-3.0-only

import { ApiClientError } from '@/lib/api-client';

export type ErrorTranslationKey =
  | 'setupInvalidSecret'
  | 'setupAlreadyCompleted'
  | 'authCsrfRequired'
  | 'authUnauthenticated'
  | 'authTokenExpired'
  | 'authInvalidCredentials'
  | 'authRefreshTokenReused'
  | 'authRefreshTokenExpired'
  | 'authAccountExists'
  | 'authForbidden'
  | 'rateLimited'
  | 'validationError'
  | 'workspaceSlugTaken'
  | 'workspaceNotFound'
  | 'workspaceMemberNotFound'
  | 'workspaceLastOwner'
  | 'workspaceLastWorkspace'
  | 'workspaceBusy'
  | 'workspaceAlreadyMember'
  | 'invitationNotFound'
  | 'invitationEmailMismatch'
  | 'botNotFound'
  | 'controlLocked'
  | 'environmentNotRunning'
  | 'environmentNotFound'
  | 'environmentModelPriceRequired'
  | 'subscriptionUsageNotFound'
  | 'subscriptionModelPriceRequired'
  | 'fileNotFound'
  | 'secretNotFound'
  | 'secretAlreadyExists'
  | 'securityViolation'
  | 'sandboxUnavailable'
  | 'approvalNotFound'
  | 'approvalConflict'
  | 'approvalHostNotBlocked'
  | 'approvalWildcardBlocked'
  | 'approvalCapNotRaised'
  | 'policyConflict'
  | 'preconditionRequired'
  | 'secretRequestUnsupportedCapability'
  | 'secretRequestNotFound'
  | 'secretRequestConflict'
  | 'secretRequestRuleConflict'
  | 'secretRequestHostNotAllowed'
  | 'secretGitLabNotRegistered'
  | 'secretGitLabNotAssigned'
  | 'secretPolicyUnavailable'
  | 'chatQueueFull'
  | 'chatUnavailable'
  | 'groupBusy'
  | 'groupNotFound'
  | 'mcpToolDenied'
  | 'mcpServerConflict'
  | 'mcpSyncFailed'
  | 'memorySecretRejected'
  | 'memoryAlwaysBudgetExceeded'
  | 'memoryLimitReached'
  | 'memoryNotFound'
  | 'memoryAutoSaveDisabled'
  | 'guardrailRuleConflict'
  | 'guardrailRuleLimit'
  | 'guardrailRuleNotFound'
  | 'guardrailInvalidPattern'
  | 'unknownError';

const ERROR_CODE_TO_KEY: Record<string, ErrorTranslationKey> = {
  'setup.invalid_secret': 'setupInvalidSecret',
  'setup.already_completed': 'setupAlreadyCompleted',
  'auth.csrf_required': 'authCsrfRequired',
  'auth.unauthenticated': 'authUnauthenticated',
  'auth.token_expired': 'authTokenExpired',
  'auth.invalid_credentials': 'authInvalidCredentials',
  'auth.refresh_token_reused': 'authRefreshTokenReused',
  'auth.refresh_token_expired': 'authRefreshTokenExpired',
  'auth.account_exists': 'authAccountExists',
  'auth.forbidden': 'authForbidden',
  rate_limited: 'rateLimited',
  validation_error: 'validationError',
  'workspace.slug_taken': 'workspaceSlugTaken',
  'workspace.not_found': 'workspaceNotFound',
  'workspace.member_not_found': 'workspaceMemberNotFound',
  'workspace.last_owner': 'workspaceLastOwner',
  'workspace.last_workspace': 'workspaceLastWorkspace',
  'workspace.busy': 'workspaceBusy',
  'workspace.already_member': 'workspaceAlreadyMember',
  'invitation.not_found': 'invitationNotFound',
  'invitation.email_mismatch': 'invitationEmailMismatch',
  'bot.not_found': 'botNotFound',
  'control.locked': 'controlLocked',
  'environment.not_running': 'environmentNotRunning',
  'environment.not_found': 'environmentNotFound',
  'environment.model_price_required': 'environmentModelPriceRequired',
  'budget.usage_not_found': 'subscriptionUsageNotFound',
  'budget.model_price_required': 'subscriptionModelPriceRequired',
  'file.not_found': 'fileNotFound',
  'secret.not_found': 'secretNotFound',
  'secret.already_exists': 'secretAlreadyExists',
  security_violation: 'securityViolation',
  sandbox_unavailable: 'sandboxUnavailable',
  'approval.not_found': 'approvalNotFound',
  'approval.conflict': 'approvalConflict',
  'approval.host_not_blocked': 'approvalHostNotBlocked',
  'approval.wildcard_blocked': 'approvalWildcardBlocked',
  'approval.cap_not_raised': 'approvalCapNotRaised',
  policy_conflict: 'policyConflict',
  precondition_required: 'preconditionRequired',
  'secret_request.unsupported_capability': 'secretRequestUnsupportedCapability',
  'secret_request.not_found': 'secretRequestNotFound',
  'secret_request.conflict': 'secretRequestConflict',
  'secret_request.rule_conflict': 'secretRequestRuleConflict',
  'secret_request.host_not_allowed': 'secretRequestHostNotAllowed',
  'secret_request.gitlab_not_registered': 'secretGitLabNotRegistered',
  'secret_request.gitlab_not_assigned': 'secretGitLabNotAssigned',
  'secret_request.policy_unavailable': 'secretPolicyUnavailable',
  'chat.queue_full': 'chatQueueFull',
  'chat.unavailable': 'chatUnavailable',
  'group.busy': 'groupBusy',
  'group.not_found': 'groupNotFound',
  'mcp.tool_denied': 'mcpToolDenied',
  'mcp.server_conflict': 'mcpServerConflict',
  'mcp.sync_failed': 'mcpSyncFailed',
  'memory.secret_rejected': 'memorySecretRejected',
  'memory.always_budget_exceeded': 'memoryAlwaysBudgetExceeded',
  'memory.limit_reached': 'memoryLimitReached',
  'memory.not_found': 'memoryNotFound',
  'memory.auto_save_disabled': 'memoryAutoSaveDisabled',
  'guardrail.rule_conflict': 'guardrailRuleConflict',
  'guardrail.rule_limit': 'guardrailRuleLimit',
  'guardrail.rule_not_found': 'guardrailRuleNotFound',
  'guardrail.invalid_pattern': 'guardrailInvalidPattern',
};

/**
 * Resolves a backend ApiClientError code or unexpected failure into a safe next-intl error key.
 */
export function resolveErrorKey(err: unknown): ErrorTranslationKey {
  if (err instanceof ApiClientError) {
    return ERROR_CODE_TO_KEY[err.code] ?? 'unknownError';
  }
  return 'unknownError';
}
