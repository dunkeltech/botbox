package vncrelay_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"github.com/dunkeltech/botbox/backend/internal/compute/vncrelay"
)

func testSigner(t *testing.T) *vncrelay.Signer {
	t.Helper()
	signer, err := vncrelay.NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return signer
}

func TestNewSignerRejectsShortSecret(t *testing.T) {
	t.Parallel()

	if _, err := vncrelay.NewSigner([]byte("too-short")); err == nil {
		t.Fatal("expected error for short secret, got nil")
	}
}

func TestRelayHTTPCases(t *testing.T) {
	t.Parallel()

	fixedNow := time.Date(2026, 11, 14, 22, 0, 0, 0, time.UTC)
	signer := testSigner(t)
	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))

	expiredToken, err := signer.Sign(vncrelay.TicketClaims{
		WorkspaceID: "ws-1",
		BotID:       "bot-atlas",
		UserID:      "usr-1",
		CanControl:  true,
		ExpiresAt:   fixedNow.Add(-time.Second),
	})
	if err != nil {
		t.Fatalf("Sign expired: %v", err)
	}

	otherWorkspaceToken, err := signer.Sign(vncrelay.TicketClaims{
		WorkspaceID: "ws-other",
		BotID:       "bot-atlas",
		UserID:      "usr-1",
		CanControl:  true,
		ExpiresAt:   fixedNow.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("Sign cross-workspace: %v", err)
	}

	validToken, err := signer.Sign(vncrelay.TicketClaims{
		WorkspaceID: "ws-1",
		BotID:       "bot-atlas",
		UserID:      "usr-1",
		CanControl:  true,
		ExpiresAt:   fixedNow.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("Sign valid: %v", err)
	}

	tests := []struct {
		name           string
		url            string
		upstream       vncrelay.UpstreamResolver
		wantStatus     int
		wantErrorCode  string
		nilSignerCheck bool
	}{
		{
			name:          "missing ticket returns 401",
			url:           "/ws/bots/ws-1/bot-atlas/vnc",
			wantStatus:    http.StatusUnauthorized,
			wantErrorCode: "unauthorized",
		},
		{
			name:          "tampered ticket returns 401",
			url:           "/ws/bots/ws-1/bot-atlas/vnc?ticket=" + validToken + "tampered",
			wantStatus:    http.StatusUnauthorized,
			wantErrorCode: "unauthorized",
		},
		{
			name:          "expired ticket returns 401",
			url:           "/ws/bots/ws-1/bot-atlas/vnc?ticket=" + expiredToken,
			wantStatus:    http.StatusUnauthorized,
			wantErrorCode: "unauthorized",
		},
		{
			name:          "cross-workspace ticket returns 404",
			url:           "/ws/bots/ws-1/bot-atlas/vnc?ticket=" + otherWorkspaceToken,
			wantStatus:    http.StatusNotFound,
			wantErrorCode: "not_found",
		},
		{
			name: "unreachable upstream returns 502",
			url:  "/ws/bots/ws-1/bot-atlas/vnc?ticket=" + validToken,
			upstream: func(_ context.Context, _, _ string) (string, error) {
				return "", errors.New("container stopped")
			},
			wantStatus:    http.StatusBadGateway,
			wantErrorCode: "upstream_unavailable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := chi.NewRouter()
			handler := vncrelay.NewRelayHandler(signer, tc.upstream, discardLogger, &vncrelay.RelayOptions{
				Now: func() time.Time { return fixedNow },
			})
			r.Get("/ws/bots/{workspaceID}/{botID}/vnc", handler)

			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			res := rec.Result()
			defer func() {
				_ = res.Body.Close()
			}()

			if res.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.wantStatus)
			}

			var decoded struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
				t.Fatalf("decode error JSON: %v", err)
			}
			if decoded.Error.Code != tc.wantErrorCode {
				t.Fatalf("error.code = %q, want %q", decoded.Error.Code, tc.wantErrorCode)
			}
		})
	}
}

