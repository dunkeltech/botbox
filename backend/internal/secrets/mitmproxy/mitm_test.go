// SPDX-License-Identifier: AGPL-3.0-only

package mitmproxy_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
)

func TestSelectiveTLSInterceptionAndGitAuth(t *testing.T) {
	t.Parallel()

	botCA, err := mitmproxy.NewBotCA("bot-atlas")
	if err != nil {
		t.Fatalf("NewBotCA: %v", err)
	}

	realSecret := "glpat-super-secret-live-token" //nolint:gosec // synthetic test secret
	placeholder := "placeholder-token-inside-sandbox"

	gitlabCA, err := mitmproxy.NewBotCA("upstream-gitlab")
	if err != nil {
		t.Fatalf("NewBotCA upstream-gitlab: %v", err)
	}
	gitlabCert, err := gitlabCA.LeafCertificate("gitlab.com")
	if err != nil {
		t.Fatalf("LeafCertificate gitlab.com: %v", err)
	}

	var sawAuthHeader string
	var mu sync.Mutex
	gitlabUpstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sawAuthHeader = r.Header.Get("Authorization")
		mu.Unlock()
		if r.URL.Path == "/team/repo.git/info/refs" {
			_, _ = w.Write([]byte("001e# service=git-upload-pack\n0000"))
			return
		}
		if r.URL.Path == "/leak-check" {
			// Simulate buggy upstream reflecting request Authorization back on 500 error.
			http.Error(w, "rejected header: "+r.Header.Get("Authorization"), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	gitlabUpstream.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{*gitlabCert},
	}
	gitlabUpstream.StartTLS()
	defer gitlabUpstream.Close()

	passthroughUpstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Upstream receives raw TLS directly from client; proxy must NOT have MITMed or added secrets.
		if r.Header.Get("Authorization") != "" {
			http.Error(w, "unexpected authorization", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("passthrough-ok"))
	}))
	defer passthroughUpstream.Close()

	upstreamPool := gitlabCA.CertPool()
	upstreamPool.AddCert(passthroughUpstream.Certificate())

	gitlabAddr := strings.TrimPrefix(gitlabUpstream.URL, "https://")
	passAddr := strings.TrimPrefix(passthroughUpstream.URL, "https://")

	var auditMu sync.Mutex
	var audits []mitmproxy.AuditEvent
	var logBuf bytes.Buffer

	proxyHandler, err := mitmproxy.NewHandler(mitmproxy.Config{
		CA: botCA,
		AllowedHosts: map[string]bool{
			"gitlab.com":  true,
			"example.com": true,
		},
		Secrets: map[string]mitmproxy.SecretRule{
			"gitlab.com": {
				Scheme:   mitmproxy.SchemeBasic,
				Username: "oauth2",
				Value:    realSecret,
			},
		},
		UpstreamRootCAs: upstreamPool,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialer := &net.Dialer{}
			switch addr {
			case "gitlab.com:443":
				return dialer.DialContext(ctx, network, gitlabAddr)
			case "example.com:443":
				return dialer.DialContext(ctx, network, passAddr)
			default:
				return nil, fmt.Errorf("unexpected dial target %s", addr)
			}
		},
		Logger: slog.New(slog.NewTextHandler(&logBuf, nil)),
		OnAudit: func(ev mitmproxy.AuditEvent) {
			auditMu.Lock()
			audits = append(audits, ev)
			auditMu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	proxySrv := httptest.NewServer(proxyHandler)
	defer proxySrv.Close()
	proxyURL, err := url.Parse(proxySrv.URL)
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}

	t.Run("MITM injects real GitLab Basic credential for git-upload-pack without leaking secret", func(t *testing.T) {
		sandboxClient := &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
				TLSClientConfig: &tls.Config{
					MinVersion: tls.VersionTLS12,
					// Sandbox trusts ONLY per-bot CA here: proves proxy re-signed leaf cleanly.
					RootCAs: botCA.CertPool(),
				},
			},
		}
		defer sandboxClient.CloseIdleConnections()

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://gitlab.com/team/repo.git/info/refs?service=git-upload-pack", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		fakeCred := base64.StdEncoding.EncodeToString([]byte("oauth2:" + placeholder))
		req.Header.Set("Authorization", "Basic "+fakeCred)

		resp, err := sandboxClient.Do(req)
		if err != nil {
			t.Fatalf("git info/refs through MITM proxy failed: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "git-upload-pack") {
			t.Fatalf("unexpected git response: %d %q", resp.StatusCode, string(body))
		}

		expectedHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte("oauth2:"+realSecret))
		mu.Lock()
		gotAuth := sawAuthHeader
		mu.Unlock()
		if gotAuth != expectedHeader {
			t.Fatalf("upstream Authorization = %q, want %q", gotAuth, expectedHeader)
		}

		// Verify error responses echoing credentials are scrubbed before reaching the sandbox.
		leakReq, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://gitlab.com/leak-check", nil)
		if err != nil {
			t.Fatalf("NewRequest leak-check: %v", err)
		}
		leakResp, err := sandboxClient.Do(leakReq)
		if err != nil {
			t.Fatalf("leak-check failed: %v", err)
		}
		leakBody, _ := io.ReadAll(leakResp.Body)
		_ = leakResp.Body.Close()
		if bytes.Contains(leakBody, []byte(realSecret)) || bytes.Contains(logBuf.Bytes(), []byte(realSecret)) {
			t.Fatalf("secret value leaked in response %q or logs %q", string(leakBody), logBuf.String())
		}
	})

	t.Run("non-secret allowlisted host uses raw TLS passthrough without MITM", func(t *testing.T) {
		passthroughClient := &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
				TLSClientConfig: &tls.Config{
					MinVersion: tls.VersionTLS12,
					// Trusts ONLY upstream server cert (valid for example.com), NOT botCA.
					RootCAs: upstreamPool,
				},
			},
		}
		defer passthroughClient.CloseIdleConnections()

		resp, err := passthroughClient.Get("https://example.com/health")
		if err != nil {
			t.Fatalf("passthrough GET failed: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || string(body) != "passthrough-ok" {
			t.Fatalf("unexpected passthrough response: %d %q", resp.StatusCode, string(body))
		}
	})

	t.Run("other bot CA cannot verify Bot A leaf certificate", func(t *testing.T) {
		otherCA, err := mitmproxy.NewBotCA("bot-other")
		if err != nil {
			t.Fatalf("NewBotCA other: %v", err)
		}
		wrongClient := &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
				TLSClientConfig: &tls.Config{
					MinVersion: tls.VersionTLS12,
					RootCAs:    otherCA.CertPool(),
				},
			},
		}
		resp, err := wrongClient.Get("https://gitlab.com/team/repo.git/info/refs")
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if err == nil {
			t.Fatal("expected TLS failure when verifying with a different bot CA")
		}
	})

	auditMu.Lock()
	recorded := append([]mitmproxy.AuditEvent(nil), audits...)
	auditMu.Unlock()

	var sawMITM, sawPass bool
	for _, ev := range recorded {
		if ev.Action == mitmproxy.ActionMITMTokenAdded && ev.Host == "gitlab.com" {
			sawMITM = true
		}
		if ev.Action == mitmproxy.ActionTunnelPassthrough && ev.Host == "example.com" {
			sawPass = true
		}
	}
	if !sawMITM || !sawPass {
		t.Fatalf("missing expected audit events: %+v", recorded)
	}
}

