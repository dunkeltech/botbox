// Package llmproxy implements Spike S2: injecting real LLM credentials outside
// the sandbox so CLIs (claude, codex, agy) only ever hold placeholder tokens.
package llmproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// ErrSecretNotFound indicates no credential is configured for the bot and provider.
var ErrSecretNotFound = errors.New("provider secret not configured")

// AuthMode reports the placeholder the sandbox CLI must present.
// The result is oauth, api_key, or none. Credential material is never returned.
func AuthMode(ctx context.Context, resolve SecretResolver, botID, provider string) (string, error) {
	if resolve == nil {
		return "", errors.New("credential resolver unavailable")
	}
	cfg, err := resolve(ctx, botID, provider)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return "none", nil
		}
		return "", err
	}
	if cfg == nil || cfg.SecretValue == "" {
		return "none", nil
	}
	switch cfg.AuthKind {
	case AuthKindClaudeOAuth, AuthKindOpenAIPlan:
		return "oauth", nil
	case AuthKindAnthropicKey, AuthKindBearer:
		return "api_key", nil
	default:
		return "none", nil
	}
}

// Supported upstream provider route prefixes and credential injection strategies.
const (
	ProviderAnthropic  = "anthropic"
	ProviderOpenAI     = "openai"
	ProviderOpenAIPlan = "openai-plan"
	ProviderGoogle     = "google"
	ProviderOpenRouter = "openrouter"

	AuthKindAnthropicKey = "anthropic_key"
	AuthKindBearer       = "bearer"
	AuthKindClaudeOAuth  = "claude_oauth"
	AuthKindOpenAIPlan   = "openai_plan"
	AuthKindOAuthRefresh = "oauth_refresh"
)

// SecretConfig holds the upstream base URL and credential material stored outside the sandbox.
type SecretConfig struct {
	// PathPrefix restricts the full upstream path, including any configured base path.
	PathPrefix   string
	UpstreamURL  string
	AuthKind     string
	SecretValue  string
	PlanRevision string
}

// SecretResolver fetches a bot's credential for a specific LLM provider.
type SecretResolver func(ctx context.Context, botID, provider string) (*SecretConfig, error)

// OAuthExchanger exchanges an OAuth2 refresh token for a short-lived access token.
type OAuthExchanger func(ctx context.Context, refreshToken string) (string, error)

// AuditEvent captures egress proxy metadata without ever including secret values.
type AuditEvent struct {
	BotID    string
	Provider string
	Method   string
	Path     string
	Result   string
}

// GuardrailInputDecision reports the outcome of evaluating an outbound LLM request body.
type GuardrailInputDecision struct {
	Action  string
	Code    string
	Message string
	Body    []byte
}

// GuardrailOutputDecision reports the outcome of evaluating a non-streaming LLM response body.
type GuardrailOutputDecision struct {
	Action  string
	Code    string
	Message string
	Body    []byte
}

// GuardrailEnforcer inspects and optionally redacts or blocks LLM requests and responses.
type GuardrailEnforcer interface {
	EvaluateInput(ctx context.Context, provider, model, routePath string, body []byte) (GuardrailInputDecision, error)
	EvaluateOutputJSON(ctx context.Context, provider, model, routePath string, body []byte) (GuardrailOutputDecision, error)
	WrapSSEOutput(ctx context.Context, provider, model, routePath string, dst io.Writer) io.WriteCloser
}

// Options configures optional dependencies for NewHandler.
type Options struct {
	// Admit reserves accounting before forwarding and returns a settlement function.
	// Settlement must persist even after cancellation and release its request lock.
	Admit func(context.Context, string, string, string) (func(Usage) error, error)

	// CheckBudget must atomically check all applicable limits before each request.
	CheckBudget    func(context.Context, string, string) error
	Guardrails     GuardrailEnforcer
	workspaceID    string
	botID          string
	BotIDHeader    string
	Client         *http.Client
	OAuthExchanger OAuthExchanger
	OnAudit        func(AuditEvent)
	Logger         *slog.Logger
}

