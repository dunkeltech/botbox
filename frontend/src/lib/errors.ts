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
