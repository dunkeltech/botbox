// Package api provides the HTTP router, health probes, and JSON error handling
// for the bot.Box control plane.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/auth"
	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/compute/vncrelay"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReadinessCheck validates downstream dependencies such as Postgres and NATS.
type ReadinessCheck func(ctx context.Context) error

// Config configures the HTTP control plane handler.
type Config struct {
	Logger                *slog.Logger
	ReadinessCheck        ReadinessCheck
	VNCRelay              http.Handler
	Compute               compute.Driver
	TicketSigner          *vncrelay.Signer
	Pool                  *pgxpool.Pool
	JWT                   *auth.JWTService
	AuthProvider          auth.Provider
	Argon2Params          auth.Argon2Params
	Setup                 *auth.SetupManager
	SetupRateLimiter      *auth.RateLimiter
	LoginIPRateLimiter    *auth.RateLimiter
	LoginEmailRateLimiter *auth.RateLimiter
	RefreshRateLimiter    *auth.RateLimiter
	CookieSecure          bool
	PublicBaseURL         string
	Now                   func() time.Time
}

type healthResponse struct {
	Status string `json:"status"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

// NewHandler constructs the HTTP handler for the control plane API.
// If logger is nil, slog.Default() is used. If readinessCheck is nil, /api/healthz
// treats local process liveliness as ready.
func NewHandler(logger *slog.Logger, readinessCheck ReadinessCheck) http.Handler {
	return NewHandlerWithConfig(Config{
		Logger:         logger,
		ReadinessCheck: readinessCheck,
	})
}

// NewHandlerWithConfig constructs the HTTP handler with optional WebSocket relays.
func NewHandlerWithConfig(cfg Config) http.Handler {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, logger, http.StatusNotFound, "not_found", "resource not found")
	})

	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, logger, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	})

	srv := newServer(cfg, logger)

	r.Get("/api/healthz", healthHandler(logger, cfg.ReadinessCheck))
	if cfg.VNCRelay != nil {
		r.Method(http.MethodGet, "/ws/bots/{workspaceID}/{botID}/vnc", cfg.VNCRelay)
	} else {
		r.Method(http.MethodGet, "/ws/bots/{workspaceID}/{botID}/vnc", vncrelay.NewRelayHandler(
			srv.ticketSigner,
			srv.resolveVNCUpstream,
			logger,
			&vncrelay.RelayOptions{
				Now:                    srv.now,
				InsecureSkipOriginWait: true,
			},
		))
	}

	srv.mountRoutes(r)

	return r
}

func healthHandler(logger *slog.Logger, readinessCheck ReadinessCheck) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if err := ctx.Err(); err != nil {
			logger.Warn("health check request canceled", slog.Any("error", err))
			writeError(w, logger, http.StatusServiceUnavailable, "dependency_unavailable", "request canceled")
			return
		}

		if readinessCheck != nil {
			if err := readinessCheck(ctx); err != nil {
				logger.Warn("readiness check failed", slog.Any("error", err))
				writeError(w, logger, http.StatusServiceUnavailable, "dependency_unavailable", "service dependency unavailable")
				return
			}
		}

		writeJSON(w, logger, http.StatusOK, healthResponse{Status: "ok"})
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, logger *slog.Logger, status int, code, message string) {
	writeJSON(w, logger, status, errorEnvelope{
		Error: errorBody{
			Code:    code,
			Message: message,
		},
	})
}

func writeJSON(w http.ResponseWriter, logger *slog.Logger, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		logger.Error("failed to encode JSON response", slog.Any("error", err))
	}
}
