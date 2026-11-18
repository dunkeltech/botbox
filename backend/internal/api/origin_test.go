// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"slices"
	"testing"
)

func TestVNCOriginPatterns(t *testing.T) {
	t.Parallel()
	loopback := vncOriginPatterns("")
	for _, want := range []string{"localhost", "localhost:*", "127.0.0.1", "127.0.0.1:*"} {
		if !slices.Contains(loopback, want) {
			t.Fatalf("patterns %v missing %s", loopback, want)
		}
	}
	if slices.Contains(loopback, "*") {
		t.Fatalf("patterns allow every origin: %v", loopback)
	}

	withSite := vncOriginPatterns("https://box.example/app")
	if !slices.Contains(withSite, "box.example") {
		t.Fatalf("patterns %v missing public host", withSite)
	}

	again := vncOriginPatterns("http://127.0.0.1:8000")
	if slices.Contains(again, "127.0.0.1:8000") {
		t.Fatalf("loopback port was added beside the wildcard: %v", again)
	}
	if vncOriginPatterns("://") == nil {
		t.Fatal("bad url dropped the loopback patterns")
	}
}
