// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/api"
	"github.com/dunkeltech/botbox/backend/internal/auth"
)

func TestPersistentLoginLockoutAndReset(t *testing.T) {
	h := newTestHarness(t, nil, harnessOpts{
		loginIPLimit:    100,
		loginEmailLimit: 100,
	})
	owner, _ := h.completeSetup("lockout@example.com", "CorrectHorse123!", "Owner", "Workspace", "ws-lock")

	// 5 failed logins trigger persistent DB lockout.
	for i := range 5 {
		res := h.request(http.MethodPost, "/api/auth/login", map[string]any{
			"email":    "lockout@example.com",
			"password": "wrong-password",
		}, "", nil, true)
		if res.status != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401: %s", i+1, res.status, res.body)
		}
	}

	// A locked account cannot log in and has the same response as an unknown email.
	lockedRes := h.request(http.MethodPost, "/api/auth/login", map[string]any{
		"email":    "lockout@example.com",
		"password": "CorrectHorse123!",
	}, "", nil, true)
	unknownRes := h.request(http.MethodPost, "/api/auth/login", map[string]any{
		"email": "unknown@example.com", "password": "CorrectHorse123!",
	}, "", nil, true)
	if lockedRes.status != http.StatusUnauthorized || unknownRes.status != lockedRes.status || string(unknownRes.body) != string(lockedRes.body) {
		t.Fatalf("locked response = %d %s, unknown = %d %s", lockedRes.status, lockedRes.body, unknownRes.status, unknownRes.body)
	}

	// Even a brand new server instance sharing the DB enforces the lockout (persisted across restarts).
	restarted := api.NewHandlerWithConfig(api.Config{
		Pool:                  h.pool,
		Setup:                 h.setupMgr,
		LoginIPRateLimiter:    auth.NewRateLimiter(100, time.Minute, h.clock.Now),
		LoginEmailRateLimiter: auth.NewRateLimiter(100, 15*time.Minute, h.clock.Now),
		Now:                   h.clock.Now,
	})
	restartedRec := doRawRequest(t, restarted, http.MethodPost, "/api/auth/login", map[string]any{
		"email":    "lockout@example.com",
		"password": "CorrectHorse123!",
	})
	if restartedRec.Code != http.StatusUnauthorized {
		t.Fatalf("restarted server status = %d, want 401: %s", restartedRec.Code, restartedRec.Body.String())
	}

	// Verify audit_log recorded the lockout event without credentials.
	var auditCount int
	if err := h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE actor_user_id = $1 AND action = 'auth.login_locked'`,
		owner.User.Id,
	).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("expected 1 auth.login_locked audit row, got %d err=%v", auditCount, err)
	}

	// Advance clock past 15-minute lockout -> valid login succeeds and clears counter.
	h.clock.Advance(16 * time.Minute)
	okRes := h.request(http.MethodPost, "/api/auth/login", map[string]any{
		"email":    "lockout@example.com",
		"password": "CorrectHorse123!",
	}, "", nil, true)
	if okRes.status != http.StatusOK {
		t.Fatalf("post-lockout login status = %d, want 200: %s", okRes.status, okRes.body)
	}

	var attempts int
	if err := h.pool.QueryRow(context.Background(), `SELECT failed_login_attempts FROM users WHERE id = $1`, owner.User.Id).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("expected failed_login_attempts reset to 0, got %d err=%v", attempts, err)
	}
}
