// SPDX-License-Identifier: AGPL-3.0-only

package mitmproxy_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
)

type fixedDestinationResolver []netip.Addr

func (resolver fixedDestinationResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return resolver, nil
}

func TestPublicEgressRejectsNonPublicDNSAnswers(t *testing.T) {
	t.Parallel()
	ca, err := mitmproxy.NewBotCA("public-egress")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		addresses fixedDestinationResolver
	}{
		{"private", fixedDestinationResolver{netip.MustParseAddr("192.168.1.1")}},
		{"mixed", fixedDestinationResolver{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("127.0.0.1")}},
		{"empty", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, err := mitmproxy.NewHandler(mitmproxy.Config{
				CA: ca, Policy: mitmproxy.PolicyOpen, PublicOnly: true, PublicResolver: tc.addresses,
				DialContext: func(context.Context, string, string) (net.Conn, error) {
					t.Fatal("unsafe destination reached the dialer")
					return nil, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodConnect, "test.example:443", nil)
			handler.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("private destination: got %d, want 403", w.Code)
			}
		})
	}
}

func TestCredentialedEgressChecksPublicDestination(t *testing.T) {
	t.Parallel()
	ca, err := mitmproxy.NewBotCA("credentialed-egress")
	if err != nil {
		t.Fatal(err)
	}
	var dialed atomic.Bool
	handler, err := mitmproxy.NewHandler(mitmproxy.Config{
		CA: ca, Policy: mitmproxy.PolicyOpen, PublicOnly: true,
		PublicResolver: fixedDestinationResolver{netip.MustParseAddr("127.0.0.1")},
		Rules: []mitmproxy.SecretRule{{
			DomainMatch: "internal.test", HeaderName: "Authorization", Scheme: mitmproxy.SchemeBearer, Value: "fixture-value",
		}},
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			dialed.Store(true)
			return nil, errors.New("unexpected outbound dial")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(handler)
	defer proxy.Close()
	client := newProxiedClient(t, proxy, ca, nil)
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://internal.test/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden || dialed.Load() {
		t.Fatalf("private credentialed destination: status=%d outbound dial=%v", resp.StatusCode, dialed.Load())
	}
}

func dialLocalTestServer(t *testing.T, srv *httptest.Server) func(context.Context, string, string) (net.Conn, error) {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	targetAddr := u.Host
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, targetAddr)
	}
}

func newProxiedClient(t *testing.T, proxySrv *httptest.Server, ca *mitmproxy.BotCA, upstream *httptest.Server) *http.Client {
	t.Helper()
	proxyURL, err := url.Parse(proxySrv.URL)
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca.CertPEM()) {
		t.Fatal("append MITM CA PEM")
	}
	if upstream != nil {
		roots.AddCert(upstream.Certificate())
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    roots,
			},
		},
	}
}

