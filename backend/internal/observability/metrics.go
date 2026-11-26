// SPDX-License-Identifier: AGPL-3.0-only

package observability

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds Prometheus collectors for the bot.Box control plane and egress proxy.
type Metrics struct {
	Registry             *prometheus.Registry
	APIRequestDuration   *prometheus.HistogramVec
	TurnsTotal           *prometheus.CounterVec
	TokensTotal          *prometheus.CounterVec
	CostUSDTotal         *prometheus.CounterVec
	TurnQueueDepth       *prometheus.GaugeVec
	EgressDecisionsTotal *prometheus.CounterVec
	SandboxesByState     *prometheus.GaugeVec
	VNCSessionsActive    *prometheus.GaugeVec
}

// NewMetrics registers all bot.Box Prometheus metrics on a dedicated registry.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		Registry: reg,
		APIRequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "botbox_api_request_duration_seconds",
			Help:    "HTTP request latency in seconds grouped by method, chi route pattern, and status.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route", "status"}),
		TurnsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "botbox_turns_total",
			Help: "Total bot turns processed by final status.",
		}, []string{"workspace_id", "bot_id", "status"}),
		TokensTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "botbox_tokens_total",
			Help: "Total LLM tokens consumed by bot, provider, and direction.",
		}, []string{"workspace_id", "bot_id", "provider", "direction"}),
		CostUSDTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "botbox_cost_usd_total",
			Help: "Total metered LLM spend in USD by bot and provider.",
		}, []string{"workspace_id", "bot_id", "provider"}),
		TurnQueueDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "botbox_turn_queue_depth",
			Help: "Current number of queued bot turns per workspace.",
		}, []string{"workspace_id"}),
		EgressDecisionsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "botbox_egress_decisions_total",
			Help: "Total egress proxy decisions by action.",
		}, []string{"workspace_id", "bot_id", "action"}),
		SandboxesByState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "botbox_sandboxes_state",
			Help: "Current number of bot sandbox environments by lifecycle state.",
		}, []string{"state"}),
		VNCSessionsActive: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "botbox_vnc_sessions_active",
			Help: "Current number of active VNC relay WebSocket sessions per workspace.",
		}, []string{"workspace_id"}),
	}
	reg.MustRegister(
		m.APIRequestDuration,
		m.TurnsTotal,
		m.TokensTotal,
		m.CostUSDTotal,
		m.TurnQueueDepth,
		m.EgressDecisionsTotal,
		m.SandboxesByState,
		m.VNCSessionsActive,
	)
	return m
}

// Handler exposes Prometheus metrics for internal scraping only.
func (m *Metrics) Handler() http.Handler {
	if m == nil || m.Registry == nil {
		return http.NotFoundHandler()
	}
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}

// StartMetricsServer binds an internal-only HTTP server exposing GET /metrics.
// If addr is empty, no server is started and a no-op shutdown is returned.
func StartMetricsServer(ctx context.Context, addr string, m *Metrics, logger *slog.Logger) (func(context.Context) error, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" || m == nil {
		return func(context.Context) error { return nil }, nil
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", m.Handler())

	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if logger != nil {
			logger.Info("metrics server listening", "addr", lis.Addr().String())
		}
		if serveErr := srv.Serve(lis); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && logger != nil {
			logger.Error("metrics server failed", "err", serveErr)
		}
	}()
	return srv.Shutdown, nil
}
