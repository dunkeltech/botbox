// SPDX-License-Identifier: AGPL-3.0-only

package engine_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/engine"
)

func TestTurnFailureCodesNeverExposePrivateCauses(t *testing.T) {
	private := errors.New("credential=fixture-private-value")
	for _, code := range []string{"secret_request.gitlab_not_registered", "secret_request.gitlab_not_assigned", "secret_request.host_not_allowed", "secret_request.policy_unavailable", engine.FailureCLIUnavailable, engine.FailureComputerNotRunning, engine.FailureComputerRestart, engine.FailureControlHeld, engine.FailureProxyUnavailable, engine.FailureCredentialsMissing, engine.FailureCredentialsUnavailable, engine.FailureProvider, engine.FailureStreamFormat, "fixture-private-value", ""} {
		wrapped := engine.WithFailureCode(code, private)
		safe := engine.TurnFailureCode(wrapped)
		if strings.Contains(safe, "fixture") || strings.Contains(wrapped.Error(), "fixture") {
			t.Fatal("private details exposed")
		}
		if !errors.Is(wrapped, private) {
			t.Fatal("private cause lost")
		}
		if code != "fixture-private-value" && code != "" && safe != code {
			t.Fatal("failure category lost")
		}
	}
	if engine.TurnFailureCode(private) != engine.FailureTurn {
		t.Fatal("unexpected dependency error exposed")
	}
}
