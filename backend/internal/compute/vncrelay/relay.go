// SPDX-License-Identifier: AGPL-3.0-only

package vncrelay

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
)

const (
	// websockify turns each TCP read into one WebSocket message. The library
	// default is 32 KiB, and the first 1280×800 framebuffer is already larger,
	// so the viewer socket closes and the UI stays on "Connecting…".
	// A raw frame is 4 MiB; the cap leaves room for one update.
	maxVNCMessage       = 8 << 20
	accessCheckInterval = 500 * time.Millisecond
)

// ErrAccessRevoked indicates that the ticket's holder lost workspace or desktop access.
var ErrAccessRevoked = errors.New("desktop access revoked")

// UpstreamResolver returns the upstream websockify ws:// URL for a workspace bot.
type UpstreamResolver func(ctx context.Context, workspaceID, botID string) (string, error)

// RelayOptions configures optional clock and origin behavior for NewRelayHandler.
type RelayOptions struct {
	Now func() time.Time
	// OriginPatterns lists extra browser origins allowed to open the viewer.
	// A proxy (Next or Caddy) makes the request Host the API address while the
	// page Origin stays on the site the human opened. The ticket is checked
	// before the upgrade, so this list is not the authorization check.
	OriginPatterns []string
	// DialUpstream opens the TCP connection to websockify. Set it when the
	// process cannot route the sandbox bridge address. The relay still
	// completes the WebSocket handshake on that connection.
	DialUpstream func(ctx context.Context, workspaceID, botID string) (net.Conn, error)
	// Authorize checks the current workspace membership and, for controllers,
	// the current desktop holder. Errors terminate the connection.
	Authorize func(ctx context.Context, claims TicketClaims) error
}

type jsonErrorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// NewRelayHandler returns an HTTP handler that verifies the signed ticket, connects
// to the sandbox websockify upstream, and enforces view-only input filtering.
func NewRelayHandler(
	signer *Signer,
	resolveUpstream UpstreamResolver,
	logger *slog.Logger,
	opts *RelayOptions,
) http.HandlerFunc {
	if logger == nil {
		logger = slog.Default()
	}
	nowFn := time.Now
	if opts != nil && opts.Now != nil {
		nowFn = opts.Now
	}

	return func(w http.ResponseWriter, r *http.Request) {
		workspaceID := chi.URLParam(r, "workspaceID")
		if workspaceID == "" {
			workspaceID = r.URL.Query().Get("workspace_id")
		}
		botID := chi.URLParam(r, "botID")
		if botID == "" {
			botID = r.URL.Query().Get("bot_id")
		}
		ticket := r.URL.Query().Get("ticket")

		if workspaceID == "" || botID == "" {
			writeRelayError(w, logger, http.StatusNotFound, "not_found", "bot not found")
			return
		}
		if ticket == "" || signer == nil {
			writeRelayError(w, logger, http.StatusUnauthorized, "unauthorized", "missing VNC ticket")
			return
		}

		claims, err := signer.Verify(ticket, workspaceID, botID, nowFn())
		if err != nil {
			switch {
			case errors.Is(err, ErrCrossWorkspace):
				writeRelayError(w, logger, http.StatusNotFound, "not_found", "bot not found")
			case errors.Is(err, ErrExpiredTicket), errors.Is(err, ErrInvalidTicket):
				writeRelayError(w, logger, http.StatusUnauthorized, "unauthorized", "invalid or expired VNC ticket")
			default:
				writeRelayError(w, logger, http.StatusUnauthorized, "unauthorized", "ticket verification failed")
			}
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), claims.ExpiresAt.Sub(nowFn()))
		defer cancel()
		var authorize func(context.Context) error
		if opts != nil && opts.Authorize != nil {
			authorize = func(checkCtx context.Context) error {
				checkCtx, stop := context.WithTimeout(checkCtx, accessCheckInterval)
				defer stop()
				return opts.Authorize(checkCtx, *claims)
			}
			if err := authorize(ctx); err != nil {
				if errors.Is(err, ErrAccessRevoked) {
					writeRelayError(w, logger, http.StatusNotFound, "not_found", "bot not found")
				} else {
					writeRelayError(w, logger, http.StatusServiceUnavailable, "dependency_unavailable", "desktop authorization unavailable")
				}
				return
			}
		}

		if resolveUpstream == nil {
			writeRelayError(w, logger, http.StatusBadGateway, "upstream_unavailable", "VNC upstream resolver unavailable")
			return
		}
		upstreamURL, err := resolveUpstream(ctx, workspaceID, botID)
		if err != nil || strings.TrimSpace(upstreamURL) == "" {
			logger.Warn("failed to resolve VNC upstream",
				slog.String("workspace_id", workspaceID),
				slog.String("bot_id", botID),
				slog.Any("error", err),
			)
			writeRelayError(w, logger, http.StatusBadGateway, "upstream_unavailable", "sandbox desktop unavailable")
			return
		}

		// Dial upstream before accepting the client upgrade so unreachable containers return 502 cleanly.
		// A bridge URL is not routed from this process on Docker Desktop, so DialUpstream
		// carries a stream that already ends at 127.0.0.1:6080 inside the sandbox.
		var upstreamConn *websocket.Conn
		var resp *http.Response
		if opts != nil && opts.DialUpstream != nil {
			raw, dialErr := opts.DialUpstream(ctx, workspaceID, botID)
			if dialErr != nil {
				logger.Warn("failed to open VNC tunnel",
					slog.String("workspace_id", workspaceID),
					slog.String("bot_id", botID),
					slog.Any("error", dialErr),
				)
				writeRelayError(w, logger, http.StatusBadGateway, "upstream_unavailable", "sandbox desktop unreachable")
				return
			}
			upstreamConn, resp, err = dialTunneled(ctx, raw)
		} else {
			upstreamConn, resp, err = websocket.Dial(ctx, upstreamURL, &websocket.DialOptions{
				Subprotocols: []string{"binary"},
			})
		}
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if err != nil {
			logger.Warn("failed to dial VNC upstream",
				slog.String("workspace_id", workspaceID),
				slog.String("bot_id", botID),
				slog.Any("error", err),
			)
			writeRelayError(w, logger, http.StatusBadGateway, "upstream_unavailable", "sandbox desktop unreachable")
			return
		}
		defer func() {
			_ = upstreamConn.CloseNow()
		}()

		var origins []string
		if opts != nil {
			origins = opts.OriginPatterns
		}
		// Accept compares Origin to this request's Host. After a proxy those
		// differ, and the browser then stays on "Connecting…".
		clientConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols:   []string{"binary"},
			OriginPatterns: origins,
		})
		if err != nil {
			logger.Warn("failed to accept VNC WebSocket client", slog.Any("error", err))
			return
		}
		defer func() {
			_ = clientConn.CloseNow()
		}()

		upstreamConn.SetReadLimit(maxVNCMessage)
		clientConn.SetReadLimit(maxVNCMessage)
		pump(ctx, clientConn, upstreamConn, claims.CanControl, authorize, logger)
	}
}