type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// NewHandler builds an HTTP handler routing /llm/{provider}/* to the real upstream
// while stripping placeholder auth headers and injecting real secrets server-side.
func NewHandler(resolve SecretResolver, opts *Options) http.Handler {
	botHeader := "X-Botbox-Bot-ID"
	client := http.DefaultClient
	var exchange OAuthExchanger
	var onAudit func(AuditEvent)
	logger := slog.Default()

	if opts != nil {
		if opts.BotIDHeader != "" {
			botHeader = opts.BotIDHeader
		}
		if opts.Client != nil {
			client = opts.Client
		}
		exchange = opts.OAuthExchanger
		onAudit = opts.OnAudit
		if opts.Logger != nil {
			logger = opts.Logger
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provider, upstreamPath, ok := parseLLMPath(r.URL.Path)
		if !ok || !isSupportedProvider(provider) {
			emitAudit(onAudit, r.Header.Get(botHeader), provider, r.Method, r.URL.Path, "blocked")
			writeError(w, logger, http.StatusForbidden, "forbidden", "unsupported LLM provider")
			return
		}

		plan := provider == ProviderOpenAIPlan
		if plan && (opts == nil || opts.workspaceID == "" || opts.Admit == nil ||
			r.Method != http.MethodPost || !meteredPath(provider, upstreamPath) ||
			r.URL.RawQuery != "" || r.Header.Get("Upgrade") != "" || r.Header.Get("Authorization") != "Bearer botbox-placeholder") {
			writeError(w, logger, http.StatusForbidden, "forbidden", "unsupported ChatGPT plan request")
			return
		}

		if resolve == nil {
			writeError(w, logger, http.StatusForbidden, "forbidden", "credential resolver unavailable")
			return
		}

		botID := r.Header.Get(botHeader)
		if opts != nil && opts.botID != "" {
			botID = opts.botID
		}
		if botID == "" {
			botID = "default"
		}

		if opts != nil && opts.CheckBudget != nil {
			if err := opts.CheckBudget(r.Context(), opts.workspaceID, botID); err != nil {
				statusCode, code, message := http.StatusServiceUnavailable, "budget_unavailable", "budget service unavailable"
				if errors.Is(err, ErrBudgetExceeded) {
					statusCode, code, message = http.StatusTooManyRequests, "budget_exceeded", "budget exhausted"
				}
				emitAudit(onAudit, botID, provider, r.Method, upstreamPath, code)
				writeError(w, logger, statusCode, code, message)
				return
			}
		}

		cfg, err := resolve(r.Context(), botID, provider)
		if plan && err != nil && !errors.Is(err, ErrSecretNotFound) {
			status, code, message := http.StatusServiceUnavailable, "plan_unavailable", "ChatGPT plan is temporarily unavailable"
			if errors.Is(err, ErrPlanReconnect) {
				status, code, message = http.StatusUnauthorized, "plan_reconnect", "reconnect the ChatGPT plan in bot.Box"
			}
			writeError(w, logger, status, code, message)
			return
		}
		if err != nil || cfg == nil || cfg.SecretValue == "" || plan != (cfg.AuthKind == AuthKindOpenAIPlan) {
			emitAudit(onAudit, botID, provider, r.Method, upstreamPath, "blocked")
			writeError(w, logger, http.StatusForbidden, "forbidden", "provider credential not configured")
			return
		}

		targetBase, err := url.Parse(cfg.UpstreamURL)
		if err != nil || targetBase.Scheme == "" || targetBase.Host == "" {
			emitAudit(onAudit, botID, provider, r.Method, upstreamPath, "upstream_error")
			writeError(w, logger, http.StatusBadGateway, "upstream_error", "invalid upstream configuration")
			return
		}

		targetURL := *targetBase
		targetURL.Path = strings.TrimRight(targetBase.Path, "/") + upstreamPath
		targetURL.RawQuery = r.URL.RawQuery
		if prefix := strings.TrimRight(cfg.PathPrefix, "/"); prefix != "" && targetURL.Path != prefix && !strings.HasPrefix(targetURL.Path, prefix+"/") {
			writeError(w, logger, http.StatusForbidden, "forbidden", "credential does not allow this path")
			return
		}

		if opts != nil && opts.Admit != nil && provider == ProviderAnthropic {
			oauthPlaceholder := r.Header.Get("Authorization") == "Bearer botbox-placeholder" && r.Header.Get("x-api-key") == ""
			if oauthPlaceholder != (cfg.AuthKind == AuthKindClaudeOAuth) {
				// Credential rotation must not switch a running subscription CLI
				// to paid API billing without starting it in the matching mode.
				writeError(w, logger, 403, "auth_mode_changed", "provider authentication changed; start a new turn")
				return
			}
		}
		// Security invariant: copy headers and strip all sandbox placeholder auth headers
		// before adding real credentials so placeholders never reach upstream providers.
		requestCtx := context.WithValue(r.Context(), subscriptionContextKey{}, cfg.AuthKind == AuthKindClaudeOAuth || plan)
		if plan {
			requestCtx = context.WithValue(requestCtx, planRevisionContextKey{}, cfg.PlanRevision)
		}
		outReq, err := http.NewRequestWithContext(requestCtx, r.Method, targetURL.String(), r.Body)
		if err != nil {
			emitAudit(onAudit, botID, provider, r.Method, upstreamPath, "upstream_error")
			writeError(w, logger, http.StatusBadGateway, "upstream_error", "failed to build upstream request")
			return
		}
		if plan {
			outReq.Header.Set("Content-Type", "application/json")
			outReq.Header.Set("Accept", "text/event-stream")
			outReq.Header.Set("User-Agent", "bot.Box")
		} else {
			copyHeaders(outReq.Header, r.Header)
		}
		stripSandboxAuthHeaders(outReq.Header)
		outReq.Header.Del(botHeader)
		// Claude Code asks for br and zstd. net/http only decompresses gzip
		// when it selected the encoding. A copied Accept-Encoding leaves the
		// body compressed, and the metered response keeps only Content-Type,
		// so the CLI parses gzip bytes as JSON.
		outReq.Header.Del("Accept-Encoding")

		if err := injectCredential(r.Context(), outReq.Header, cfg, exchange); err != nil {
			logger.Warn("credential preparation failed",
				slog.String("bot_id", botID),
				slog.String("provider", provider),
				slog.String("path", upstreamPath),
			)
			emitAudit(onAudit, botID, provider, r.Method, upstreamPath, "upstream_error")
			writeError(w, logger, http.StatusBadGateway, "upstream_error", "credential exchange failed")
			return
		}

		var settle func(Usage) error
		var requestModel string
		if opts != nil && opts.Admit != nil {
			if r.Method != http.MethodPost || !meteredPath(provider, upstreamPath) {
				writeError(w, logger, 403, "forbidden", "unsupported metered endpoint")
				return
			}
			body, bodyErr := io.ReadAll(io.LimitReader(outReq.Body, (8<<20)+1))
			if bodyErr != nil || len(body) > 8<<20 {
				writeError(w, logger, 400, "validation_error", "invalid model request")
				return
			}
			var model struct {
				Model string `json:"model"`
			}
			if json.Unmarshal(body, &model) != nil || model.Model == "" || len(model.Model) > 200 {
				writeError(w, logger, 400, "validation_error", "model is required")
				return
			}
			requestModel = model.Model
			if plan {
				body, bodyErr = planRequest(body, model.Model)
				if bodyErr != nil {
					writeError(w, logger, http.StatusBadRequest, "plan_request_unsupported", "Codex sent an unsupported ChatGPT plan request")
					return
				}
			}
			if opts.Guardrails != nil {
				dec, gErr := opts.Guardrails.EvaluateInput(requestCtx, provider, requestModel, upstreamPath, body)
				if gErr != nil {
					emitAudit(onAudit, botID, provider, r.Method, upstreamPath, "guardrail_unavailable")
					writeError(w, logger, http.StatusForbidden, "guardrail_unavailable", "LLM guardrail evaluation failed closed")
					return
				}
				if dec.Action == "blocked" {
					code := dec.Code
					if code == "" {
						code = "guardrail_blocked"
					}
					msg := dec.Message
					if msg == "" {
						msg = "LLM request blocked by workspace guardrail"
					}
					emitAudit(onAudit, botID, provider, r.Method, upstreamPath, "guardrail_blocked")
					writeError(w, logger, http.StatusForbidden, code, msg)
					return
				}
				if dec.Action == "redacted" && len(dec.Body) > 0 {
					body = dec.Body
				}
			}
			outReq.Body = io.NopCloser(bytes.NewReader(body))
			outReq.ContentLength = int64(len(body))
			requestCtx = boundedRequest(requestCtx, provider, upstreamPath, body)
			settle, err = opts.Admit(requestCtx, provider, model.Model, upstreamPath)
			if err != nil {
				status := http.StatusServiceUnavailable
				code := "budget_unavailable"
				if errors.Is(err, ErrBudgetExceeded) {
					status = http.StatusTooManyRequests
					code = "budget_exceeded"
				}
				message := "model request is blocked"
				if errors.Is(err, ErrModelPriceRequired) {
					code, message = "model_price_required", "configure a price for the selected model"
				}
				if errors.Is(err, ErrPlanAccess) {
					status, code, message = http.StatusForbidden, "plan_forbidden", "ChatGPT plan is unavailable for this desktop"
				}
				writeError(w, logger, status, code, message)
				return
			}
			if settle == nil {
				writeError(w, logger, 503, "budget_unavailable", "accounting unavailable")
				return
			}
		} else if opts != nil && opts.Guardrails != nil && r.Method == http.MethodPost && outReq.Body != nil {
			body, bodyErr := io.ReadAll(io.LimitReader(outReq.Body, (8<<20)+1))
			if bodyErr != nil || len(body) > 8<<20 {
				writeError(w, logger, 400, "validation_error", "invalid model request")
				return
			}
			var model struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(body, &model)
			requestModel = model.Model
			dec, gErr := opts.Guardrails.EvaluateInput(requestCtx, provider, requestModel, upstreamPath, body)
			if gErr != nil {
				emitAudit(onAudit, botID, provider, r.Method, upstreamPath, "guardrail_unavailable")
				writeError(w, logger, http.StatusForbidden, "guardrail_unavailable", "LLM guardrail evaluation failed closed")
				return
			}
			if dec.Action == "blocked" {
				code := dec.Code
				if code == "" {
					code = "guardrail_blocked"
				}
				msg := dec.Message
				if msg == "" {
					msg = "LLM request blocked by workspace guardrail"
				}
				emitAudit(onAudit, botID, provider, r.Method, upstreamPath, "guardrail_blocked")
				writeError(w, logger, http.StatusForbidden, code, msg)
				return
			}
			if dec.Action == "redacted" && len(dec.Body) > 0 {
				body = dec.Body
			}
			outReq.Body = io.NopCloser(bytes.NewReader(body))
			outReq.ContentLength = int64(len(body))
		}
		usage := Usage{}
		if settle != nil {
			defer func() {
				if err := settle(usage); err != nil {
					logger.Warn("request accounting incomplete", slog.String("bot_id", botID))
				}
			}()
		}
		if cfg.AuthKind == AuthKindClaudeOAuth && !strings.Contains(outReq.Header.Get("anthropic-beta"), "oauth-2025-04-20") {
			beta := outReq.Header.Get("anthropic-beta")
			if beta != "" {
				beta += ","
			}
			outReq.Header.Set("anthropic-beta", beta+"oauth-2025-04-20")
		}
		upstreamClient := client
		if plan {
			withoutRedirects := *client
			withoutRedirects.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			upstreamClient = &withoutRedirects
		}
		resp, err := upstreamClient.Do(outReq) //nolint:gosec // target URL originates from verified server-side SecretConfig, not user input
		if err != nil {
			// Security invariant: never include err.Error() or headers in logs/response in case a driver embeds request metadata.
			logger.Warn("upstream LLM call failed",
				slog.String("bot_id", botID),
				slog.String("provider", provider),
				slog.String("path", upstreamPath),
			)
			emitAudit(onAudit, botID, provider, r.Method, upstreamPath, "upstream_error")
			writeError(w, logger, http.StatusBadGateway, "upstream_error", "upstream LLM request failed")
			return
		}
		defer func() {
			_ = resp.Body.Close()
		}()

		if plan && (resp.StatusCode != http.StatusOK || !isEventStream(resp.Header.Get("Content-Type"))) {
			usage = Usage{Known: resp.StatusCode >= 300 && resp.StatusCode < 500}
			writePlanError(w, logger, resp, cfg.SecretValue)
			return
		}
		if settle != nil && resp.StatusCode >= 400 {
			// Provider error bodies may echo credentials or request headers.
			// Return a fixed envelope instead of forwarding those details.
			usage = Usage{Known: resp.StatusCode < 500}
			writeError(w, logger, resp.StatusCode, "upstream_error", "model provider rejected request")
			return
		}
		emitAudit(onAudit, botID, provider, r.Method, upstreamPath, "token_added")

		if opts != nil && opts.Guardrails != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && !isEventStream(resp.Header.Get("Content-Type")) {
			respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
			if readErr != nil || len(respBody) > 8<<20 {
				writeError(w, logger, http.StatusBadGateway, "upstream_error", "upstream response exceeds maximum size")
				return
			}
			collector := newUsageCollector(resp.Header.Get("Content-Type"))
			if settle != nil {
				_, _ = collector.Write(respBody)
			}
			dec, gErr := opts.Guardrails.EvaluateOutputJSON(requestCtx, provider, requestModel, upstreamPath, respBody)
			if gErr != nil {
				if settle != nil {
					usage = collector.finish(true)
				}
				emitAudit(onAudit, botID, provider, r.Method, upstreamPath, "guardrail_unavailable")
				writeError(w, logger, http.StatusForbidden, "guardrail_unavailable", "LLM guardrail evaluation failed closed")
				return
			}
			if dec.Action == "blocked" {
				if settle != nil {
					usage = collector.finish(true)
				}
				code := dec.Code
				if code == "" {
					code = "guardrail_blocked"
				}
				msg := dec.Message
				if msg == "" {
					msg = "LLM response blocked by workspace guardrail"
				}
				emitAudit(onAudit, botID, provider, r.Method, upstreamPath, "guardrail_blocked")
				writeError(w, logger, http.StatusForbidden, code, msg)
				return
			}
			if dec.Action == "redacted" && len(dec.Body) > 0 {
				respBody = dec.Body
			}
			if settle != nil {
				w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
			} else {
				copyHeaders(w.Header(), resp.Header)
				w.Header().Del("Content-Length")
			}
			w.WriteHeader(resp.StatusCode)
			_, copyErr := w.Write(respBody)
			if settle != nil {
				usage = collector.finish(copyErr == nil)
				if copyErr == nil && upstreamPath == "/v1/messages/count_tokens" && resp.StatusCode == 200 {
					usage = Usage{Known: true}
				}
			}
			return
		}

		if settle != nil {
			if plan {
				w.Header().Set("Content-Type", "text/event-stream")
			} else {
				w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
			}
		} else {
			copyHeaders(w.Header(), resp.Header)
		}
		w.WriteHeader(resp.StatusCode)
		var destination io.Writer = w
		if isEventStream(resp.Header.Get("Content-Type")) {
			// net/http otherwise buffers small chunks until completion, hiding
			// live CLI activity and delaying cancellation feedback in the UI.
			destination = flushingWriter{w}
		}
		var guardCloser io.WriteCloser
		if opts != nil && opts.Guardrails != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && isEventStream(resp.Header.Get("Content-Type")) {
			guardCloser = opts.Guardrails.WrapSSEOutput(requestCtx, provider, requestModel, upstreamPath, destination)
			if guardCloser != nil {
				destination = guardCloser
			}
		}
		var source io.Reader = resp.Body
		collector := newUsageCollector(resp.Header.Get("Content-Type"))
		if settle != nil {
			source = io.TeeReader(resp.Body, collector)
		}
		var copyErr error
		if plan {
			copyErr = copyPlanStream(destination, source, cfg.SecretValue)
		} else {
			_, copyErr = io.Copy(destination, source)
		}
		if guardCloser != nil {
			if closeErr := guardCloser.Close(); closeErr != nil && copyErr == nil {
				copyErr = closeErr
			}
		}
		if settle != nil {
			usage = collector.finish(copyErr == nil && resp.StatusCode >= 200 && resp.StatusCode < 300)
			if copyErr == nil && (resp.StatusCode >= 400 && resp.StatusCode < 500 || upstreamPath == "/v1/messages/count_tokens" && resp.StatusCode == 200) {
				usage = Usage{Known: true}
			}
		}
		if copyErr != nil {
			logger.Debug("stream upstream response interrupted", slog.String("provider", provider))
		}
	})
}

