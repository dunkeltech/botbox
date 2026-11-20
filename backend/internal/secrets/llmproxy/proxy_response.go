// SPDX-License-Identifier: AGPL-3.0-only

package llmproxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
)

func (h *proxyHandler) forwardResponse(
	ctx context.Context,
	w http.ResponseWriter,
	resp *http.Response,
	route proxyRoute,
	model string,
	cfg *SecretConfig,
	metered bool,
) Usage {
	plan := route.provider == ProviderOpenAIPlan || cfg.AuthKind == AuthKindOpenAIPlan
	stream := isEventStream(resp.Header.Get("Content-Type"))
	if plan {
		stream = planEventStream(resp)
	}
	if plan && (resp.StatusCode != http.StatusOK || !stream) {
		writePlanError(w, h.options.Logger, resp, cfg.SecretValue)
		return Usage{Known: resp.StatusCode >= 300 && resp.StatusCode < 500}
	}
	if metered && resp.StatusCode >= 400 {
		// Provider error bodies may echo credentials or request headers.
		writeError(w, h.options.Logger, resp.StatusCode, "upstream_error", "model provider rejected request")
		return Usage{Known: resp.StatusCode < 500}
	}
	h.audit(route, "token_added")
	guarded := h.options.Guardrails != nil && resp.StatusCode >= 200 && resp.StatusCode < 300
	if guarded && !stream {
		return h.forwardGuardedJSON(ctx, w, resp, route, model, metered)
	}
	return h.forwardBody(ctx, w, resp, route, model, cfg.SecretValue, metered, stream, guarded, plan)
}

func (h *proxyHandler) forwardGuardedJSON(ctx context.Context, w http.ResponseWriter, resp *http.Response, route proxyRoute, model string, metered bool) Usage {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelBodyBytes+1))
	if err != nil || len(body) > maxModelBodyBytes {
		writeError(w, h.options.Logger, http.StatusBadGateway, "upstream_error", "upstream response exceeds maximum size")
		return Usage{}
	}
	collector := newUsageCollector(resp.Header.Get("Content-Type"))
	if metered {
		_, _ = collector.Write(body)
	}
	decision, err := h.options.Guardrails.EvaluateOutputJSON(ctx, route.provider, model, route.path, body)
	if !h.allowGuardrailDecision(w, route, decision.Action, decision.Code, decision.Message, "LLM response blocked by workspace guardrail", err) {
		if metered {
			return collector.finish(true)
		}
		return Usage{}
	}
	if decision.Action == "redacted" && len(decision.Body) > 0 {
		body = decision.Body
	}
	if metered {
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	} else {
		copyHeaders(w.Header(), resp.Header)
		w.Header().Del("Content-Length")
	}
	w.WriteHeader(resp.StatusCode)
	_, copyErr := w.Write(body)
	if !metered {
		return Usage{}
	}
	if copyErr == nil && route.path == "/v1/messages/count_tokens" && resp.StatusCode == http.StatusOK {
		return Usage{Known: true}
	}
	return collector.finish(copyErr == nil)
}

func (h *proxyHandler) forwardBody(
	ctx context.Context,
	w http.ResponseWriter,
	resp *http.Response,
	route proxyRoute,
	model string,
	planToken string,
	metered, stream, guarded, plan bool,
) Usage {
	contentType := resp.Header.Get("Content-Type")
	if metered {
		if plan {
			contentType = "text/event-stream"
		}
		w.Header().Set("Content-Type", contentType)
	} else {
		copyHeaders(w.Header(), resp.Header)
	}
	w.WriteHeader(resp.StatusCode)
	var destination io.Writer = w
	if stream {
		// Flush small chunks so clients see live activity and cancellation.
		destination = flushingWriter{w}
	}
	var guardCloser io.WriteCloser
	if guarded && stream {
		guardCloser = h.options.Guardrails.WrapSSEOutput(ctx, route.provider, model, route.path, destination)
		if guardCloser != nil {
			destination = guardCloser
		}
	}
	var source io.Reader = resp.Body
	collector := newUsageCollector(contentType)
	if metered {
		source = io.TeeReader(resp.Body, collector)
	}
	var copyErr error
	if plan {
		copyErr = copyPlanStream(destination, source, planToken)
	} else {
		_, copyErr = io.Copy(destination, source)
	}
	if guardCloser != nil {
		if closeErr := guardCloser.Close(); copyErr == nil {
			copyErr = closeErr
		}
	}
	if copyErr != nil {
		h.options.Logger.Debug("stream upstream response interrupted", slog.String("provider", route.provider))
	}
	if !metered {
		return Usage{}
	}
	if copyErr == nil && route.path == "/v1/messages/count_tokens" && resp.StatusCode == http.StatusOK {
		return Usage{Known: true}
	}
	// Codex may close the request after decoding the terminal event. A verified
	// provider usage report remains authoritative when only the connection fails.
	complete := copyErr == nil || (plan && collector.responsesUsageComplete)
	usage := collector.finish(complete && resp.StatusCode >= 200 && resp.StatusCode < 300)
	if plan && !usage.Known {
		h.options.Logger.Warn("ChatGPT plan usage not confirmed",
			"stream_interrupted", copyErr != nil, "terminal_event_seen", collector.terminal,
			"usage_report_seen", collector.responsesUsageComplete, "invalid_usage", collector.invalid)
	}
	return usage
}