// dialTunneled finishes the websockify handshake on a connection that is already
// attached to 127.0.0.1:6080. The URL host is not dialed.
func dialTunneled(ctx context.Context, raw net.Conn) (*websocket.Conn, *http.Response, error) {
	used := false
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			if used {
				return nil, errors.New("desktop tunnel already open")
			}
			used = true
			return raw, nil
		},
		DisableKeepAlives: true,
	}}
	conn, resp, err := websocket.Dial(ctx, "ws://127.0.0.1:6080/", &websocket.DialOptions{
		Subprotocols: []string{"binary"},
		HTTPClient:   client,
	})
	if err != nil && !used {
		_ = raw.Close()
	}
	return conn, resp, err
}

func pump(
	ctx context.Context,
	clientConn *websocket.Conn,
	upstreamConn *websocket.Conn,
	canControl bool,
	authorize func(context.Context) error,
	logger *slog.Logger,
) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 2)

	// Upstream -> client (always permitted for both controller and watchers)
	go func() {
		for {
			msgType, data, err := upstreamConn.Read(ctx)
			if err != nil {
				errCh <- err
				return
			}
			if err := clientConn.Write(ctx, msgType, data); err != nil {
				errCh <- err
				return
			}
		}
	}()

	// Client -> upstream (server-side input enforcement when canControl is false)
	go func() {
		if !canControl {
			// RFB is a byte stream. NetConn joins the WebSocket payloads before
			// the viewer allowlist inspects and forwards complete messages.
			stream := websocket.NetConn(ctx, clientConn, websocket.MessageBinary)
			clientConn.SetReadLimit(maxVNCMessage)
			errCh <- forwardViewerMessages(stream, func(data []byte) error {
				return upstreamConn.Write(ctx, websocket.MessageBinary, data)
			})
			return
		}
		for {
			msgType, data, err := clientConn.Read(ctx)
			if err != nil {
				errCh <- err
				return
			}
			if authorize != nil {
				if err := authorize(ctx); err != nil {
					errCh <- err
					return
				}
			}
			if err := upstreamConn.Write(ctx, msgType, data); err != nil {
				errCh <- err
				return
			}
		}
	}()

	if err := awaitRelayEnd(ctx, errCh, authorize); err != nil {
		logger.Debug("vnc relay ended", slog.String("reason", err.Error()))
	}
	cancel()
	_ = clientConn.Close(websocket.StatusNormalClosure, "relay closed")
	_ = upstreamConn.Close(websocket.StatusNormalClosure, "relay closed")
}

func awaitRelayEnd(ctx context.Context, errCh <-chan error, authorize func(context.Context) error) error {
	var recheck <-chan time.Time
	if authorize != nil {
		ticker := time.NewTicker(accessCheckInterval)
		defer ticker.Stop()
		recheck = ticker.C
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errCh:
			return err
		case <-recheck:
			if err := authorize(ctx); err != nil {
				return err
			}
		}
	}
}

func writeRelayError(w http.ResponseWriter, logger *slog.Logger, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	var env jsonErrorEnvelope
	env.Error.Code = code
	env.Error.Message = message
	if err := json.NewEncoder(w).Encode(env); err != nil {
		logger.Error("encode relay error response failed", slog.Any("error", err))
	}
}
