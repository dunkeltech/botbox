// SPDX-License-Identifier: AGPL-3.0-only

// Package api provides the HTTP router, health probes, and JSON error handling
// for the bot.Box control plane.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/auth"
	"github.com/dunkeltech/botbox/backend/internal/authbroker"
	"github.com/dunkeltech/botbox/backend/internal/codexplan"
	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/compute/vncrelay"
	"github.com/dunkeltech/botbox/backend/internal/mcpproxy"
	"github.com/dunkeltech/botbox/backend/internal/observability"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
)

// ReadinessCheck validates downstream dependencies such as Postgres and NATS.
type ReadinessCheck func(ctx context.Context) error

// Config configures the HTTP control plane handler.
type Config struct {
	ChatRateLimiter           *auth.RateLimiter
	ChatEvents                ChatEvents
	TurnQueue                 TurnQueue
	Logger                    *slog.Logger
	ReadinessCheck            ReadinessCheck
	VNCRelay                  http.Handler
	Compute                   compute.Driver
	AuthBroker                authbroker.Client
	TicketSigner              *vncrelay.Signer
	KeyRing                   *secrets.KeyRing
	Pool                      *pgxpool.Pool
	JWT                       *auth.JWTService
	AuthProvider              auth.Provider
	Argon2Params              auth.Argon2Params
	Setup                     *auth.SetupManager
	SetupRateLimiter          *auth.RateLimiter
	LoginIPRateLimiter        *auth.RateLimiter
	LoginEmailRateLimiter     *auth.RateLimiter
	RefreshRateLimiter        *auth.RateLimiter
	CookieSecure              bool
	PublicBaseURL             string
	TrustedProxyCIDRs         []netip.Prefix
	LoginLockoutThreshold     int
	LoginLockoutDuration      time.Duration
	EgressAckTimeout          time.Duration
	DefaultSandboxIdleTimeout time.Duration
	MaxRunningSandboxes       int
	MaxSandboxMemoryMiB       int
	MCPUpstream               *mcpproxy.UpstreamClient
	CodexPlanOAuth            *codexplan.OAuth
	Metrics                   *observability.Metrics
	Now                       func() time.Time
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
	_, handler := NewRuntimeHandler(cfg)
	return handler
}

// NewRuntimeHandler constructs the HTTP handler with optional WebSocket relays
// and returns a Runtime handle for background maintenance operations.
func NewRuntimeHandler(cfg Config) (*Runtime, http.Handler) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	httpsEnabled := strings.HasPrefix(strings.ToLower(strings.TrimSpace(cfg.PublicBaseURL)), "https://")
	r.Use(securityHeaders(httpsEnabled))
	r.Use(observabilityMiddleware(cfg.Metrics))

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
		relayOpts := &vncrelay.RelayOptions{
			Now:            srv.now,
			OriginPatterns: wsOriginPatterns(cfg.PublicBaseURL),
			Authorize:      srv.authorizeVNC,
		}
		if cfg.Metrics != nil {
			relayOpts.OnSessionStart = func(workspaceID string) {
				cfg.Metrics.VNCSessionsActive.WithLabelValues(workspaceID).Inc()
			}
			relayOpts.OnSessionEnd = func(workspaceID string) {
				cfg.Metrics.VNCSessionsActive.WithLabelValues(workspaceID).Dec()
			}
		}
		if _, ok := cfg.Compute.(compute.VNCDialer); ok {
			// The browser reaches this process, not the container bridge.
			// Docker Desktop cannot route that bridge from Go (ADR-0013).
			relayOpts.DialUpstream = srv.dialVNCUpstream
		}
		r.Method(http.MethodGet, "/ws/bots/{workspaceID}/{botID}/vnc", vncrelay.NewRelayHandler(
			srv.ticketSigner,
			srv.resolveVNCUpstream,
			logger,
			relayOpts,
		))
	}

	r.Get("/ws/chat/{workspaceID}/{conversationID}", func(w http.ResponseWriter, r *http.Request) {
		for _, protocol := range strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
			if token, ok := strings.CutPrefix(strings.TrimSpace(protocol), "botbox.auth."); ok {
				r.Header.Set("Authorization", "Bearer "+token)
			}
		}
		srv.requireAuth(http.HandlerFunc(srv.handleChatEvents)).ServeHTTP(w, r)
	})
	srv.mountRoutes(r)

	return &Runtime{srv: srv}, r
}

// wsOriginPatterns allows browser WebSocket connections from loopback during
// development and from the configured PublicBaseURL host in deployments.
func wsOriginPatterns(publicBaseURL string) []string {
	patterns := []string{"localhost", "localhost:*", "127.0.0.1", "127.0.0.1:*"}
	parsed, err := url.Parse(strings.TrimSpace(publicBaseURL))
	if err != nil || parsed.Host == "" {
		return patterns
	}
	host := strings.ToLower(parsed.Host)
	for _, existing := range patterns {
		// Same match the WebSocket library uses, so a loopback public URL
		// does not add a second entry next to localhost:* .
		matched, matchErr := path.Match(strings.ToLower(existing), host)
		if matchErr == nil && matched {
			return patterns
		}
	}
	return append(patterns, parsed.Host)
}

func vncOriginPatterns(publicBaseURL string) []string {
	return wsOriginPatterns(publicBaseURL)
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

func securityHeaders(httpsEnabled bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
			w.Header().Set("Cache-Control", "no-store")
			if httpsEnabled {
				w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

func observabilityMiddleware(metrics *observability.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			method := r.Method
			switch method {
			case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
				http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace:
			default:
				method = "_OTHER"
			}
			extracted := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			spanCtx, span := observability.Tracer("botbox/api").Start(extracted, method+" HTTP")
			defer span.End()

			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r.WithContext(spanCtx))

			route := "unmatched"
			if rctx := chi.RouteContext(spanCtx); rctx != nil {
				if pattern := rctx.RoutePattern(); pattern != "" {
					route = pattern
				}
			}
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			span.SetName(method + " " + route)
			span.SetAttributes(
				attribute.String("http.request.method", method),
				attribute.String("http.route", route),
				attribute.Int("http.response.status_code", status),
			)
			if status >= http.StatusInternalServerError {
				span.SetStatus(codes.Error, http.StatusText(status))
			}
			if metrics != nil {
				metrics.APIRequestDuration.WithLabelValues(method, route, strconv.Itoa(status)).Observe(time.Since(start).Seconds())
			}
		})
	}
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
