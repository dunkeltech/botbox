package mitmproxy_test

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
)

func newHostTLSServer(t *testing.T, host string, h http.Handler) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	upstreamCA, err := mitmproxy.NewBotCA("upstream-" + host)
	if err != nil {
		t.Fatalf("NewBotCA: %v", err)
	}
	cert, err := upstreamCA.LeafCertificate(host)
	if err != nil {
		t.Fatalf("LeafCertificate: %v", err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{*cert},
	}
	srv.StartTLS()
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(upstreamCA.CertPEM())
	return srv, roots
}

func TestAdaptersAndEnvValidation(t *testing.T) {
	t.Parallel()

	gh, ok := mitmproxy.LookupAdapter("github_api")
	if !ok {
		t.Fatal("expected github_api adapter")
	}
	if !gh.MatchesRequest("api.github.com", "443", http.MethodGet, "/repos/dunkel/botbox") {
		t.Fatal("expected /repos/ path to match github_api")
	}
	if gh.MatchesRequest("api.github.com", "443", http.MethodGet, "/repos/../admin/keys") {
		t.Fatal("path traversal must not match github_api")
	}
	if gh.MatchesRequest("evil.github.com", "443", http.MethodGet, "/user") {
		t.Fatal("unexpected host must not match github_api")
	}

	git, ok := mitmproxy.LookupAdapter("gitlab_git")
	if !ok {
		t.Fatal("expected gitlab_git adapter")
	}
	if !git.MatchesRequest("gitlab.com", "443", http.MethodPost, "/dunkel/dt_cron.git/git-upload-pack") {
		t.Fatal("expected git-upload-pack to match gitlab_git")
	}
	if git.MatchesRequest("gitlab.com", "443", http.MethodPost, "/api/v4/projects") {
		t.Fatal("API route must not match gitlab_git")
	}

	for _, valid := range []string{"GH_TOKEN", "NPM_TOKEN", "MY_CLI_KEY_1"} {
		if err := mitmproxy.ValidateEnvPlaceholderName(valid); err != nil {
			t.Fatalf("expected valid env name %q, got %v", valid, err)
		}
	}
	for _, invalid := range []string{"PATH", "LD_PRELOAD", "HTTPS_PROXY", "ANTHROPIC_API_KEY", "BOTBOX_SECRETS_KEK", "bad-name", "123_BAD", "lower_case"} {
		if err := mitmproxy.ValidateEnvPlaceholderName(invalid); err == nil {
			t.Fatalf("expected invalid/reserved env name %q to be rejected", invalid)
		}
	}

	if _, _, _, err := mitmproxy.CanonicalHTTPSOrigin("http://api.github.com"); err == nil {
		t.Fatal("expected non-https URL to be rejected")
	}
	if _, _, _, err := mitmproxy.CanonicalHTTPSOrigin("https://user:pass@api.github.com"); err == nil {
		t.Fatal("expected userinfo URL to be rejected")
	}
	if _, _, _, err := mitmproxy.CanonicalHTTPSOrigin("https://127.0.0.1:443"); err == nil {
		t.Fatal("expected IP target to be rejected")
	}
	orig, host, port, err := mitmproxy.CanonicalHTTPSOrigin("https://API.GitHub.com/user")
	if err != nil || orig != "https://api.github.com:443" || host != "api.github.com" || port != "443" {
		t.Fatalf("unexpected canonical origin: %q %q %q %v", orig, host, port, err)
	}
}

