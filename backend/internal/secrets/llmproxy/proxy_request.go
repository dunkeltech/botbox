// SPDX-License-Identifier: AGPL-3.0-only

package llmproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

const maxModelBodyBytes = 8 << 20

func (h *proxyHandler) resolveUpstream(w http.ResponseWriter, r *http.Request, route proxyRoute) (*SecretConfig, *url.URL) {
	botID, provider, upstreamPath := route.botID, route.provider, route.path
	logger := h.options.Logger
	plan := provider == ProviderOpenAIPlan
	cfg, err := h.resolve(r.Context(), botID, provider)
	if plan && err != nil && !errors.Is(err, ErrSecretNotFound) {
		status, code, message := http.StatusServiceUnavailable, "plan_unavailable", "ChatGPT plan is temporarily unavailable"
		if errors.Is(err, ErrPlanReconnect) {
			status, code, message = http.StatusUnauthorized, "plan_reconnect", "reconnect the ChatGPT plan in bot.Box"
		}
		writeError(w, logger, status, code, message)
		return nil, nil
	}
	if err != nil || cfg == nil || cfg.SecretValue == "" || plan != (cfg.AuthKind == AuthKindOpenAIPlan) {
		h.audit(route, "blocked")
		writeError(w, logger, http.StatusForbidden, "forbidden", "provider credential not configured")
		return nil, nil
	}

	targetBase, err := url.Parse(cfg.UpstreamURL)
	if err != nil || targetBase.Scheme == "" || targetBase.Host == "" {
		h.audit(route, "upstream_error")
		writeError(w, logger, http.StatusBadGateway, "upstream_error", "invalid upstream configuration")
		return nil, nil
	}

	targetURL := *targetBase
	targetURL.Path = strings.TrimRight(targetBase.Path, "/") + upstreamPath
	targetURL.RawQuery = r.URL.RawQuery
	if prefix := strings.TrimRight(cfg.PathPrefix, "/"); prefix != "" && targetURL.Path != prefix && !strings.HasPrefix(targetURL.Path, prefix+"/") {
		writeError(w, logger, http.StatusForbidden, "forbidden", "credential does not allow this path")
		return nil, nil
	}

	if h.options.Admit != nil && provider == ProviderAnthropic {
		oauthPlaceholder := r.Header.Get("Authorization") == "Bearer botbox-placeholder" && r.Header.Get("x-api-key") == ""
		if oauthPlaceholder != (cfg.AuthKind == AuthKindClaudeOAuth) {
			// Credential rotation must not switch a running subscription CLI
			// to paid API billing without starting it in the matching mode.
			writeError(w, logger, 403, "auth_mode_changed", "provider authentication changed; start a new turn")
			return nil, nil
		}
	}
	return cfg, &targetURL
}

func (h *proxyHandler) buildUpstreamRequest(ctx context.Context, w http.ResponseWriter, r *http.Request, route proxyRoute, cfg *SecretConfig, targetURL *url.URL) *http.Request {
	logger := h.options.Logger
	outReq, err := http.NewRequestWithContext(ctx, r.Method, targetURL.String(), r.Body)
	if err != nil {
		h.audit(route, "upstream_error")
		writeError(w, logger, http.StatusBadGateway, "upstream_error", "failed to build upstream request")
		return nil
	}
	if route.provider == ProviderOpenAIPlan {
		outReq.Header.Set("Content-Type", "application/json")
		outReq.Header.Set("Accept", "text/event-stream")
		outReq.Header.Set("User-Agent", "bot.Box")
	} else {
		copyHeaders(outReq.Header, r.Header)
	}
	// Strip sandbox authorization before injecting the server-side credential.
	stripSandboxAuthHeaders(outReq.Header)
	outReq.Header.Del(h.options.BotIDHeader)
	// Claude Code asks for br and zstd. net/http only decompresses gzip
	// when it selected the encoding. A copied Accept-Encoding leaves the
	// body compressed, and the metered response keeps only Content-Type,
	// so the CLI parses gzip bytes as JSON.
	outReq.Header.Del("Accept-Encoding")

	if err := injectCredential(ctx, outReq.Header, cfg, h.options.OAuthExchanger); err != nil {
		logger.Warn("credential preparation failed",
			slog.String("bot_id", route.botID),
			slog.String("provider", route.provider),
			slog.String("path", route.path),
		)
		h.audit(route, "upstream_error")
		writeError(w, logger, http.StatusBadGateway, "upstream_error", "credential exchange failed")
		return nil
	}
	return outReq
}

