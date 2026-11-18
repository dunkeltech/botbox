// SPDX-License-Identifier: AGPL-3.0-only

package authbroker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestHTTPHandlerAuthAndRoundtrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := NewMemoryBrowserEngine()
	svc := NewService(eng)

	if _, err := NewHTTPHandler(svc, "too-short"); err == nil {
		t.Fatal("expected error for short auth token")
	}
	handler, err := NewHTTPHandler(svc, "broker-secret-token-for-tests-only")
	if err != nil {
		t.Fatalf("NewHTTPHandler failed: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Unauthenticated request to broker control plane fails closed with 401.
	badClient, err := NewHTTPClient(srv.URL, "wrong-broker-secret-token-1234")
	if err != nil {
		t.Fatalf("NewHTTPClient failed: %v", err)
	}
	wsID := uuid.New()
	botID := uuid.New()
	userID := uuid.New()
	if _, err := badClient.OpenSession(ctx, OpenSessionInput{
		WorkspaceID: wsID,
		BotID:       botID,
		AdapterID:   "github_web",
	}); err == nil {
		t.Fatal("expected unauthorized error with wrong token")
	}

	// Health check stays reachable without token.
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close healthz body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on /healthz, got %d", resp.StatusCode)
	}

	client, err := NewHTTPClient(srv.URL, "broker-secret-token-for-tests-only")
	if err != nil {
		t.Fatalf("NewHTTPClient failed: %v", err)
	}

	sess, err := client.OpenSession(ctx, OpenSessionInput{
		WorkspaceID: wsID,
		BotID:       botID,
		AdapterID:   "github_web",
	})
	if err != nil {
		t.Fatalf("OpenSession over HTTP failed: %v", err)
	}

	eng.SetMFARequired(sess.SessionID, true)
	sub, err := client.SubmitCredentials(ctx, SubmitCredentialsInput{
		WorkspaceID: wsID,
		BotID:       botID,
		SessionID:   sess.SessionID,
		AdapterID:   "github_web",
		Expected:    sess.Binding,
		Username:    "alice",
		Secret:      "secret-over-http-test",
	})
	if err != nil {
		t.Fatalf("SubmitCredentials over HTTP failed: %v", err)
	}
	if !sub.Outcome.RequiresMFA {
		t.Fatal("expected RequiresMFA=true")
	}

	// Cross-workspace lookup preserves ErrSessionNotFound.
	if _, err := client.GetSession(ctx, uuid.New(), sess.SessionID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound over HTTP, got %v", err)
	}

	// Human takeover frame & completion roundtrip.
	if _, err := client.StartTakeover(ctx, wsID, sess.SessionID, userID); err != nil {
		t.Fatalf("StartTakeover failed: %v", err)
	}
	if _, _, err := client.BotScreenshot(ctx, wsID, botID, sess.SessionID); !errors.Is(err, ErrTakeoverActive) {
		t.Fatalf("expected ErrTakeoverActive over HTTP, got %v", err)
	}
	if _, _, err := client.TakeoverFrame(ctx, wsID, sess.SessionID, uuid.New()); !errors.Is(err, ErrTakeoverForbidden) {
		t.Fatalf("expected ErrTakeoverForbidden for wrong user over HTTP, got %v", err)
	}
	frame, info, err := client.TakeoverFrame(ctx, wsID, sess.SessionID, userID)
	if err != nil || len(frame) == 0 || !info.TakeoverActive {
		t.Fatalf("TakeoverFrame failed: err=%v len=%d info=%+v", err, len(frame), info)
	}
	if _, err := client.TakeoverInput(ctx, wsID, sess.SessionID, userID, TakeoverInput{Kind: "key", Key: "Enter"}); err != nil {
		t.Fatalf("TakeoverInput failed: %v", err)
	}
	done, err := client.CompleteTakeover(ctx, wsID, sess.SessionID, userID)
	if err != nil || done.Status != StatusAuthenticated {
		t.Fatalf("CompleteTakeover failed: err=%v done=%+v", err, done)
	}

	shot, _, err := client.BotScreenshot(ctx, wsID, botID, sess.SessionID)
	if err != nil || len(shot) == 0 {
		t.Fatalf("BotScreenshot after auth failed: %v", err)
	}
}
