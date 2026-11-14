package vncrelay_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

func TestRelayRechecksDesktopAccess(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	signer := testSigner(t)
	ticket, err := signer.Sign(vncrelay.TicketClaims{
		WorkspaceID: "ws", BotID: "bot", UserID: "user", ExpiresAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		if err := conn.Write(r.Context(), websocket.MessageBinary, []byte("RFB 003.008\n")); err != nil {
			return
		}
		_, _, _ = conn.Read(r.Context())
	}))
	defer upstream.Close()

	var allowed atomic.Bool
	allowed.Store(true)
	relay := httptest.NewServer(vncrelay.NewRelayHandler(signer, func(context.Context, string, string) (string, error) {
		return "ws" + strings.TrimPrefix(upstream.URL, "http"), nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), &vncrelay.RelayOptions{
		Now: func() time.Time { return now },
		Authorize: func(_ context.Context, claims vncrelay.TicketClaims) error {
			if !allowed.Load() || claims.UserID != "user" {
				return vncrelay.ErrAccessRevoked
			}
			return nil
		},
	}))
	defer relay.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(relay.URL, "http") + "?workspace_id=ws&bot_id=bot&ticket=" + ticket
	conn, resp, err := websocket.Dial(ctx, url, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatal(err)
	}
	allowed.Store(false)
	if _, _, err := conn.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("revoked desktop socket must close promptly: %v", err)
	}
	_, resp, err = websocket.Dial(ctx, url, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatal("desktop access was admitted after revocation")
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

func TestRelayUsesTunnelInsteadOfUnroutableURL(t *testing.T) {
	t.Parallel()
	fixedNow := time.Date(2026, 11, 14, 22, 0, 0, 0, time.UTC)
	signer := testSigner(t)
	banner := []byte("RFB 003.008\n")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		_ = conn.Write(r.Context(), websocket.MessageBinary, banner)
		_, _, _ = conn.Read(r.Context())
	}))
	t.Cleanup(upstream.Close)

	r := chi.NewRouter()
	r.Get("/ws/bots/{workspaceID}/{botID}/vnc", vncrelay.NewRelayHandler(
		signer,
		func(context.Context, string, string) (string, error) {
			// This address is not routed. The tunnel must be the connection.
			return "ws://192.0.2.1:6080", nil
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		&vncrelay.RelayOptions{
			Now: func() time.Time { return fixedNow },
			DialUpstream: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "tcp", upstream.Listener.Addr().String())
			},
		},
	))
	relay := httptest.NewServer(r)
	t.Cleanup(relay.Close)
	token, err := signer.Sign(vncrelay.TicketClaims{
		WorkspaceID: "ws-1", BotID: "bot-atlas", UserID: "usr-1", CanControl: true,
		ExpiresAt: fixedNow.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clientWS := "ws" + strings.TrimPrefix(relay.URL, "http") + "/ws/bots/ws-1/bot-atlas/vnc?ticket=" + token
	conn, resp, err := websocket.Dial(ctx, clientWS, &websocket.DialOptions{Subprotocols: []string{"binary"}})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("Dial relay: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()
	_, got, err := conn.Read(ctx)
	if err != nil || !bytes.Equal(got, banner) {
		t.Fatalf("banner %q err=%v", got, err)
	}
}

func TestRelayTunnelFailureIsBadGateway(t *testing.T) {
	t.Parallel()
	fixedNow := time.Date(2026, 11, 14, 22, 0, 0, 0, time.UTC)
	signer := testSigner(t)
	token, err := signer.Sign(vncrelay.TicketClaims{
		WorkspaceID: "ws-1", BotID: "bot-atlas", UserID: "usr-1", CanControl: false,
		ExpiresAt: fixedNow.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := vncrelay.NewRelayHandler(
		signer,
		func(context.Context, string, string) (string, error) { return "ws://127.0.0.1:1", nil },
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		&vncrelay.RelayOptions{
			Now: func() time.Time { return fixedNow },
			DialUpstream: func(context.Context, string, string) (net.Conn, error) {
				return nil, errors.New("tunnel down")
			},
		},
	)
	r := chi.NewRouter()
	r.Get("/ws/bots/{workspaceID}/{botID}/vnc", handler)
	req := httptest.NewRequest(http.MethodGet, "/ws/bots/ws-1/bot-atlas/vnc?ticket="+token, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "upstream_unavailable") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRelayAllowsProxiedPageOriginAndRejectsOtherSites(t *testing.T) {
	t.Parallel()
	fixedNow := time.Date(2026, 11, 14, 22, 0, 0, 0, time.UTC)
	signer := testSigner(t)
	banner := []byte("RFB 003.008\n")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols:       []string{"binary"},
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		_ = conn.Write(r.Context(), websocket.MessageBinary, banner)
		_, _, _ = conn.Read(r.Context())
	}))
	t.Cleanup(upstream.Close)

	r := chi.NewRouter()
	r.Get("/ws/bots/{workspaceID}/{botID}/vnc", vncrelay.NewRelayHandler(
		signer,
		func(context.Context, string, string) (string, error) { return "ws://192.0.2.1:6080", nil },
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		&vncrelay.RelayOptions{
			Now:            func() time.Time { return fixedNow },
			OriginPatterns: []string{"localhost:*", "127.0.0.1:*"},
			DialUpstream: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "tcp", upstream.Listener.Addr().String())
			},
		},
	))
	relay := httptest.NewServer(r)
	t.Cleanup(relay.Close)
	token, err := signer.Sign(vncrelay.TicketClaims{
		WorkspaceID: "ws-1", BotID: "bot-atlas", UserID: "usr-1", CanControl: true,
		ExpiresAt: fixedNow.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	base := "ws" + strings.TrimPrefix(relay.URL, "http") + "/ws/bots/ws-1/bot-atlas/vnc?ticket=" + token
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, resp, err := websocket.Dial(ctx, base, &websocket.DialOptions{
		Subprotocols: []string{"binary"},
		Host:         "127.0.0.1:8080",
		HTTPHeader:   http.Header{"Origin": []string{"http://localhost:3000"}},
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("proxied origin: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()
	_, got, err := conn.Read(ctx)
	if err != nil || !bytes.Equal(got, banner) {
		t.Fatalf("banner %q err=%v", got, err)
	}

	_, resp, err = websocket.Dial(ctx, base, &websocket.DialOptions{
		Subprotocols: []string{"binary"},
		Host:         "127.0.0.1:8080",
		HTTPHeader:   http.Header{"Origin": []string{"https://evil.example"}},
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("evil origin status=%d err=%v", status, err)
	}
}

func TestRelayForwardsMessageLargerThanDefaultLimit(t *testing.T) {
	t.Parallel()
	fixedNow := time.Date(2026, 11, 14, 22, 0, 0, 0, time.UTC)
	signer := testSigner(t)
	// websockify sends one TCP read as one WebSocket message. The first
	// framebuffer is already past the library default of 32 KiB.
	payload := bytes.Repeat([]byte{0x07}, 40<<10)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols:       []string{"binary"},
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		conn.SetReadLimit(1 << 20)
		_ = conn.Write(r.Context(), websocket.MessageBinary, payload)
		_, _, _ = conn.Read(r.Context())
	}))
	t.Cleanup(upstream.Close)

	r := chi.NewRouter()
	r.Get("/ws/bots/{workspaceID}/{botID}/vnc", vncrelay.NewRelayHandler(
		signer,
		func(context.Context, string, string) (string, error) {
			return "ws" + strings.TrimPrefix(upstream.URL, "http"), nil
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		&vncrelay.RelayOptions{Now: func() time.Time { return fixedNow }},
	))
	relay := httptest.NewServer(r)
	t.Cleanup(relay.Close)
	token, err := signer.Sign(vncrelay.TicketClaims{
		WorkspaceID: "ws-1", BotID: "bot-atlas", UserID: "usr-1", CanControl: true,
		ExpiresAt: fixedNow.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clientWS := "ws" + strings.TrimPrefix(relay.URL, "http") + "/ws/bots/ws-1/bot-atlas/vnc?ticket=" + token
	conn, resp, err := websocket.Dial(ctx, clientWS, &websocket.DialOptions{Subprotocols: []string{"binary"}})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("Dial relay: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(1 << 20)
	_, got, err := conn.Read(ctx)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("len=%d err=%v", len(got), err)
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

			for i, response := range [][]byte{{1, 1}, {0, 0, 0, 0}, {0, 1, 0, 1}} {
				_, data, err := conn.Read(r.Context())
				if err != nil || (i == 0 && !bytes.Equal(data, serverBanner)) || (i > 0 && !bytes.Equal(data, []byte{1})) {
					return
				}
				if err := conn.Write(r.Context(), websocket.MessageBinary, response); err != nil {
					return
				}
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
		for _, frame := range [][]byte{serverBanner, {1}, {1}} {
			if err := conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
				t.Fatalf("write handshake: %v", err)
			}
			if _, _, err := conn.Read(ctx); err != nil {
				t.Fatalf("read handshake: %v", err)
			}
		}

		keyEvent := []byte{4, 1, 0, 0, 0, 0, 0, 65}
		pointerEvent := []byte{5, 1, 0, 100, 0, 200}
		fbUpdateReq := []byte{3, 1, 0, 0, 0, 0, 5, 0, 3, 32}

		frames := [][]byte{keyEvent, pointerEvent, fbUpdateReq}
		if !canControl {
			frames = [][]byte{keyEvent, pointerEvent, fbUpdateReq[:4], fbUpdateReq[4:]}
		}
		for _, frame := range frames {
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
