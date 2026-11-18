// SPDX-License-Identifier: AGPL-3.0-only

package secretrequests

import (
	"errors"

	"github.com/dunkeltech/botbox/backend/internal/gitlabaccess"
)

// FailureCode classifies failed tool requests without exposing dependency errors or secrets.
func FailureCode(err error) string {
	switch {
	case errors.Is(err, gitlabaccess.ErrNotRegistered):
		return "secret_request.gitlab_not_registered"
	case errors.Is(err, gitlabaccess.ErrNotAssigned):
		return "secret_request.gitlab_not_assigned"
	case errors.Is(err, gitlabaccess.ErrHostBlocked), errors.Is(err, ErrHostNotAllowed):
		return "secret_request.host_not_allowed"
	case errors.Is(err, ErrInvalidInput):
		return "secret_request.invalid_input"
	case errors.Is(err, ErrRateLimited):
		return "secret_request.rate_limited"
	case errors.Is(err, ErrUnsupportedCapability):
		return "secret_request.unsupported_capability"
	default:
		return "secret_request.policy_unavailable"
	}
}

// FailureMessage provides a safe English tool result; the UI translates the corresponding code.
func FailureMessage(code string) string {
	switch code {
	case "secret_request.gitlab_not_registered":
		return "Register this GitLab HTTPS origin in Workspace settings before requesting credentials."
	case "secret_request.gitlab_not_assigned":
		return "Assign this GitLab instance to this bot in Bot settings before requesting credentials."
	case "secret_request.host_not_allowed":
		return "Allow this destination in the bot network settings; explicit blocks must be removed by a bot manager."
	case "secret_request.invalid_input":
		return "The secret request has an invalid adapter, target origin, reason or placeholder name."
	case "secret_request.rate_limited":
		return "The bot or workspace secret request limit has been reached."
	case "secret_request.unsupported_capability":
		return "This credential capability is unavailable; browser login requires the isolated authentication broker."
	default:
		return "Credential policy is unavailable. Retry after the service is restored; credentials have not been granted."
	}
}
