package llmproxy_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
)

func TestProxySecretInjectionAndScrubbing(t *testing.T) {
	t.Parallel()

	const (
		realAnthropicKey = "sk-ant-real-secret-999"       //nolint:gosec // synthetic test secret
		realOpenAIKey    = "sk-openai-real-secret-888"    //nolint:gosec // synthetic test secret
		realRefreshToken = "1//0g-real-refresh-token-777" //nolint:gosec // synthetic test secret
		realAccessToken  = "ya29.real-access-token-666"   //nolint:gosec // synthetic test secret
	)

	var gotPath string
	var gotAuth string
	var gotAnthropicKey string
	var gotGoogKey string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAnthropicKey = r.Header.Get("x-api-key")
		gotGoogKey = r.Header.Get("x-goog-api-key")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_1"}`))
	}))
	defer upstream.Close()

	resolve := func(_ context.Context, botID, provider string) (*llmproxy.SecretConfig, error) {
		if botID != "bot-atlas" {
			return nil, llmproxy.ErrSecretNotFound
		}
		switch provider {
		case llmproxy.ProviderAnthropic:
			return &llmproxy.SecretConfig{
				UpstreamURL: upstream.URL,
				AuthKind:    llmproxy.AuthKindAnthropicKey,
				SecretValue: realAnthropicKey,
			}, nil
		case llmproxy.ProviderOpenAI:
			return &llmproxy.SecretConfig{
				UpstreamURL: upstream.URL,
				AuthKind:    llmproxy.AuthKindBearer,
				SecretValue: realOpenAIKey,
			}, nil
		case llmproxy.ProviderGoogle:
			return &llmproxy.SecretConfig{
				UpstreamURL: upstream.URL,
				AuthKind:    llmproxy.AuthKindOAuthRefresh,
				SecretValue: realRefreshToken,
			}, nil
		default:
			return nil, llmproxy.ErrSecretNotFound
		}
	}

	var audits []llmproxy.AuditEvent
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))

	handler := llmproxy.NewHandler(resolve, &llmproxy.Options{
		OAuthExchanger: func(_ context.Context, refreshToken string) (string, error) {
			if refreshToken != realRefreshToken {
				return "", errors.New("unexpected refresh token")
			}
			return realAccessToken, nil
		},
		OnAudit: func(ev llmproxy.AuditEvent) {
			audits = append(audits, ev)
		},
		Logger: logger,
	})

	proxySrv := httptest.NewServer(handler)
	defer proxySrv.Close()

	t.Run("claude strips placeholder x-api-key and injects real Anthropic key", func(t *testing.T) {
		gotPath, gotAuth, gotAnthropicKey, gotGoogKey = "", "", "", ""
		req, _ := http.NewRequest(http.MethodPost, proxySrv.URL+"/llm/anthropic/v1/messages", strings.NewReader(`{}`))
		req.Header.Set("X-Botbox-Bot-ID", "bot-atlas")
		req.Header.Set("x-api-key", "sk-ant-placeholder")
		req.Header.Set("Authorization", "Bearer fake-token")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if gotPath != "/v1/messages" || gotAnthropicKey != realAnthropicKey || gotAuth != "" {
			t.Fatalf("upstream saw path=%q x-api-key=%q auth=%q", gotPath, gotAnthropicKey, gotAuth)
		}
	})

	t.Run("codex strips placeholder Bearer and injects real OpenAI key", func(t *testing.T) {
		gotPath, gotAuth, gotAnthropicKey, gotGoogKey = "", "", "", ""
		req, _ := http.NewRequest(http.MethodPost, proxySrv.URL+"/llm/openai/v1/responses", strings.NewReader(`{}`))
		req.Header.Set("X-Botbox-Bot-ID", "bot-atlas")
		req.Header.Set("Authorization", "Bearer sk-placeholder")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if gotPath != "/v1/responses" || gotAuth != "Bearer "+realOpenAIKey {
			t.Fatalf("upstream saw path=%q auth=%q", gotPath, gotAuth)
		}
	})

	t.Run("agy exchanges proxy-held refresh token for access token without exposing refresh token", func(t *testing.T) {
		gotPath, gotAuth, gotAnthropicKey, gotGoogKey = "", "", "", ""
		req, _ := http.NewRequest(http.MethodPost, proxySrv.URL+"/llm/google/v1beta/models", strings.NewReader(`{}`))
		req.Header.Set("X-Botbox-Bot-ID", "bot-atlas")
		req.Header.Set("Authorization", "Bearer agy-placeholder")
		req.Header.Set("x-goog-api-key", "AIzaPlaceholder")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if gotPath != "/v1beta/models" || gotAuth != "Bearer "+realAccessToken || gotGoogKey != "" {
			t.Fatalf("upstream saw path=%q auth=%q googKey=%q", gotPath, gotAuth, gotGoogKey)
		}
	})

	t.Run("unknown provider and missing bot credential return 403", func(t *testing.T) {
		for _, tc := range []struct {
			path  string
			botID string
		}{
			{path: "/llm/unknown/v1/chat", botID: "bot-atlas"},
			{path: "/llm/anthropic/v1/messages", botID: "bot-other"},
		} {
			req, _ := http.NewRequest(http.MethodPost, proxySrv.URL+tc.path, strings.NewReader(`{}`))
			req.Header.Set("X-Botbox-Bot-ID", tc.botID)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("path=%q bot=%q status=%d, want 403", tc.path, tc.botID, resp.StatusCode)
			}
		}
	})

	t.Run("upstream and exchanger failures return 502 with zero secret leaks", func(t *testing.T) {
		failHandler := llmproxy.NewHandler(func(_ context.Context, _, _ string) (*llmproxy.SecretConfig, error) {
			return &llmproxy.SecretConfig{
				UpstreamURL: "http://127.0.0.1:1",
				AuthKind:    llmproxy.AuthKindOAuthRefresh,
				SecretValue: realRefreshToken,
			}, nil
		}, &llmproxy.Options{
			OAuthExchanger: func(_ context.Context, _ string) (string, error) {
				return "", errors.New("oauth error containing " + realRefreshToken)
			},
			Logger: logger,
		})

		req := httptest.NewRequest(http.MethodPost, "/llm/google/v1beta/models", nil)
		rec := httptest.NewRecorder()
		failHandler.ServeHTTP(rec, req)

		res := rec.Result()
		defer func() { _ = res.Body.Close() }()
		bodyBytes, _ := io.ReadAll(res.Body)

		if res.StatusCode != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", res.StatusCode)
		}
		for _, secret := range []string{realAnthropicKey, realOpenAIKey, realRefreshToken, realAccessToken} {
			if strings.Contains(string(bodyBytes), secret) {
				t.Fatalf("response body leaked secret %q", secret)
			}
			if strings.Contains(logBuf.String(), secret) {
				t.Fatalf("logs leaked secret %q", secret)
			}
		}
	})

	if len(audits) == 0 || audits[0].Result != "token_added" {
		t.Fatalf("unexpected audit trail: %+v", audits)
	}
}

