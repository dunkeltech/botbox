// SPDX-License-Identifier: AGPL-3.0-only

package budgets_test

import (
	"github.com/dunkeltech/botbox/backend/internal/budgets"
	"math"
	"testing"
)

func TestPriceMicros(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		in, out, cache, ir, or, cr, want int64
		bad                              bool
	}{
		{"cache", 100, 20, 40, 1000000, 2000000, 100000, 104, false},
		{"round once", 1, 1, 0, 1, 1, 0, 1, false},
		{"zero", 0, 0, 0, 1, 1, 1, 0, false},
		{"negative", -1, 0, 0, 1, 1, 1, 0, true},
		{"cache range", 1, 0, 2, 1, 1, 1, 0, true},
		{"overflow", math.MaxInt64, math.MaxInt64, 0, math.MaxInt64, 1, 1, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := budgets.PriceMicros(tc.in, tc.out, tc.cache, tc.ir, tc.or, tc.cr)
			if (err != nil) != tc.bad || got != tc.want {
				t.Fatalf("got=%d err=%v", got, err)
			}
		})
	}
}

func TestReportedMicros(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int64
		bad   bool
	}{{"0.0000011", 2, false}, {"1.25", 1250000, false}, {"0", 0, false}, {"-1", 0, true}, {"unknown", 0, true}, {"1e30", 0, true}} {
		got, err := budgets.ReportedMicros(tc.input)
		if got != tc.want || (err != nil) != tc.bad {
			t.Fatalf("input=%s got=%d err=%v", tc.input, got, err)
		}
	}
}
