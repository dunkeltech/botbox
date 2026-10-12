package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNativeProviderTunnelsCannotBypassMetering(t *testing.T) {
	for _, host := range []string{"api.openai.com", "API.ANTHROPIC.COM.", "openrouter.ai", "cloudcode-pa.googleapis.com", "generativelanguage.googleapis.com"} {
		if !isLLMHost(host) {
			t.Fatalf("provider tunnel admitted: %s", host)
		}
	}
	for _, host := range []string{"github.com", "api.openai.com.example.org", "example.com"} {
		if isLLMHost(host) {
			t.Fatalf("unrelated host matched: %s", host)
		}
	}
}

func TestAuthModeDoesNotLeakCredentialsOrOpenProviderTunnels(t *testing.T) {
	//nolint:gosec // synthetic credential used to verify redaction
	const secret = "sk-ant-api03-SUPERSECRETVALUE"
	connected := false
	metered := false
	handler := newEgressHandler(egressDeps{
		Connect: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { connected = true }),
		LLM:     http.HandlerFunc(func(http.ResponseWriter, *http.Request) { metered = true }),
		AuthMode: func(context.Context) (string, error) {
			return "oauth", nil
		},
	})
	for _, host := range []string{"api.anthropic.com:443", "console.anthropic.com:443", "api.anthropic.com"} {
		connected = false
		req := httptest.NewRequest(http.MethodConnect, "http://egress:8081/", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || connected {
			t.Fatalf("host %s status=%d connected=%v body=%s", host, rec.Code, connected, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatal("connect denial included credential material")
		}
	}

	req := httptest.NewRequest(http.MethodGet, "http://egress:8081/llm/anthropic/auth-mode", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "oauth" || metered {
		t.Fatalf("auth mode status=%d metered=%v body=%q", rec.Code, metered, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secret) || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("auth mode response leaked or is cacheable: %q", rec.Body.String())
	}

	handler = newEgressHandler(egressDeps{AuthMode: func(context.Context) (string, error) {
		return "", errors.New("dial tcp password=" + secret)
	}})
	req = httptest.NewRequest(http.MethodGet, "http://egress:8081/llm/anthropic/auth-mode", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), secret) || !strings.Contains(rec.Body.String(), "credential service unavailable") {
		t.Fatalf("dependency failure status=%d body=%q", rec.Code, rec.Body.String())
	}

	for _, mode := range []string{"none", "api_key", "oauth\n" + secret, ""} {
		handler = newEgressHandler(egressDeps{AuthMode: func(context.Context) (string, error) { return mode, nil }})
		req = httptest.NewRequest(http.MethodGet, "http://egress:8081/llm/anthropic/auth-mode", nil)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		body := rec.Body.String()
		if strings.Contains(body, secret) {
			t.Fatalf("mode %q leaked credential: %q", mode, body)
		}
		switch mode {
		case "none", "api_key":
			if rec.Code != http.StatusOK || strings.TrimSpace(body) != mode {
				t.Fatalf("mode %q status=%d body=%q", mode, rec.Code, body)
			}
		default:
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("invalid mode %q status=%d body=%q", mode, rec.Code, body)
			}
		}
	}
}
