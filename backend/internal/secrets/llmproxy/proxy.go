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
	"net/url"
	"strings"
)

// ErrSecretNotFound indicates no credential is configured for the bot and provider.
var ErrSecretNotFound = errors.New("provider secret not configured")

// Supported upstream provider route prefixes and credential injection strategies.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
	ProviderGoogle    = "google"

	AuthKindAnthropicKey = "anthropic_key"
	AuthKindBearer       = "bearer"
	AuthKindOAuthRefresh = "oauth_refresh"
)

// SecretConfig holds the upstream base URL and credential material stored outside the sandbox.
type SecretConfig struct {
	UpstreamURL string
	AuthKind    string
	SecretValue string
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

// Options configures optional dependencies for NewHandler.
type Options struct {
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

		if resolve == nil {
			writeError(w, logger, http.StatusForbidden, "forbidden", "credential resolver unavailable")
			return
		}

		botID := r.Header.Get(botHeader)
		if botID == "" {
			botID = "default"
		}

		cfg, err := resolve(r.Context(), botID, provider)
		if err != nil || cfg == nil || cfg.SecretValue == "" {
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

		// Security invariant: copy headers and strip all sandbox placeholder auth headers
		// before adding real credentials so placeholders never reach upstream providers.
		outReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL.String(), r.Body)
		if err != nil {
			emitAudit(onAudit, botID, provider, r.Method, upstreamPath, "upstream_error")
			writeError(w, logger, http.StatusBadGateway, "upstream_error", "failed to build upstream request")
			return
		}
		copyHeaders(outReq.Header, r.Header)
		stripSandboxAuthHeaders(outReq.Header)
		outReq.Header.Del(botHeader)

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

		resp, err := client.Do(outReq) //nolint:gosec // target URL originates from verified server-side SecretConfig, not user input
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

		emitAudit(onAudit, botID, provider, r.Method, upstreamPath, "token_added")

		copyHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		if _, err := io.Copy(w, resp.Body); err != nil {
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
	return parts[0], subpath, true
}

func isSupportedProvider(provider string) bool {
	switch provider {
	case ProviderAnthropic, ProviderOpenAI, ProviderGoogle:
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
	case AuthKindBearer:
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