func TestDynamicPolicyFailClosedExpiryRevocationOnSameConnection(t *testing.T) {
	t.Parallel()

	var authLog []string
	var logMu sync.Mutex
	upstream, upstreamRoots := newHostTLSServer(t, "api.github.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logMu.Lock()
		authLog = append(authLog, r.Header.Get("Authorization"))
		logMu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	ca, err := mitmproxy.NewBotCA("bot-lease-expiry")
	if err != nil {
		t.Fatalf("NewBotCA: %v", err)
	}

	nowMu := sync.Mutex{}
	currentTime := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return currentTime
	}

	var policyMu sync.RWMutex
	var policyErr error
	snapshot := mitmproxy.PolicySnapshot{
		Policy:       mitmproxy.PolicyAllowlist,
		AllowedHosts: []string{"api.github.com"},
		Rules: []mitmproxy.SecretRule{
			{
				ID:          "lease-1",
				Scope:       "lease",
				Name:        "GH_TOKEN",
				AdapterID:   "github_api",
				DomainMatch: "api.github.com",
				Port:        "443",
				HeaderName:  "Authorization",
				Scheme:      mitmproxy.SchemeBearer,
				Value:       "ghp_supersecret_lease_1234",
				ExpiresAt:   currentTime.Add(10 * time.Minute),
			},
		},
	}

	handler, err := mitmproxy.NewHandler(mitmproxy.Config{
		CA:              ca,
		UpstreamRootCAs: upstreamRoots,
		DialContext:     dialLocalTestServer(t, upstream),
		Now:             nowFn,
		DynamicPolicy: func(_ context.Context) (mitmproxy.PolicySnapshot, error) {
			policyMu.RLock()
			defer policyMu.RUnlock()
			if policyErr != nil {
				return mitmproxy.PolicySnapshot{}, policyErr
			}
			return snapshot, nil
		},
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	var connectCount atomic.Int32
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connectCount.Add(1)
		handler.ServeHTTP(w, r)
	}))
	defer proxySrv.Close()

	// Single keep-alive HTTP client reuses the same established CONNECT/TLS connection.
	client := newProxiedClient(t, proxySrv, ca, upstream)

	// 1. Active lease injects credential on permitted adapter route.
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	req.Header.Set("Authorization", "Bearer botbox-placeholder-abc")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET active lease: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// 2. Disallowed adapter route on same host does NOT inject secret and strips dummy placeholder.
	reqBadPath, _ := http.NewRequest(http.MethodGet, "https://api.github.com/internal/admin", nil)
	reqBadPath.Header.Set("Authorization", "Bearer botbox-placeholder-abc")
	resp, err = client.Do(reqBadPath)
	if err != nil {
		t.Fatalf("GET disallowed path: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	// 3. Advance injected clock past expiry -> same established TLS connection must stop injecting!
	nowMu.Lock()
	currentTime = currentTime.Add(11 * time.Minute)
	nowMu.Unlock()

	reqExpired, _ := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	reqExpired.Header.Set("Authorization", "Bearer botbox-placeholder-abc")
	resp, err = client.Do(reqExpired)
	if err != nil {
		t.Fatalf("GET expired lease: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	// 4. Restore clock, make rule active again, then return DB/policy error -> must fail closed (503), never fall back!
	nowMu.Lock()
	currentTime = currentTime.Add(-10 * time.Minute)
	nowMu.Unlock()
	policyMu.Lock()
	policyErr = errors.New("database offline")
	policyMu.Unlock()

	resp, err = client.Get("https://api.github.com/user")
	if err != nil {
		t.Fatalf("GET during policy outage: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 fail-closed on policy outage, got %d", resp.StatusCode)
	}

	// 5. Recover policy with EMPTY rules -> must never revive cached secret!
	policyMu.Lock()
	policyErr = nil
	snapshot.Rules = nil
	policyMu.Unlock()

	resp, err = client.Get("https://api.github.com/user")
	if err != nil {
		t.Fatalf("GET empty snapshot: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	logMu.Lock()
	got := append([]string(nil), authLog...)
	logMu.Unlock()
	if len(got) != 4 {
		t.Fatalf("expected 4 upstream hits (policy outage should not hit upstream), got %d: %v", len(got), got)
	}
	if got[0] != "Bearer ghp_supersecret_lease_1234" {
		t.Fatalf("hit 0 expected real token, got %q", got[0])
	}
	if got[1] != "" || got[2] != "" || got[3] != "" {
		t.Fatalf("expected empty auth on disallowed/expired/empty hits, got %v", got[1:])
	}
	if connectCount.Load() != 1 {
		t.Fatalf("expected all 5 requests on 1 established CONNECT/TLS tunnel, got %d connects", connectCount.Load())
	}
}

func TestResponseMediationAcrossStatusesEncodingsAndBinary(t *testing.T) {
	t.Parallel()

	const secret = "ghp_secret_echo_987654321" //nolint:gosec // synthetic test secret
	b64Secret := base64.StdEncoding.EncodeToString([]byte(secret))
	urlSecret := url.QueryEscape(secret)

	var mode atomic.Int32
	upstream, upstreamRoots := newHostTLSServer(t, "api.github.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case 1:
			// 200 OK JSON echoing raw, b64, urlencoded, and header forms across chunk boundaries.
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Echo", "Bearer "+secret)
			w.WriteHeader(http.StatusOK)
			fl, _ := w.(http.Flusher)
			_, _ = w.Write([]byte(`{"raw":"` + secret[:10]))
			if fl != nil {
				fl.Flush()
			}
			_, _ = w.Write([]byte(secret[10:] + `","b64":"` + b64Secret + `","url":"` + urlSecret + `"}`))
		case 2:
			// Gzip-compressed 200 OK echoing token.
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			_, _ = gz.Write([]byte("echo=" + secret))
			_ = gz.Close()
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "gzip")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(buf.Bytes())
		case 3:
			// Deflate-compressed 200 OK echoing token.
			var buf bytes.Buffer
			zw := zlib.NewWriter(&buf)
			_, _ = zw.Write([]byte("deflate=" + secret))
			_ = zw.Close()
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "deflate")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(buf.Bytes())
		case 4:
			// Unsupported brotli encoding -> fail closed 502!
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "br")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("raw-br-bytes-" + secret))
		case 5:
			// Unexpected binary response on JSON API adapter -> fail closed 502!
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte{0x00, 0x01, 0x02, 0x03})
		case 6:
			// Redirect echoing secret in Location header -> fail closed 502!
			w.Header().Set("Location", "https://evil.example/cb?tok="+urlSecret)
			w.WriteHeader(http.StatusFound)
		}
	}))
	defer upstream.Close()

	ca, err := mitmproxy.NewBotCA("bot-mediation-test")
	if err != nil {
		t.Fatalf("NewBotCA: %v", err)
	}
	handler, err := mitmproxy.NewHandler(mitmproxy.Config{
		CA:              ca,
		UpstreamRootCAs: upstreamRoots,
		AllowedHosts:    map[string]bool{"api.github.com": true},
		Rules: []mitmproxy.SecretRule{
			{
				ID:          "sec-gh",
				Scope:       "bot",
				Name:        "GH_TOKEN",
				AdapterID:   "github_api",
				DomainMatch: "api.github.com",
				Port:        "443",
				HeaderName:  "Authorization",
				Scheme:      mitmproxy.SchemeBearer,
				Value:       secret,
			},
		},
		DialContext: dialLocalTestServer(t, upstream),
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	var connectCount atomic.Int32
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connectCount.Add(1)
		handler.ServeHTTP(w, r)
	}))
	defer proxySrv.Close()
	client := newProxiedClient(t, proxySrv, ca, upstream)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	assertNoSecret := func(t *testing.T, resp *http.Response, body []byte) {
		t.Helper()
		for _, forbidden := range []string{secret, b64Secret, urlSecret} {
			if strings.Contains(string(body), forbidden) {
				t.Fatalf("secret form %q leaked into body: %q", forbidden, body)
			}
			for k, vals := range resp.Header {
				for _, v := range vals {
					if strings.Contains(v, forbidden) {
						t.Fatalf("secret form %q leaked into header %s: %q", forbidden, k, v)
					}
				}
			}
		}
	}

	for _, tc := range []struct {
		name       string
		mode       int32
		wantStatus int
		wantSubstr string
	}{
		{name: "chunked_200_scrubbed", mode: 1, wantStatus: http.StatusOK, wantSubstr: "[REDACTED]"},
		{name: "gzip_200_scrubbed", mode: 2, wantStatus: http.StatusOK, wantSubstr: "echo=[REDACTED]"},
		{name: "deflate_200_scrubbed", mode: 3, wantStatus: http.StatusOK, wantSubstr: "deflate=[REDACTED]"},
		{name: "unsupported_br_fails_closed", mode: 4, wantStatus: http.StatusBadGateway},
		{name: "unexpected_binary_fails_closed", mode: 5, wantStatus: http.StatusBadGateway},
		{name: "redirect_leak_fails_closed", mode: 6, wantStatus: http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode.Store(tc.mode)
			resp, err := client.Get("https://api.github.com/user")
			if err != nil {
				t.Fatalf("GET %s: %v", tc.name, err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status=%d want=%d body=%q", resp.StatusCode, tc.wantStatus, body)
			}
			assertNoSecret(t, resp, body)
			if tc.wantSubstr != "" && !strings.Contains(string(body), tc.wantSubstr) {
				t.Fatalf("expected %q in %q", tc.wantSubstr, body)
			}
		})
	}
}