func TestRelayBidirectionalAndViewOnlyFiltering(t *testing.T) {
	t.Parallel()

	fixedNow := time.Date(2026, 11, 14, 22, 0, 0, 0, time.UTC)
	signer := testSigner(t)
	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))

	runScenario := func(t *testing.T, canControl bool) [][]byte {
		t.Helper()

		var mu sync.Mutex
		var receivedByUpstream [][]byte
		serverBanner := []byte("RFB 003.008\n")

		upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
			if err != nil {
				return
			}
			defer func() { _ = conn.CloseNow() }()

			if err := conn.Write(r.Context(), websocket.MessageBinary, serverBanner); err != nil {
				return
			}

			for {
				_, data, err := conn.Read(r.Context())
				if err != nil {
					return
				}
				mu.Lock()
				copied := append([]byte(nil), data...)
				receivedByUpstream = append(receivedByUpstream, copied)
				mu.Unlock()

				// Acknowledge FramebufferUpdateRequest (type 3) so test synchronizes deterministically without sleeps.
				if len(data) > 0 && data[0] == 3 {
					_ = conn.Write(r.Context(), websocket.MessageBinary, []byte{0, 0, 0, 1})
				}
			}
		}))
		defer upstreamSrv.Close()

		upstreamWS := "ws" + strings.TrimPrefix(upstreamSrv.URL, "http")

		r := chi.NewRouter()
		r.Get("/ws/bots/{workspaceID}/{botID}/vnc", vncrelay.NewRelayHandler(
			signer,
			func(_ context.Context, _, _ string) (string, error) { return upstreamWS, nil },
			discardLogger,
			&vncrelay.RelayOptions{Now: func() time.Time { return fixedNow }},
		))

		relaySrv := httptest.NewServer(r)
		defer relaySrv.Close()

		token, err := signer.Sign(vncrelay.TicketClaims{
			WorkspaceID: "ws-1",
			BotID:       "bot-atlas",
			UserID:      "usr-1",
			CanControl:  canControl,
			ExpiresAt:   fixedNow.Add(time.Minute),
		})
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		clientWS := "ws" + strings.TrimPrefix(relaySrv.URL, "http") + "/ws/bots/ws-1/bot-atlas/vnc?ticket=" + token
		conn, resp, err := websocket.Dial(ctx, clientWS, &websocket.DialOptions{Subprotocols: []string{"binary"}})
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if err != nil {
			t.Fatalf("Dial relay: %v", err)
		}
		defer func() { _ = conn.CloseNow() }()

		_, banner, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read server banner: %v", err)
		}
		if !bytes.Equal(banner, serverBanner) {
			t.Fatalf("banner = %q, want %q", banner, serverBanner)
		}

		keyEvent := []byte{4, 1, 0, 0, 0, 0, 0, 65}
		pointerEvent := []byte{5, 1, 0, 100, 0, 200}
		fbUpdateReq := []byte{3, 1, 0, 0, 0, 0, 5, 0, 3, 32}

		for _, frame := range [][]byte{keyEvent, pointerEvent, fbUpdateReq} {
			if err := conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
				t.Fatalf("write frame: %v", err)
			}
		}

		// Wait for upstream acknowledgment of the FramebufferUpdateRequest, which arrives after any preceding allowed frames.
		_, ack, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read upstream ack: %v", err)
		}
		if !bytes.Equal(ack, []byte{0, 0, 0, 1}) {
			t.Fatalf("unexpected ack: %v", ack)
		}

		_ = conn.Close(websocket.StatusNormalClosure, "done")

		mu.Lock()
		defer mu.Unlock()
		return append([][]byte(nil), receivedByUpstream...)
	}

	t.Run("controller forwards KeyEvent, PointerEvent, and FramebufferUpdateRequest", func(t *testing.T) {
		t.Parallel()
		got := runScenario(t, true)
		if len(got) != 3 {
			t.Fatalf("upstream received %d frames, want 3", len(got))
		}
	})

	t.Run("viewer drops KeyEvent and PointerEvent while keeping FramebufferUpdateRequest", func(t *testing.T) {
		t.Parallel()
		got := runScenario(t, false)
		if len(got) != 1 || len(got[0]) == 0 || got[0][0] != 3 {
			t.Fatalf("upstream received %v, expected only FramebufferUpdateRequest (type 3)", got)
		}
	})
}
