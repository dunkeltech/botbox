// SPDX-License-Identifier: AGPL-3.0-only

package mitmproxy

import "testing"

func TestGitLabAdapterBindsExplicitOrigin(t *testing.T) {
	for _, id := range []string{"gitlab_api", "gitlab_git"} {
		adapter, err := ResolveAdapter(id, "https://gitlab.example.org")
		if err != nil {
			t.Fatal(err)
		}
		if adapter.Host != "gitlab.example.org" || adapter.Port != "443" {
			t.Fatalf("wrong destination: %+v", adapter)
		}
		path, method := "/api/v4/user", "GET"
		if id == "gitlab_git" {
			path = "/team/repo.git/info/refs"
		}
		if !adapter.MatchesRequest(adapter.Host, "443", method, path) {
			t.Fatal("expected route rejected")
		}
		if adapter.MatchesRequest("gitlab.com", "443", method, path) {
			t.Fatal("cross-instance credential reuse")
		}
		if adapter.MatchesRequest(adapter.Host, "443", "GET", "/api/v4evil/user") {
			t.Fatal("invalid API prefix accepted")
		}
	}
	for _, origin := range []string{"http://gitlab.example.org", "https://127.0.0.1", "https://gitlab.example.org:8443", "https://gitlab.example.org/api", "https://gitlab.example.org?x=1", "https://user:pw@gitlab.example.org"} {
		if _, err := ResolveAdapter("gitlab_api", origin); err == nil {
			t.Fatalf("unsafe origin: %s", origin)
		}
	}
	if _, err := ResolveAdapter("github_api", "https://gitlab.example.org"); err == nil {
		t.Fatal("arbitrary adapter rebound")
	}
}

func TestGitLabAdapterEncodedPaths(t *testing.T) {
	adapter, err := ResolveAdapter("gitlab_api", "https://gitlab.example.org")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		path  string
		valid bool
	}{
		{"/api/v4/projects/team%2Frepo", true},
		{"/api/v4/%2e%2e/%2e%2e/admin", false},
		{"/api/v4/%252e%252e/%252e%252e/admin", false},
		{"/api/v4evil/user", false},
		{"/api/v4/projects", true},
	} {
		if adapter.MatchesEscapedRequest(adapter.Host, "443", "GET", tt.path) != tt.valid {
			t.Fatalf("wrong policy for %s", tt.path)
		}
	}
}

func TestGitLabDefaultKeepsBearerCompatibility(t *testing.T) {
	adapter, err := ResolveAdapter("gitlab_api", "")
	if err != nil {
		t.Fatal(err)
	}
	if adapter.Host != "gitlab.com" || adapter.HeaderName != "Authorization" || adapter.Scheme != SchemeBearer {
		t.Fatal("GitLab.com token format changed")
	}
	custom, err := ResolveAdapter("gitlab_api", "https://gitlab.example.org")
	if err != nil {
		t.Fatal(err)
	}
	if custom.HeaderName != "PRIVATE-TOKEN" || custom.Scheme != SchemeRaw {
		t.Fatal("custom GitLab token format changed")
	}
}
