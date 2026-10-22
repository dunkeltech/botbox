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
  | 'workspaceAlreadyMember'
  | 'invitationNotFound'
  | 'invitationEmailMismatch'
  | 'botNotFound'
  | 'controlLocked'
  | 'environmentNotRunning'
  | 'environmentNotFound'
  | 'fileNotFound'
  | 'secretNotFound'
  | 'secretAlreadyExists'
  | 'securityViolation'
  | 'sandboxUnavailable'
  | 'approvalNotFound'
  | 'approvalConflict'
  | 'approvalHostNotBlocked'
  | 'approvalCapNotRaised'
  | 'chatQueueFull'
  | 'chatUnavailable'
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
  'workspace.already_member': 'workspaceAlreadyMember',
  'invitation.not_found': 'invitationNotFound',
  'invitation.email_mismatch': 'invitationEmailMismatch',
  'bot.not_found': 'botNotFound',
  'control.locked': 'controlLocked',
  'environment.not_running': 'environmentNotRunning',
  'environment.not_found': 'environmentNotFound',
  'file.not_found': 'fileNotFound',
  'secret.not_found': 'secretNotFound',
  'secret.already_exists': 'secretAlreadyExists',
  security_violation: 'securityViolation',
  sandbox_unavailable: 'sandboxUnavailable',
  'approval.not_found': 'approvalNotFound',
  'approval.conflict': 'approvalConflict',
  'approval.host_not_blocked': 'approvalHostNotBlocked',
  'approval.cap_not_raised': 'approvalCapNotRaised',
  'chat.queue_full': 'chatQueueFull',
  'chat.unavailable': 'chatUnavailable',
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
