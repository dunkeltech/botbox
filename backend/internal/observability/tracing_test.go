// SPDX-License-Identifier: AGPL-3.0-only

package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTracingPropagatesAcrossCarrier(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
		_ = tp.Shutdown(context.Background())
	})

	ctx, rootSpan := Tracer("test").Start(context.Background(), "api.request")
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	rootSpan.End()

	childCtx := otel.GetTextMapPropagator().Extract(context.Background(), carrier)
	_, childSpan := Tracer("test").Start(childCtx, "engine.process")
	childSpan.End()

	spans := exp.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(spans))
	}
	if spans[0].SpanContext.TraceID() != spans[1].SpanContext.TraceID() {
		t.Fatalf("trace ID mismatch: %s vs %s", spans[0].SpanContext.TraceID(), spans[1].SpanContext.TraceID())
	}
}

func TestMetricsExposeAllRequiredFamilies(t *testing.T) {
	t.Parallel()

	m := NewMetrics()
	m.APIRequestDuration.WithLabelValues("GET", "/api/healthz", "200").Observe(0.01)
	m.TurnsTotal.WithLabelValues("ws-1", "bot-1", "completed").Inc()
	m.TokensTotal.WithLabelValues("ws-1", "bot-1", "anthropic", "input").Add(100)
	m.CostUSDTotal.WithLabelValues("ws-1", "bot-1", "anthropic").Add(0.02)
	m.TurnQueueDepth.WithLabelValues("ws-1").Set(3)
	m.EgressDecisionsTotal.WithLabelValues("ws-1", "bot-1", "mitm_token_added").Inc()
	m.SandboxesByState.WithLabelValues("running").Set(2)
	m.VNCSessionsActive.WithLabelValues("ws-1").Set(1)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status=%d", rec.Code)
	}
	body := rec.Body.String()
	for _, metricName := range []string{
		"botbox_api_request_duration_seconds",
		"botbox_turns_total",
		"botbox_tokens_total",
		"botbox_cost_usd_total",
		"botbox_turn_queue_depth",
		"botbox_egress_decisions_total",
		"botbox_sandboxes_state",
		"botbox_vnc_sessions_active",
	} {
		if !strings.Contains(body, metricName) {
			t.Fatalf("metrics output missing %s", metricName)
		}
	}
}