func TestMITMProxyNegativePaths(t *testing.T) {
	t.Parallel()

	botCA, err := mitmproxy.NewBotCA("bot-neg")
	if err != nil {
		t.Fatalf("NewBotCA: %v", err)
	}

	if _, err := mitmproxy.NewHandler(mitmproxy.Config{}); err == nil {
		t.Fatal("expected error when BotCA is nil")
	}

	t.Run("blocks non-allowlisted host, non-443 port, non-CONNECT method, and unreachable upstream", func(t *testing.T) {
		t.Parallel()

		dialErr := fmt.Errorf("dial refused")
		h, err := mitmproxy.NewHandler(mitmproxy.Config{
			CA:           botCA,
			AllowedHosts: map[string]bool{"down.example": true},
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return nil, dialErr
			},
		})
		if err != nil {
			t.Fatalf("NewHandler: %v", err)
		}

		tests := []struct {
			name     string
			method   string
			host     string
			wantCode int
		}{
			{name: "blocked host returns 403", method: http.MethodConnect, host: "evil.example:443", wantCode: http.StatusForbidden},
			{name: "non-443 port returns 403", method: http.MethodConnect, host: "down.example:8443", wantCode: http.StatusForbidden},
			{name: "non-CONNECT method returns 405", method: http.MethodGet, host: "down.example:443", wantCode: http.StatusMethodNotAllowed},
			{name: "unreachable upstream returns 502", method: http.MethodConnect, host: "down.example:443", wantCode: http.StatusBadGateway},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				req := httptest.NewRequest(tc.method, "http://"+tc.host, nil)
				req.Host = tc.host
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != tc.wantCode {
					t.Fatalf("status = %d, want %d (body=%q)", rec.Code, tc.wantCode, rec.Body.String())
				}
			})
		}
	})
}
