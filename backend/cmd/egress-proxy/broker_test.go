package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/authbroker"
	"github.com/google/uuid"
)

func TestEgressBrokerBridgeIsolationAndGating(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// 1. Fail closed when broker is not configured.
	disabledHandler := newEgressHandler(egressDeps{})
	req := httptest.NewRequest(http.MethodPost, "/broker/read", strings.NewReader(`{"broker_session_id":"`+uuid.New().String()+`","selector":"body"}`))
	rec := httptest.NewRecorder()
	disabledHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 when broker nil, got %d (%s)", rec.Code, rec.Body.String())
	}

	// 2. Configured broker pins (Workspace, Bot) strictly on every bridge operation.
	eng := authbroker.NewMemoryBrowserEngine()
	svc := authbroker.NewService(eng)
	wsID := uuid.New()
	botID := uuid.New()
	otherBotID := uuid.New()
	userID := uuid.New()

	sess, err := svc.OpenSession(ctx, authbroker.OpenSessionInput{WorkspaceID: wsID, BotID: botID, AdapterID: "github_web"})
	if err != nil {
		t.Fatalf("OpenSession failed: %v", err)
	}
	otherSess, err := svc.OpenSession(ctx, authbroker.OpenSessionInput{WorkspaceID: wsID, BotID: otherBotID, AdapterID: "github_web"})
	if err != nil {
		t.Fatalf("OpenSession otherBot failed: %v", err)
	}
	if _, err := svc.SubmitCredentials(ctx, authbroker.SubmitCredentialsInput{ //nolint:gosec // synthetic test fixture
		WorkspaceID: wsID,
		BotID:       botID,
		SessionID:   sess.SessionID,
		AdapterID:   "github_web",
		Expected:    sess.Binding,
		Username:    "alice",
		Secret:      "secret-never-return",
	}); err != nil {
		t.Fatalf("SubmitCredentials failed: %v", err)
	}

	h := newEgressHandler(egressDeps{
		Broker:          svc,
		BrokerWorkspace: wsID,
		BrokerBot:       botID,
	})

	call := func(path string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// Cross-bot session ID returns 404 even if same workspace.
	if w := call("/broker/read", map[string]string{"broker_session_id": otherSess.SessionID.String(), "selector": "body"}); w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 accessing another bot broker session, got %d (%s)", w.Code, w.Body.String())
	}

	// Password selector & unapproved navigation host are forbidden.
	if w := call("/broker/read", map[string]string{"broker_session_id": sess.SessionID.String(), "selector": "input[type='password']"}); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for password selector, got %d", w.Code)
	}
	if w := call("/broker/navigate", map[string]string{"broker_session_id": sess.SessionID.String(), "url": "https://evil.example/login"}); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 navigating outside adapter origin, got %d", w.Code)
	}

	// Valid read and screenshot succeed without secret leakage.
	if w := call("/broker/read", map[string]string{"broker_session_id": sess.SessionID.String(), "selector": "body"}); w.Code != http.StatusOK || strings.Contains(w.Body.String(), "secret-never-return") {
		t.Fatalf("unexpected /broker/read result: code=%d body=%s", w.Code, w.Body.String())
	}
	if w := call("/broker/screenshot", map[string]string{"broker_session_id": sess.SessionID.String()}); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"sanitized":true`) {
		t.Fatalf("unexpected /broker/screenshot result: code=%d body=%s", w.Code, w.Body.String())
	}

	// Human takeover blocks bot bridge routes cleanly.
	if _, err := svc.StartTakeover(ctx, wsID, sess.SessionID, userID); err != nil {
		t.Fatalf("StartTakeover failed: %v", err)
	}
	if w := call("/broker/read", map[string]string{"broker_session_id": sess.SessionID.String(), "selector": "body"}); w.Code != http.StatusLocked {
		t.Fatalf("expected 423 during takeover, got %d (%s)", w.Code, w.Body.String())
	}
}
