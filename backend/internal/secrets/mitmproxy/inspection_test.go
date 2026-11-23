// SPDX-License-Identifier: AGPL-3.0-only

package mitmproxy_test

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
)

func TestNativeTLSTunnelPreservesOriginAndRejectsDifferentSNI(t *testing.T) {
	const host = "oauth2.googleapis.com"
	issuer, err := mitmproxy.NewBotCA("origin")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := issuer.LeafCertificate(host)
	if err != nil {
		t.Fatal(err)
	}
	alternate, err := issuer.LeafCertificate("cloudcode-pa.googleapis.com")
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || r.TLS.ServerName != host {
			http.Error(w, "unexpected TLS hostname", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, "fixture login")
	}))
	upstream.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*leaf, *alternate}}
	upstream.StartTLS()
	defer upstream.Close()
	egressCA, err := mitmproxy.NewBotCA("egress")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := mitmproxy.NewHandler(mitmproxy.Config{
		CA: egressCA, AllowedHosts: map[string]bool{host: true},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr != host+":443" {
				t.Errorf("unexpected upstream: %s", addr)
			}
			return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(upstream.URL, "https://"))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(handler.ServeUnmodified))
	defer server.Close()
	proxy, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, serverName string }{
		{name: "direct TLS reaches OAuth"},
		{name: "wrong SNI is stopped", serverName: "cloudcode-pa.googleapis.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &http.Transport{
				Proxy:           http.ProxyURL(proxy),
				TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: issuer.CertPool(), ServerName: tc.serverName},
			}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			res, err := client.Get("https://" + host + "/token")
			if tc.serverName != "" {
				if err == nil {
					_ = res.Body.Close()
					t.Fatal("accepted a mismatched TLS server name")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = res.Body.Close() }()
			body, err := io.ReadAll(res.Body)
			if err != nil || string(body) != "fixture login" {
				t.Fatalf("unexpected reply: %s, %v", body, err)
			}
		})
	}
}

type inspectedFixtureResolver struct{}

func (inspectedFixtureResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
}

func TestInspectedTLSVerifiesUpstreamAndRejectsFronting(t *testing.T) {
	const host = "daily-cloudcode-pa.googleapis.com"
	originCA, err := mitmproxy.NewBotCA("origin")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := originCA.LeafCertificate(host)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Host != host || r.Header.Get("Authorization") != "Bearer fixture-native-login" {
			http.Error(w, "unexpected upstream request", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, "fixture model response")
	}))
	upstream.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*leaf}}
	upstream.StartTLS()
	defer upstream.Close()

	egressCA, err := mitmproxy.NewBotCA("egress")
	if err != nil {
		t.Fatal(err)
	}
	var blocked atomic.Bool
	handler, err := mitmproxy.NewHandler(mitmproxy.Config{
		CA: egressCA, UpstreamRootCAs: originCA.CertPool(), PublicResolver: inspectedFixtureResolver{},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr != "1.1.1.1:443" {
				t.Errorf("unexpected pinned address %s", addr)
			}
			return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(upstream.URL, "https://"))
		},
		DynamicPolicy: func(context.Context) (mitmproxy.PolicySnapshot, error) {
			snap := mitmproxy.PolicySnapshot{Policy: mitmproxy.PolicyAllowlist, AllowedHosts: []string{host}}
			if blocked.Load() {
				snap.BlockedHosts = []string{host}
				snap.Revision++
			}
			return snap, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeInspected(w, r, func(w http.ResponseWriter, r *http.Request, origin string, transport http.RoundTripper) {
			out, err := http.NewRequestWithContext(r.Context(), r.Method, "https://"+origin+"/fixture", nil)
			if err != nil {
				t.Errorf("build inspected fixture request: %v", err)
				return
			}
			out.Header.Set("Authorization", r.Header.Get("Authorization"))
			res, err := transport.RoundTrip(out)
			if err != nil {
				t.Errorf("inspected fixture transport: %v", err)
				return
			}
			defer func() { _ = res.Body.Close() }()
			w.WriteHeader(res.StatusCode)
			_, _ = io.Copy(w, res.Body)
		})
	}))
	defer server.Close()
	proxy, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: egressCA.CertPool()}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	for _, tc := range []struct {
		authority  string
		blocked    bool
		wantStatus int
	}{
		{host, false, 200},
		{"oauth2.googleapis.com", false, 403},
		{host, true, 403},
	} {
		blocked.Store(tc.blocked)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://"+host+"/fixture", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = tc.authority
		req.Header.Set("Authorization", "Bearer fixture-native-login")
		res, err := client.Do(req)
		if tc.blocked && err != nil {
			continue // A revision can close the TLS session before sending the denial.
		}
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		if res.StatusCode != tc.wantStatus {
			t.Fatalf("inspected status = %d, want %d", res.StatusCode, tc.wantStatus)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("got %d model requests, want only the authorized one", calls.Load())
	}
}