func TestMeteredProxyDoesNotForwardCompressedProviderBody(t *testing.T) {
	const payload = `{"id":"msg_fixture","type":"message"}`
	const realKey = "sk-ant-fixture-compression"
	var gotEncoding string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEncoding = r.Header.Get("Accept-Encoding")
		if r.Header.Get("x-api-key") != realKey {
			http.Error(w, "missing key", http.StatusUnauthorized)
			return
		}
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write([]byte(payload)); err != nil {
			t.Errorf("gzip: %v", err)
			return
		}
		if err := zw.Close(); err != nil {
			t.Errorf("gzip close: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf.Bytes())
	}))
	defer upstream.Close()

	handler, err := llmproxy.NewScopedHandler("ws", "bot", func(context.Context, string, string) (*llmproxy.SecretConfig, error) {
		return &llmproxy.SecretConfig{UpstreamURL: upstream.URL, AuthKind: llmproxy.AuthKindAnthropicKey, SecretValue: realKey}, nil
	}, &llmproxy.Options{
		Admit: func(context.Context, string, string, string) (func(llmproxy.Usage) error, error) {
			return func(llmproxy.Usage) error { return nil }, nil
		},
		Client: &http.Client{},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/llm/anthropic/v1/messages", strings.NewReader(`{"model":"claude-sonnet-5-5"}`))
	req.Header.Set("x-api-key", "botbox-placeholder")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if strings.Contains(gotEncoding, "br") || strings.Contains(gotEncoding, "zstd") || gotEncoding == "" {
		t.Fatalf("provider saw Accept-Encoding %q", gotEncoding)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != payload {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Encoding") != "" || strings.Contains(rec.Body.String(), realKey) {
		t.Fatal("compressed body or credential was forwarded")
	}
}