func parseLLMPath(path string) (provider, upstreamPath string, ok bool) {
	trimmed := strings.TrimPrefix(path, "/llm/")
	if trimmed == path || trimmed == "" {
		return "", "", false
	}
	parts := strings.SplitN(trimmed, "/", 2)
	if parts[0] == "" {
		return "", "", false
	}
	subpath := "/"
	if len(parts) == 2 && parts[1] != "" {
		subpath = "/" + parts[1]
	}
	// Pi's Messages client includes /v1 even when its shared OpenRouter base
	// URL already includes it for Chat Completions.
	if parts[0] == ProviderOpenRouter && subpath == "/v1/v1/messages" {
		subpath = "/v1/messages"
	}
	return parts[0], subpath, true
}

func isSupportedProvider(provider string) bool {
	switch provider {
	case ProviderAnthropic, ProviderOpenAI, ProviderOpenAIPlan, ProviderGoogle, ProviderOpenRouter:
		return true
	default:
		return false
	}
}

func stripSandboxAuthHeaders(h http.Header) {
	h.Del("Authorization")
	h.Del("Proxy-Authorization")
	h.Del("X-Api-Key")
	h.Del("X-Goog-Api-Key")
}

func injectCredential(ctx context.Context, h http.Header, cfg *SecretConfig, exchange OAuthExchanger) error {
	switch cfg.AuthKind {
	case AuthKindAnthropicKey:
		h.Set("x-api-key", cfg.SecretValue)
		return nil
	case AuthKindBearer, AuthKindClaudeOAuth, AuthKindOpenAIPlan:
		h.Set("Authorization", "Bearer "+cfg.SecretValue)
		return nil
	case AuthKindOAuthRefresh:
		if exchange == nil {
			return errors.New("oauth exchanger not configured")
		}
		accessToken, err := exchange(ctx, cfg.SecretValue)
		if err != nil {
			return fmt.Errorf("exchange token: %w", err)
		}
		if accessToken == "" {
			return errors.New("empty access token")
		}
		h.Set("Authorization", "Bearer "+accessToken)
		return nil
	default:
		return fmt.Errorf("unsupported auth kind: %s", cfg.AuthKind)
	}
}

