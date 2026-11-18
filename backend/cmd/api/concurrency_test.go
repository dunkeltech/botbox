// SPDX-License-Identifier: AGPL-3.0-only

package main

import "testing"

func TestTurnConcurrency(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
	}{{"", 4}, {"1", 1}, {"64", 64}, {"0", 0}, {"65", 0}, {"-1", 0}, {"oops", 0}, {"1.5", 0}} {
		t.Run(tc.value, func(t *testing.T) {
			got, err := turnConcurrency(tc.value)
			if got != tc.want || (err != nil) != (tc.want == 0) {
				t.Fatalf("got=%d err=%v", got, err)
			}
		})
	}
}
