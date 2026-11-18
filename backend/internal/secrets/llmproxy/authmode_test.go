// SPDX-License-Identifier: AGPL-3.0-only

package llmproxy

import (
	"context"
	"errors"
	"testing"
)

func TestAuthModeReportsPlaceholderWithoutCredential(t *testing.T) {
	//nolint:gosec // synthetic credential used to verify redaction
	const secret = "sk-ant-api03-SUPERSECRETVALUE"
	cases := []struct {
		name string
		cfg  *SecretConfig
		err  error
		want string
		fail bool
	}{
		{name: "oauth", cfg: &SecretConfig{AuthKind: AuthKindClaudeOAuth, SecretValue: secret}, want: "oauth"},
		{name: "api key", cfg: &SecretConfig{AuthKind: AuthKindAnthropicKey, SecretValue: secret}, want: "api_key"},
		{name: "bearer", cfg: &SecretConfig{AuthKind: AuthKindBearer, SecretValue: secret}, want: "api_key"},
		{name: "missing", err: ErrSecretNotFound, want: "none"},
		{name: "empty", cfg: &SecretConfig{AuthKind: AuthKindClaudeOAuth}, want: "none"},
		{name: "unknown kind", cfg: &SecretConfig{AuthKind: "other", SecretValue: secret}, want: "none"},
		{name: "dependency", err: errors.New("dial tcp password=" + secret), fail: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AuthMode(context.Background(), func(context.Context, string, string) (*SecretConfig, error) {
				return tc.cfg, tc.err
			}, "bot", ProviderAnthropic)
			if tc.fail {
				if err == nil || got != "" {
					t.Fatalf("got %q err=%v", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q err=%v want %s", got, err, tc.want)
			}
			if got == secret {
				t.Fatal("auth mode returned the credential")
			}
		})
	}
	if _, err := AuthMode(context.Background(), nil, "bot", ProviderAnthropic); err == nil {
		t.Fatal("nil resolver was accepted")
	}
}