func copyHeaders(dst, src http.Header) {
	for k, vals := range src {
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func emitAudit(cb func(AuditEvent), botID, provider, method, path, result string) {
	if cb == nil {
		return
	}
	cb(AuditEvent{
		BotID:    botID,
		Provider: provider,
		Method:   method,
		Path:     path,
		Result:   result,
	})
}

func writeError(w http.ResponseWriter, logger *slog.Logger, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	var env errorEnvelope
	env.Error.Code = code
	env.Error.Message = message
	if err := json.NewEncoder(w).Encode(env); err != nil {
		logger.Error("encode llmproxy error failed", slog.Any("error", err))
	}
}

// ErrBudgetExceeded prevents a provider call after an applicable limit is reached.
var ErrBudgetExceeded = errors.New("budget exhausted")

// NewScopedHandler binds identity to trusted proxy configuration instead of
// sandbox headers. A separate handler must be provisioned for each bot.
// CheckBudget failures deny requests before credential resolution or exchange.
func NewScopedHandler(workspaceID, botID string, resolve SecretResolver, opts *Options) (http.Handler, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(botID) == "" || resolve == nil {
		return nil, errors.New("workspace, bot and credential resolver are required")
	}
	config := Options{}
	if opts != nil {
		config = *opts
	}
	config.workspaceID, config.botID = workspaceID, botID
	return NewHandler(resolve, &config), nil
}

// flushingWriter preserves live SSE boundaries on real HTTP listeners.
type flushingWriter struct{ http.ResponseWriter }

func (w flushingWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	if err != nil {
		return n, err
	}
	err = http.NewResponseController(w.ResponseWriter).Flush()
	if errors.Is(err, http.ErrNotSupported) {
		err = nil
	}
	return n, err
}

func meteredPath(provider, path string) bool {
	switch provider {
	case ProviderOpenAIPlan:
		return path == "/v1/responses"
	case ProviderOpenAI:
		return path == "/v1/responses" || path == "/v1/chat/completions"
	case ProviderOpenRouter:
		return path == "/v1/chat/completions" || path == "/v1/messages"
	case ProviderAnthropic:
		return path == "/v1/messages" || path == "/v1/messages/count_tokens"
	default:
		return false
	}
}

type subscriptionContextKey struct{}

// SubscriptionRequest reports billing selected by the trusted credential resolver.
// The marker contains no token and cannot be set by sandbox request headers.
func SubscriptionRequest(ctx context.Context) bool {
	value, _ := ctx.Value(subscriptionContextKey{}).(bool)
	return value
}
