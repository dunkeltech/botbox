// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/observability"
)

func TestRequestMetricsBoundUnknownMethods(t *testing.T) {
	t.Parallel()
	metrics := observability.NewMetrics()
	h := NewHandlerWithConfig(Config{Metrics: metrics})
	for _, method := range []string{"CUSTOM-ONE", "CUSTOM-TWO", http.MethodGet} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, "/api/healthz", nil))
	}
	families, err := metrics.Registry.Gather()
	if err != nil || len(families) != 1 {
		t.Fatalf("gathered %d metric families: %v", len(families), err)
	}
	observations := map[string]uint64{}
	for _, metric := range families[0].GetMetric() {
		for _, label := range metric.GetLabel() {
			if label.GetName() == "method" {
				observations[label.GetValue()] += metric.GetHistogram().GetSampleCount()
			}
		}
	}
	if len(observations) != 2 || observations["_OTHER"] != 2 || observations[http.MethodGet] != 1 {
		t.Fatalf("samples by method: %v", observations)
	}
}

func TestSecurityHeadersAndTrustedProxyClientIP(t *testing.T) {
	t.Parallel()

	t.Run("HTTP PublicBaseURL sets CSP and security headers without HSTS", func(t *testing.T) {
		h := NewHandlerWithConfig(Config{PublicBaseURL: "http://127.0.0.1:8000"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/healthz", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d", rec.Code)
		}
		for header, want := range map[string]string{
			"X-Content-Type-Options":  "nosniff",
			"X-Frame-Options":         "DENY",
			"Referrer-Policy":         "no-referrer",
			"Permissions-Policy":      "camera=(), microphone=(), geolocation=()",
			"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
			"Cache-Control":           "no-store",
		} {
			if got := rec.Header().Get(header); got != want {
				t.Fatalf("%s = %q, want %q", header, got, want)
			}
		}
		if hsts := rec.Header().Get("Strict-Transport-Security"); hsts != "" {
			t.Fatalf("unexpected HSTS on HTTP PublicBaseURL: %q", hsts)
		}
	})

	t.Run("HTTPS PublicBaseURL enables HSTS", func(t *testing.T) {
		h := NewHandlerWithConfig(Config{PublicBaseURL: "https://botbox.example"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/healthz", nil))
		if got := rec.Header().Get("Strict-Transport-Security"); got != "max-age=31536000; includeSubDomains" {
			t.Fatalf("Strict-Transport-Security = %q", got)
		}
	})

	t.Run("ParseTrustedProxyCIDRs validates inputs", func(t *testing.T) {
		prefixes, err := ParseTrustedProxyCIDRs("127.0.0.1, 10.0.0.0/8, ::1")
		if err != nil || len(prefixes) != 3 {
			t.Fatalf("ParseTrustedProxyCIDRs failed: prefixes=%v err=%v", prefixes, err)
		}
		if _, err := ParseTrustedProxyCIDRs("not-an-ip"); err == nil {
			t.Fatal("expected invalid CIDR error")
		}
	})

	t.Run("clientIP ignores spoofed X-Forwarded-For from untrusted peers and extracts real IP behind trusted proxies", func(t *testing.T) {
		trusted, err := ParseTrustedProxyCIDRs("172.18.0.0/16, 127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		srv := newServer(Config{TrustedProxyCIDRs: trusted}, nil)
		untrustedSrv := newServer(Config{}, nil)

		// Untrusted direct peer spoofing X-Forwarded-For must not be trusted.
		spoofReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		spoofReq.RemoteAddr = "198.51.100.25:43210"
		spoofReq.Header.Set("X-Forwarded-For", "203.0.113.9")
		if got := srv.clientIP(spoofReq); got != "198.51.100.25" {
			t.Fatalf("spoofed peer clientIP = %q, want 198.51.100.25", got)
		}

		// When no trusted proxies are configured, even local Caddy peer ignores XFF.
		localReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		localReq.RemoteAddr = "127.0.0.1:43210"
		localReq.Header.Set("X-Forwarded-For", "203.0.113.9")
		if got := untrustedSrv.clientIP(localReq); got != "127.0.0.1" {
			t.Fatalf("default clientIP = %q, want 127.0.0.1", got)
		}

		// Trusted proxy chain: rightmost untrusted hop wins even if client prepended a fake IP.
		proxiedReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		proxiedReq.RemoteAddr = "172.18.0.5:8080"
		proxiedReq.Header.Set("X-Forwarded-For", "1.2.3.4, 203.0.113.77, 172.18.0.2")
		if got := srv.clientIP(proxiedReq); got != "203.0.113.77" {
			t.Fatalf("proxied clientIP = %q, want 203.0.113.77", got)
		}
	})
}
