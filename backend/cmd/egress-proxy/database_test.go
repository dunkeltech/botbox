// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/codexplan"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
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

func TestACPConnectIsConfinedToEnabledWorkerAndExactOrigins(t *testing.T) {
	ca, err := mitmproxy.NewBotCA("fixture")
	if err != nil {
		t.Fatal(err)
	}
	connect, err := mitmproxy.NewHandler(mitmproxy.Config{CA: ca})
	if err != nil {
		t.Fatal(err)
	}
	var enabled, broken bool
	native := newNativeGoogleConnect(connect, func(context.Context) (bool, error) {
		if broken {
			return false, errors.New("private database cause")
		}
		return enabled, nil
	}, func(http.ResponseWriter, *http.Request, string, http.RoundTripper) {
		t.Fatal("disallowed native route reached upstream")
	})
	h := newEgressHandler(egressDeps{
		NativeOnly: true, NativeGoogle: native, Connect: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("ACP worker fell back to generic egress")
		}),
	})
	for _, tc := range []struct {
		method, target  string
		enabled, broken bool
		want            int
	}{
		{"CONNECT", "daily-cloudcode-pa.googleapis.com:443", false, false, 403},
		{"CONNECT", "daily-cloudcode-pa.googleapis.com:443", true, true, 403},
		{"CONNECT", "daily-cloudcode-pa.googleapis.com:8443", true, false, 403},
		{"CONNECT", "daily-cloudcode-pa.googleapis.com.fixture.test:443", true, false, 403},
		{"CONNECT", "generativelanguage.googleapis.com:443", true, false, 403},
		{"CONNECT", "example.org:443", true, false, 403},
		{"POST", "/llm/google/v1beta/models/a:generateContent", true, false, 404},
		{"POST", "/broker/browser", true, false, 404},
	} {
		enabled, broken = tc.enabled, tc.broken
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.target, nil))
		if rec.Code != tc.want || strings.Contains(rec.Body.String(), "private database cause") {
			t.Fatalf("%s %s = %d, want %d", tc.method, tc.target, rec.Code, tc.want)
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

func TestUnifiedOpenAIReadyAndAuthModeEndpoints(t *testing.T) {
	//nolint:gosec // synthetic credential used to verify redaction
	const secret = "oa-secret-leak-check"
	handler := newEgressHandler(egressDeps{
		OpenAIAuthMode: func(context.Context) (string, error) { return "oauth", nil },
		OpenAIReady:    func(context.Context) error { return nil },
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://egress:8081/llm/openai/auth-mode", nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "oauth" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("openai auth-mode status=%d body=%q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://egress:8081/llm/openai/ready", nil))
	if rec.Code != http.StatusNoContent || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("openai ready status=%d body=%q", rec.Code, rec.Body.String())
	}

	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"forbidden", codexplan.ErrForbidden, http.StatusForbidden},
		{"reconnect", codexplan.ErrReconnect, http.StatusForbidden},
		{"unavailable", errors.New("db down " + secret), http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newEgressHandler(egressDeps{OpenAIReady: func(context.Context) error { return tc.err }})
			r := httptest.NewRecorder()
			h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "http://egress:8081/llm/openai/ready", nil))
			if r.Code != tc.want || strings.Contains(r.Body.String(), secret) {
				t.Fatalf("status=%d want=%d body=%q", r.Code, tc.want, r.Body.String())
			}
		})
	}
}