func (h *proxyHandler) prepareModelBody(ctx context.Context, w http.ResponseWriter, outReq *http.Request, route proxyRoute) (string, []byte, bool) {
	metered := h.options.Admit != nil
	guardrails := h.options.Guardrails
	if metered && (route.method != http.MethodPost || !meteredPath(route.provider, route.path)) {
		writeError(w, h.options.Logger, http.StatusForbidden, "forbidden", "unsupported metered endpoint")
		return "", nil, false
	}
	if !metered && (guardrails == nil || route.method != http.MethodPost || outReq.Body == nil) {
		return "", nil, true
	}
	if outReq.Body == nil {
		writeError(w, h.options.Logger, http.StatusBadRequest, "validation_error", "invalid model request")
		return "", nil, false
	}
	body, err := io.ReadAll(io.LimitReader(outReq.Body, maxModelBodyBytes+1))
	if err != nil || len(body) > maxModelBodyBytes {
		writeError(w, h.options.Logger, http.StatusBadRequest, "validation_error", "invalid model request")
		return "", nil, false
	}
	var model struct {
		Model string `json:"model"`
	}
	modelErr := json.Unmarshal(body, &model)
	if metered && (modelErr != nil || model.Model == "" || len(model.Model) > 200) {
		writeError(w, h.options.Logger, http.StatusBadRequest, "validation_error", "model is required")
		return "", nil, false
	}
	if route.provider == ProviderOpenAIPlan {
		body, err = planRequest(body, model.Model)
		if err != nil {
			writeError(w, h.options.Logger, http.StatusBadRequest, "plan_request_unsupported", "Codex sent an unsupported ChatGPT plan request")
			return "", nil, false
		}
	}
	if guardrails != nil {
		decision, err := guardrails.EvaluateInput(ctx, route.provider, model.Model, route.path, body)
		if !h.allowGuardrailDecision(w, route, decision.Action, decision.Code, decision.Message, "LLM request blocked by workspace guardrail", err) {
			return "", nil, false
		}
		if decision.Action == "redacted" && len(decision.Body) > 0 {
			body = decision.Body
		}
	}
	outReq.Body = io.NopCloser(bytes.NewReader(body))
	outReq.ContentLength = int64(len(body))
	return model.Model, body, true
}

func (h *proxyHandler) admitRequest(ctx context.Context, w http.ResponseWriter, route proxyRoute, model string) (func(Usage) error, bool) {
	settle, err := h.options.Admit(ctx, route.provider, model, route.path)
	if err != nil {
		status, code, message := http.StatusServiceUnavailable, "budget_unavailable", "model request is blocked"
		if errors.Is(err, ErrBudgetExceeded) {
			status, code = http.StatusTooManyRequests, "budget_exceeded"
		}
		if errors.Is(err, ErrModelPriceRequired) {
			code, message = "model_price_required", "configure a price for the selected model"
		}
		if errors.Is(err, ErrPlanAccess) {
			status, code, message = http.StatusForbidden, "plan_forbidden", "ChatGPT plan is unavailable for this desktop"
		}
		writeError(w, h.options.Logger, status, code, message)
		return nil, false
	}
	if settle == nil {
		writeError(w, h.options.Logger, http.StatusServiceUnavailable, "budget_unavailable", "accounting unavailable")
		return nil, false
	}
	return settle, true
}
