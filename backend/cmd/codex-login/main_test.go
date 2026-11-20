// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestPairingDestinationRequiresTLSOrLoopback(t *testing.T) {
	for _, tt := range []struct {
		origin string
		valid  bool
	}{
		{"https://botbox.example", true},
		{"http://127.0.0.1:8000", true},
		{"http://localhost:8000/", true},
		{"http://botbox.example", false},
		{"https://user@botbox.example", false},
		{"https://botbox.example/other", false},
		{"https://botbox.example?state=a", false},
		{"file:///tmp", false},
	} {
		url, err := controlPlaneURL(tt.origin)
		if (err == nil) != tt.valid {
			t.Fatalf("%s: %v", tt.origin, err)
		}
		if tt.valid && url.Path != "/api/codex-plan/pairing/complete" {
			t.Fatalf("unexpected destination %s", url)
		}
	}
}

func TestShowAuthorizationLinkBeforeOpeningBrowser(t *testing.T) {
	const address = "https://example.com/authorize?client_id=test&state=test"
	for _, tt := range []struct {
		name string
		err  error
	}{
		{name: "browser opens"},
		{name: "opener fails", err: errors.New("browser unavailable")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			calls := 0
			showAuthorizationLink(&output, address, func() error {
				calls++
				if !strings.HasSuffix(output.String(), "\n"+address+"\n") {
					t.Errorf("authorization URL should be on its own line before the browser opens, got %q", output.String())
				}
				return tt.err
			})
			if calls != 1 || strings.Count(output.String(), address) != 1 {
				t.Errorf("expected one browser launch and one URL, got %d calls and %q", calls, output.String())
			}
		})
	}
}
