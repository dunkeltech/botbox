// SPDX-License-Identifier: AGPL-3.0-only

package bots

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComputeAvatarSpecMatchesFixtures(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("testdata", "avatar_fixtures.json"))
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}

	var want []AvatarSpec
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("unmarshal fixtures: %v", err)
	}
	if len(want) == 0 {
		t.Fatal("expected non-empty avatar fixtures")
	}

	for _, tc := range want {
		tc := tc
		t.Run(tc.Seed, func(t *testing.T) {
			t.Parallel()
			got, err := ComputeAvatarSpec(tc.Seed)
			if err != nil {
				t.Fatalf("ComputeAvatarSpec(%q) failed: %v", tc.Seed, err)
			}
			if got != tc {
				t.Fatalf("ComputeAvatarSpec(%q) mismatch:\n got: %+v\nwant: %+v", tc.Seed, got, tc)
			}
		})
	}
}

func TestValidateAvatarSeedNegativeCases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		seed string
	}{
		{name: "empty", seed: ""},
		{name: "uppercase", seed: "Atlas-005a"},
		{name: "spaces", seed: "atlas 005a"},
		{name: "underscore", seed: "atlas_005a"},
		{name: "unicode", seed: "atläs-005a"},
		{name: "too_long", seed: strings.Repeat("a", 101)},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := ValidateAvatarSeed(tc.seed); !errors.Is(err, ErrInvalidAvatarSeed) {
				t.Fatalf("ValidateAvatarSeed(%q) err = %v, want %v", tc.seed, err, ErrInvalidAvatarSeed)
			}
			if _, err := ComputeAvatarSpec(tc.seed); !errors.Is(err, ErrInvalidAvatarSeed) {
				t.Fatalf("ComputeAvatarSpec(%q) err = %v, want %v", tc.seed, err, ErrInvalidAvatarSeed)
			}
		})
	}
}
