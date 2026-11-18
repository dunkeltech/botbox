// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"sync"
	"time"
)

// RateLimiter enforces an exact sliding-window request limit per string key
// (such as client IP address or normalized email) with an injectable clock.
type RateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	now    func() time.Time
	hits   map[string][]time.Time
}

// NewRateLimiter creates a deterministic sliding-window rate limiter.
func NewRateLimiter(limit int, window time.Duration, now func() time.Time) *RateLimiter {
	if limit < 1 {
		limit = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	if now == nil {
		now = time.Now
	}
	return &RateLimiter{
		limit:  limit,
		window: window,
		now:    now,
		hits:   make(map[string][]time.Time),
	}
}

// Allow records an attempt for key when within the window limit and returns true,
// or returns false without adding a hit if the limit is exhausted.
func (l *RateLimiter) Allow(key string) bool {
	if l == nil {
		return true
	}
	now := l.now().UTC()
	cutoff := now.Add(-l.window)

	l.mu.Lock()
	defer l.mu.Unlock()

	existing := l.hits[key]
	kept := existing[:0]
	for _, ts := range existing {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}

	if len(kept) >= l.limit {
		l.hits[key] = kept
		return false
	}

	kept = append(kept, now)
	l.hits[key] = kept

	// Keep the map bounded if many distinct one-off keys appear over time.
	if len(l.hits) > 4096 {
		l.pruneExpiredLocked(cutoff)
	}
	return true
}

func (l *RateLimiter) pruneExpiredLocked(cutoff time.Time) {
	for k, timestamps := range l.hits {
		hasFresh := false
		for _, ts := range timestamps {
			if ts.After(cutoff) {
				hasFresh = true
				break
			}
		}
		if !hasFresh {
			delete(l.hits, k)
		}
	}
}
