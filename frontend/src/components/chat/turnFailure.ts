// SPDX-License-Identifier: AGPL-3.0-only

const turnFailureKeys: Record<string, string> = {
  'secret_request.gitlab_not_registered': 'secretGitLabNotRegistered',
  'secret_request.gitlab_not_assigned': 'secretGitLabNotAssigned',
  'secret_request.host_not_allowed': 'secretHostNotAllowed',
  'secret_request.invalid_input': 'secretInvalidInput',
  'secret_request.rate_limited': 'secretRateLimited',
  'secret_request.policy_unavailable': 'secretPolicyUnavailable',
  'secret_request.unsupported_capability': 'secretCapabilityUnavailable',
  'engine.cli_unavailable': 'cliUnavailable',
  'engine.computer_not_running': 'computerNotRunning',
  'engine.computer_restart_required': 'computerRestartRequired',
  'engine.computer_controlled': 'computerControlled',
  'engine.proxy_unavailable': 'proxyUnavailable',
  'engine.credentials_missing': 'credentialsMissing',
  'engine.agy_login_required': 'agyLoginRequired',
  'engine.credentials_unavailable': 'credentialsUnavailable',
  'engine.provider_failed': 'providerFailed',
  'engine.stream_format_invalid': 'streamFormatInvalid',
  'engine.guardrail_blocked': 'guardrailBlocked',
  'engine.interrupted': 'turnInterrupted',
  budget_exceeded: 'budgetExceeded',
  'engine.control_held': 'computerControlled',
};

/** Map persisted failure codes to public recovery copy, never raw dependency text. */
export function turnFailureKey(code: string): string {
  return Object.hasOwn(turnFailureKeys, code)
    ? (turnFailureKeys[code] ?? 'turnFailed')
    : 'turnFailed';
}
