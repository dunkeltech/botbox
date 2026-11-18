// SPDX-License-Identifier: AGPL-3.0-only

package bots

import "time"

const (
	// DefaultTurnTimeout applies to existing bots and agents called without a deadline.
	DefaultTurnTimeout = 10 * time.Minute
	// MaxTurnTimeout bounds how long a single execution can occupy a worker slot.
	MaxTurnTimeout = 120 * time.Minute
)

// ValidTurnTimeoutMinutes reports whether the requested execution limit is allowed.
func ValidTurnTimeoutMinutes(minutes int32) bool {
	return minutes >= 1 && minutes <= int32(MaxTurnTimeout/time.Minute)
}
