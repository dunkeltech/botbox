package engine

import "errors"

// Public failure codes describe recovery steps without exposing dependency errors.
const (
	FailureTurn                   = "engine.turn_failed"
	FailureCLIUnavailable         = "engine.cli_unavailable"
	FailureComputerNotRunning     = "engine.computer_not_running"
	FailureComputerRestart        = "engine.computer_restart_required"
	FailureControlHeld            = "engine.control_held"
	FailureProxyUnavailable       = "engine.proxy_unavailable"
	FailureCredentialsMissing     = "engine.credentials_missing"     //nolint:gosec // public error code, not credential material
	FailureCredentialsUnavailable = "engine.credentials_unavailable" //nolint:gosec // public error code, not credential material
	FailureProvider               = "engine.provider_failed"
)

type turnFailure struct {
	code  string
	cause error
}

func (e *turnFailure) Error() string { return e.code }
func (e *turnFailure) Unwrap() error { return e.cause }

// WithFailureCode attaches an allowlisted recovery category to a private cause.
// Error returns only that category, so dependency credentials cannot leak.
func WithFailureCode(code string, cause error) error {
	switch code {
	case "secret_request.gitlab_not_registered", "secret_request.gitlab_not_assigned", "secret_request.host_not_allowed", "secret_request.invalid_input", "secret_request.rate_limited", "secret_request.unsupported_capability", "secret_request.policy_unavailable", FailureCLIUnavailable, FailureComputerNotRunning, FailureComputerRestart, FailureControlHeld, FailureProxyUnavailable, FailureCredentialsMissing, FailureCredentialsUnavailable, FailureProvider:
	default:
		code = FailureTurn
	}
	return &turnFailure{code: code, cause: cause}
}

// TurnFailureCode returns a safe persisted code, including for unknown errors.
func TurnFailureCode(err error) string {
	var failure *turnFailure
	if errors.As(err, &failure) {
		return failure.code
	}
	return FailureTurn
}
