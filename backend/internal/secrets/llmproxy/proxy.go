// SPDX-License-Identifier: AGPL-3.0-only

// Package llmproxy implements Spike S2: injecting real LLM credentials outside
// the sandbox so CLIs (claude, codex, agy) only ever hold placeholder tokens.
package llmproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ErrSecretNotFound indicates no credential is configured for the bot and provider.
var ErrSecretNotFound = errors.New("provider secret not configured")

// ErrStreamBlocked is returned by a GuardrailEnforcer stream writer when it
// stops a response.
var ErrStreamBlocked = errors.New("llm stream blocked by workspace guardrail")

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
	case AuthKindAnthropicKey, AuthKindBearer, AuthKindGoogleAPIKey:
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
	AuthKindGoogleAPIKey = "google_api_key"
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
	// Now replaces the clock in tests.
	Now func() time.Time
}

type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type proxyHandler struct {
	resolve SecretResolver
	options Options

	mu sync.Mutex
	// refuseGeminiUntil is set after a guardrail stopped a Gemini stream.
	refuseGeminiUntil time.Time
}

type proxyRoute struct {
	botID    string
	provider string
	method   string
	path     string
}

// NewHandler builds an HTTP handler routing /llm/{provider}/* to the real upstream
// while stripping placeholder auth headers and injecting real secrets server-side.
func NewHandler(resolve SecretResolver, opts *Options) http.Handler {
	var options Options
	if opts != nil {
		options = *opts
	}
	if options.BotIDHeader == "" {
		options.BotIDHeader = "X-Botbox-Bot-ID"
	}
	if options.Client == nil {
		options.Client = http.DefaultClient
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &proxyHandler{resolve: resolve, options: options}
}

func (h *proxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	opts := &h.options
	logger := opts.Logger
	provider, upstreamPath, ok := parseLLMPath(r.URL.Path)
	if !ok || !isSupportedProvider(provider) {
		emitAudit(opts.OnAudit, r.Header.Get(opts.BotIDHeader), provider, r.Method, r.URL.Path, "blocked")
		writeError(w, logger, http.StatusForbidden, "forbidden", "unsupported LLM provider")
		return
	}

	if provider == ProviderGoogle && h.takeGeminiRefusal() {
		emitAudit(opts.OnAudit, r.Header.Get(opts.BotIDHeader), provider, r.Method, r.URL.Path, "guardrail_blocked")
		writeError(w, logger, http.StatusForbidden, "guardrail_blocked", "LLM response blocked by workspace guardrail")
		return
	}

	planRoute := provider == ProviderOpenAIPlan
	if planRoute && (opts.workspaceID == "" || opts.Admit == nil ||
		r.Method != http.MethodPost || !meteredPath(provider, upstreamPath) ||
		r.URL.RawQuery != "" || r.Header.Get("Upgrade") != "" || r.Header.Get("Authorization") != "Bearer botbox-placeholder") {
		writeError(w, logger, http.StatusForbidden, "forbidden", "unsupported ChatGPT plan request")
		return
	}

	if h.resolve == nil {
		writeError(w, logger, http.StatusForbidden, "forbidden", "credential resolver unavailable")
		return
	}

	botID := opts.botID
	if botID == "" {
		botID = r.Header.Get(opts.BotIDHeader)
	}
	if botID == "" {
		botID = "default"
	}
	route := proxyRoute{botID: botID, provider: provider, method: r.Method, path: upstreamPath}
	if opts.CheckBudget != nil {
		if err := opts.CheckBudget(r.Context(), opts.workspaceID, botID); err != nil {
			statusCode, code, message := http.StatusServiceUnavailable, "budget_unavailable", "budget service unavailable"
			if errors.Is(err, ErrBudgetExceeded) {
				statusCode, code, message = http.StatusTooManyRequests, "budget_exceeded", "budget exhausted"
			}
			h.audit(route, code)
			writeError(w, logger, statusCode, code, message)
			return
		}
	}

	cfg, targetURL := h.resolveUpstream(w, r, route)
	if cfg == nil {
		return
	}
	planProtocol := provider == ProviderOpenAIPlan || cfg.AuthKind == AuthKindOpenAIPlan
	requestCtx := context.WithValue(r.Context(), subscriptionContextKey{}, cfg.AuthKind == AuthKindClaudeOAuth || planProtocol)
	if cfg.PlanRevision != "" {
		requestCtx = context.WithValue(requestCtx, planRevisionContextKey{}, cfg.PlanRevision)
	}
	outReq := h.buildUpstreamRequest(requestCtx, w, r, route, cfg, targetURL)
	if outReq == nil {
		return
	}
	model, body, ok := h.prepareModelBody(requestCtx, w, outReq, route, planProtocol)
	if !ok {
		return
	}

	var usage Usage
	metered := opts.Admit != nil
	if metered {
		requestCtx = boundedRequest(requestCtx, provider, upstreamPath, body)
		settle, ok := h.admitRequest(requestCtx, w, route, model)
		if !ok {
			return
		}
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
	upstreamClient := opts.Client
	if planProtocol {
		withoutRedirects := *opts.Client
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
		h.audit(route, "upstream_error")
		writeError(w, logger, http.StatusBadGateway, "upstream_error", "upstream LLM request failed")
		return
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	usage = h.forwardResponse(requestCtx, w, resp, route, model, cfg, metered)
}

func (h *proxyHandler) audit(route proxyRoute, result string) {
	emitAudit(h.options.OnAudit, route.botID, route.provider, route.method, route.path, result)
}

func (h *proxyHandler) allowGuardrailDecision(w http.ResponseWriter, route proxyRoute, action, code, message, defaultMessage string, err error) bool {
	if err != nil {
		h.audit(route, "guardrail_unavailable")
		writeError(w, h.options.Logger, http.StatusForbidden, "guardrail_unavailable", "LLM guardrail evaluation failed closed")
		return false
	}
	if action != "blocked" {
		return true
	}
	if code == "" {
		code = "guardrail_blocked"
	}
	if message == "" {
		message = defaultMessage
	}
	h.audit(route, "guardrail_blocked")
	writeError(w, h.options.Logger, http.StatusForbidden, code, message)
	return false
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
	case AuthKindGoogleAPIKey:
		h.Set("x-goog-api-key", cfg.SecretValue)
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
	case ProviderGoogle:
		return geminiPath.MatchString(path)
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
