package main

import "testing"

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