func TestMITMProxySubdomainAndPathPrefixAndQueryStripping(t *testing.T) {
	t.Parallel()

	var upstreamAuths []string
	var mu sync.Mutex
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		upstreamAuths = append(upstreamAuths, r.URL.Path+"|"+r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream-payload"))
	}))
	defer upstream.Close()

	ca, err := mitmproxy.NewBotCA("bot-phase3-test")
	if err != nil {
		t.Fatalf("NewBotCA: %v", err)
	}

	var auditMu sync.Mutex
	var events []mitmproxy.AuditEvent
	handler, err := mitmproxy.NewHandler(mitmproxy.Config{
		CA:              ca,
		UpstreamRootCAs: upstream.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs,
		AllowedPatterns: []string{"*.example.com"},
		Rules: []mitmproxy.SecretRule{
			{
				ID:              "ws-sec-1",
				Scope:           "workspace",
				Name:            "WS_TOKEN",
				DomainMatch:     "example.com",
				MatchSubdomains: true,
				PathPrefix:      "/v1",
				HeaderName:      "Authorization",
				Scheme:          mitmproxy.SchemeBearer,
				Value:           "ws-secret-should-be-overridden",
			},
			{
				ID:              "bot-sec-1",
				Scope:           "bot",
				Name:            "BOT_TOKEN",
				DomainMatch:     "api.example.com",
				MatchSubdomains: false,
				PathPrefix:      "/v1/private",
				HeaderName:      "Authorization",
				Scheme:          mitmproxy.SchemeBearer,
				Value:           "bot-private-real-secret",
			},
		},
		DialContext: dialLocalTestServer(t, upstream),
		OnAudit: func(ev mitmproxy.AuditEvent) {
			auditMu.Lock()
			events = append(events, ev)
			auditMu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	proxySrv := httptest.NewServer(handler)
	defer proxySrv.Close()
	client := newProxiedClient(t, proxySrv, ca, upstream)

	// 1. Private path hits bot-scoped rule and strips query params from audit path.
	resp, err := client.Get("https://api.example.com/v1/private/repos?secret_q=do_not_log")
	if err != nil {
		t.Fatalf("GET private path: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "upstream-payload" {
		t.Fatalf("unexpected private response: %d %q", resp.StatusCode, body)
	}

	// 2. General /v1 path hits workspace subdomain rule.
	resp, err = client.Get("https://sub.example.com/v1/items?page=2")
	if err != nil {
		t.Fatalf("GET sub /v1 path: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	// 3. Non-matching path on intercepted host passes through WITHOUT secret injection.
	reqPublic, err := http.NewRequest(http.MethodGet, "https://api.example.com/public/health?ping=1", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	reqPublic.Header.Set("Authorization", "Bearer client-nonsecret")
	resp, err = client.Do(reqPublic)
	if err != nil {
		t.Fatalf("GET public path: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	// 4. Suffix-spoofed host must be blocked with 403.
	blockedResp, err := client.Get("https://notexample.com/v1/private")
	if blockedResp != nil && blockedResp.Body != nil {
		_ = blockedResp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected blocked suffix-spoofed host request to fail")
	}

	mu.Lock()
	gotAuths := append([]string(nil), upstreamAuths...)
	mu.Unlock()
	if len(gotAuths) != 3 {
		t.Fatalf("upstream received %d requests, want 3: %v", len(gotAuths), gotAuths)
	}
	if gotAuths[0] != "/v1/private/repos|Bearer bot-private-real-secret" {
		t.Fatalf("private request auth mismatch: %q", gotAuths[0])
	}
	if gotAuths[1] != "/v1/items|Bearer ws-secret-should-be-overridden" {
		t.Fatalf("subdomain /v1 auth mismatch: %q", gotAuths[1])
	}
	if gotAuths[2] != "/public/health|Bearer client-nonsecret" {
		t.Fatalf("non-matching PathPrefix leaked or stripped auth: %q", gotAuths[2])
	}

	auditMu.Lock()
	gotEvents := append([]mitmproxy.AuditEvent(nil), events...)
	auditMu.Unlock()
	if len(gotEvents) != 4 {
		t.Fatalf("expected 4 audit events, got %d: %+v", len(gotEvents), gotEvents)
	}
	for _, ev := range gotEvents {
		if strings.Contains(ev.Path, "?") || strings.Contains(ev.Path, "do_not_log") {
			t.Fatalf("query string leaked into audit event path: %+v", ev)
		}
	}
	if gotEvents[0].Action != mitmproxy.ActionMITMTokenAdded || gotEvents[0].SecretID != "bot-sec-1" || gotEvents[0].SecretScope != "bot" || gotEvents[0].Bytes <= 0 {
		t.Fatalf("unexpected first audit event: %+v", gotEvents[0])
	}
	if gotEvents[2].Action != mitmproxy.ActionTunnelPassthrough || gotEvents[2].SecretID != "" {
		t.Fatalf("expected passthrough audit event for non-matching path: %+v", gotEvents[2])
	}
	if gotEvents[3].Action != mitmproxy.ActionBlockedHost || gotEvents[3].Status != http.StatusForbidden {
		t.Fatalf("expected blocked audit event: %+v", gotEvents[3])
	}
}

func TestMITMProxyPolicyOpenAndDynamicReload(t *testing.T) {
	t.Parallel()

	var headerMu sync.Mutex
	var receivedKeys []string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headerMu.Lock()
		receivedKeys = append(receivedKeys, r.Header.Get("X-Api-Key"))
		headerMu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	ca, err := mitmproxy.NewBotCA("bot-open-test")
	if err != nil {
		t.Fatalf("NewBotCA: %v", err)
	}

	var policyMu sync.RWMutex
	snapshot := mitmproxy.PolicySnapshot{
		Policy: mitmproxy.PolicyOpen,
		Rules: []mitmproxy.SecretRule{
			{
				ID:          "sec-open",
				Scope:       "bot",
				Name:        "API_KEY",
				DomainMatch: "secret.example.com",
				HeaderName:  "X-Api-Key",
				Scheme:      mitmproxy.SchemeRaw,
				Value:       "raw-api-key-999",
			},
		},
	}

	handler, err := mitmproxy.NewHandler(mitmproxy.Config{
		CA:              ca,
		UpstreamRootCAs: upstream.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs,
		DynamicPolicy: func(_ context.Context) (mitmproxy.PolicySnapshot, error) {
			policyMu.RLock()
			defer policyMu.RUnlock()
			return snapshot, nil
		},
		DialContext: dialLocalTestServer(t, upstream),
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	proxySrv := httptest.NewServer(handler)
	defer proxySrv.Close()
	client := newProxiedClient(t, proxySrv, ca, upstream)

	// Open policy passes arbitrary non-secret host directly.
	resp, err := client.Get("https://anything.example.com/hello")
	if err != nil {
		t.Fatalf("GET arbitrary host under PolicyOpen: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	// Secret-bound host still receives MITM credential injection under PolicyOpen.
	resp, err = client.Get("https://secret.example.com/call")
	if err != nil {
		t.Fatalf("GET secret host under PolicyOpen: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	headerMu.Lock()
	gotKeys := append([]string(nil), receivedKeys...)
	headerMu.Unlock()
	if len(gotKeys) != 2 || gotKeys[0] != "" || gotKeys[1] != "raw-api-key-999" {
		t.Fatalf("unexpected upstream headers under PolicyOpen: %v", gotKeys)
	}

	// Switch dynamically back to empty allowlist -> new connections immediately block.
	policyMu.Lock()
	snapshot = mitmproxy.PolicySnapshot{Policy: mitmproxy.PolicyAllowlist}
	policyMu.Unlock()

	freshClient := newProxiedClient(t, proxySrv, ca, upstream)
	blockedResp, err := freshClient.Get("https://anything.example.com/hello")
	if blockedResp != nil && blockedResp.Body != nil {
		_ = blockedResp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected request to fail after dynamic switch to empty allowlist")
	}
}
