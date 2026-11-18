// SPDX-License-Identifier: AGPL-3.0-only

package authbroker

import (
	"errors"
	"testing"
)

func TestResolveLoginAdapterSelfHostedGitLab(t *testing.T) {
	for _, tt := range []struct {
		name, adapter, origin string
		valid                 bool
	}{
		{"self hosted", "gitlab_web", "https://gitlab.example.org", true},
		{"default", "gitlab_web", "", true},
		{"github cannot change host", "github_web", "https://gitlab.example.org", false},
		{"unknown", "unknown", "https://gitlab.example.org", false},
		{"http", "gitlab_web", "http://gitlab.example.org", false},
		{"port", "gitlab_web", "https://gitlab.example.org:8443", false},
		{"path", "gitlab_web", "https://gitlab.example.org/gitlab", false},
		{"query", "gitlab_web", "https://gitlab.example.org?x=1", false},
		{"fragment", "gitlab_web", "https://gitlab.example.org#x", false},
		{"userinfo", "gitlab_web", "https://user@gitlab.example.org", false},
		{"ip", "gitlab_web", "https://127.0.0.1", false},
		{"wildcard", "gitlab_web", "https://*.example.org", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a, err := ResolveLoginAdapter(tt.adapter, tt.origin)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v err=%v", tt.valid, err)
			}
			if err != nil {
				if tt.adapter != "unknown" && !errors.Is(err, ErrNavigationForbidden) {
					t.Fatalf("invalid origin lost its safe broker error: %v", err)
				}
				return
			}
			if tt.origin != "" && a.Origin != "https://gitlab.example.org:443" {
				t.Fatalf("wrong origin: %s", a.Origin)
			}
			if !a.MatchesFormDestination(a.LoginURL(), a.LoginPath) {
				t.Fatal("own login form rejected")
			}
			if a.MatchesFormDestination(a.LoginURL(), "https://evil.example/users/sign_in") {
				t.Fatal("foreign form allowed")
			}
			if _, err := a.PermitsNavigation("https://evil.example/"); err == nil {
				t.Fatal("foreign redirect allowed")
			}
		})
	}
}