func TestPassthroughTunnelInvalidatedWhenSecretAdded(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var auths []string
	upstream, upstreamRoots := newHostTLSServer(t, "api.github.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	ca, err := mitmproxy.NewBotCA("bot-invalidate-test")
	if err != nil {
		t.Fatalf("NewBotCA: %v", err)
	}
	var policyMu sync.RWMutex
	snapshot := mitmproxy.PolicySnapshot{
		Policy:       mitmproxy.PolicyAllowlist,
		AllowedHosts: []string{"api.github.com"},
	}

	handler, err := mitmproxy.NewHandler(mitmproxy.Config{
		CA:              ca,
		UpstreamRootCAs: upstreamRoots,
		DialContext:     dialLocalTestServer(t, upstream),
		DynamicPolicy: func(_ context.Context) (mitmproxy.PolicySnapshot, error) {
			policyMu.RLock()
			defer policyMu.RUnlock()
			return snapshot, nil
		},
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	var connectCount atomic.Int32
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connectCount.Add(1)
		handler.ServeHTTP(w, r)
	}))
	defer proxySrv.Close()
	client := newProxiedClient(t, proxySrv, ca, upstream)

	// 1. Opens passthrough CONNECT tunnel while no rule exists.
	resp, err := client.Get("https://api.github.com/user")
	if err != nil {
		t.Fatalf("initial passthrough GET: %v", err)
	}
	_ = resp.Body.Close()

	// 2. Add a rule and revalidate passthrough tunnels -> existing passthrough tunnel is closed.
	policyMu.Lock()
	snapshot.Rules = []mitmproxy.SecretRule{{
		ID:          "sec-new",
		Scope:       "lease",
		Name:        "GH_TOKEN",
		AdapterID:   "github_api",
		DomainMatch: "api.github.com",
		Port:        "443",
		HeaderName:  "Authorization",
		Scheme:      mitmproxy.SchemeBearer,
		Value:       "ghp_new_tunnel_secret",
	}}
	policyMu.Unlock()
	if err := handler.RevalidatePassthrough(context.Background()); err != nil {
		t.Fatalf("RevalidatePassthrough: %v", err)
	}

	// 3. Next request re-connects through MITM and receives credential injection.
	resp, err = client.Get("https://api.github.com/user")
	if err != nil {
		t.Fatalf("post-invalidation GET: %v", err)
	}
	_ = resp.Body.Close()

	mu.Lock()
	got := append([]string(nil), auths...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "" || got[1] != "Bearer ghp_new_tunnel_secret" {
		t.Fatalf("expected passthrough upgrade to MITM, got auths=%v", got)
	}
}
