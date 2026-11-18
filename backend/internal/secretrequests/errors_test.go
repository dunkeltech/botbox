// SPDX-License-Identifier: AGPL-3.0-only

package secretrequests

import (
	"errors"
	"github.com/dunkeltech/botbox/backend/internal/gitlabaccess"
	"strings"
	"testing"
)

func TestFailureCodesNeverExposeDependencyCauses(t *testing.T) {
	for _, tt := range []struct {
		err  error
		code string
	}{
		{gitlabaccess.ErrNotRegistered, "secret_request.gitlab_not_registered"},
		{gitlabaccess.ErrNotAssigned, "secret_request.gitlab_not_assigned"},
		{gitlabaccess.ErrHostBlocked, "secret_request.host_not_allowed"},
		{ErrRateLimited, "secret_request.rate_limited"},
		{ErrInvalidInput, "secret_request.invalid_input"},
		{errors.New("private-token-value"), "secret_request.policy_unavailable"},
	} {
		code := FailureCode(tt.err)
		if code != tt.code {
			t.Fatalf("code %s want %s", code, tt.code)
		}
		if strings.Contains(FailureMessage(code), "private-token-value") {
			t.Fatal("private cause exposed")
		}
	}
}
