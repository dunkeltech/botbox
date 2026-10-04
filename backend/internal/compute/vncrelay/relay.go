package vncrelay

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
)

const (
	rfbMsgKeyEvent      byte = 4
	rfbMsgPointerEvent  byte = 5
	rfbMsgClientCutText byte = 6
)

// UpstreamResolver returns the upstream websockify ws:// URL for a workspace bot.
type UpstreamResolver func(ctx context.Context, workspaceID, botID string) (string, error)

// RelayOptions configures optional clock and origin behavior for NewRelayHandler.
type RelayOptions struct {
	Now                    func() time.Time
	InsecureSkipOriginWait bool
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

		if resolveUpstream == nil {
			writeRelayError(w, logger, http.StatusBadGateway, "upstream_unavailable", "VNC upstream resolver unavailable")
			return
		}
		upstreamURL, err := resolveUpstream(r.Context(), workspaceID, botID)
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
		upstreamConn, resp, err := websocket.Dial(r.Context(), upstreamURL, &websocket.DialOptions{
			Subprotocols: []string{"binary"},
		})
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

		clientConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{"binary"},
		})
		if err != nil {
			logger.Warn("failed to accept VNC WebSocket client", slog.Any("error", err))
			return
		}
		defer func() {
			_ = clientConn.CloseNow()
		}()

		pump(r.Context(), clientConn, upstreamConn, claims.CanControl, logger)
	}
}

func pump(
	ctx context.Context,
	clientConn *websocket.Conn,
	upstreamConn *websocket.Conn,
	canControl bool,
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
		for {
			msgType, data, err := clientConn.Read(ctx)
			if err != nil {
				errCh <- err
				return
			}
			if !canControl && isBlockedViewerInput(data) {
				logger.Debug("dropped VNC viewer input frame because control lock is held elsewhere")
				continue
			}
			if err := upstreamConn.Write(ctx, msgType, data); err != nil {
				errCh <- err
				return
			}
		}
	}()

	if err := <-errCh; err != nil {
		logger.Debug("vnc relay ended", slog.String("reason", err.Error()))
	}
	cancel()
	_ = clientConn.Close(websocket.StatusNormalClosure, "relay closed")
	_ = upstreamConn.Close(websocket.StatusNormalClosure, "relay closed")
}

// isBlockedViewerInput drops RFB interactive messages (KeyEvent=4, PointerEvent=5,
// ClientCutText=6) sent by a view-only client while keeping handshake and screen-update frames.
func isBlockedViewerInput(frame []byte) bool {
	if len(frame) < 6 {
		return false
	}
	// RFB protocol version strings start with ASCII "RFB ", never a raw 4/5/6 opcode.
	switch frame[0] {
	case rfbMsgKeyEvent:
		return len(frame) >= 8
	case rfbMsgPointerEvent:
		return len(frame) >= 6
	case rfbMsgClientCutText:
		return len(frame) >= 8
	default:
		return false
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
