package authbroker

import (
	"context"
	"crypto/tls"
	"errors"
	"github.com/dunkeltech/botbox/backend/internal/netguard"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type staticResolver struct {
	addresses []netip.Addr
	err       error
}

func (r staticResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r.addresses, r.err
}

func TestResolvePublicAddressesRejectsPrivateAndMixedDNS(t *testing.T) {
	for _, ips := range [][]string{{"127.0.0.1"}, {"10.0.0.1"}, {"169.254.169.254"}, {"::ffff:192.168.1.2"}, {"fc00::1"}, {"100.100.100.200"}, {"168.63.129.16"}, {"8.8.8.8", "10.0.0.1"}, {}} {
		r := staticResolver{}
		for _, ip := range ips {
			r.addresses = append(r.addresses, netip.MustParseAddr(ip))
		}
		if _, err := netguard.ResolvePublicAddresses(context.Background(), r, "gitlab.example.org"); err == nil {
			t.Fatalf("unsafe DNS accepted: %v", ips)
		}
	}
}

type changingResolver struct {
	mu      sync.Mutex
	address netip.Addr
}

func (r *changingResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return []netip.Addr{r.address}, nil
}
func (r *changingResolver) set(address string) {
	r.mu.Lock()
	r.address = netip.MustParseAddr(address)
	r.mu.Unlock()
}

func TestPublicProxyPinsEachConnectionAndPreservesTLS(t *testing.T) {
	ca, err := mitmproxy.NewBotCA("public-proxy-test")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ca.LeafCertificate("gitlab.example.org")
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("GitLab")) }))
	upstream.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*cert}}
	upstream.StartTLS()
	defer upstream.Close()
	proxy, err := newPublicOriginProxy("https://gitlab.example.org")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.close()
	resolver := &changingResolver{}
	resolver.set("8.8.8.8")
	proxy.resolver = resolver
	var mu sync.Mutex
	var dialed []string
	proxy.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		mu.Lock()
		dialed = append(dialed, address)
		mu.Unlock()
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(upstream.URL, "https://"))
	}
	proxyURL, _ := url.Parse(proxy.url)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: ca.CertPool()}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	read := func() {
		t.Helper()
		resp, err := client.Get("https://gitlab.example.org/")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if string(body) != "GitLab" {
			t.Fatalf("wrong body: %q", body)
		}
	}
	read()
	resolver.set("169.254.169.254")
	if resp, err := client.Get("https://gitlab.example.org/"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("DNS rebinding accepted")
	}
	resolver.set("8.8.4.4")
	read()
	mu.Lock()
	defer mu.Unlock()
	if len(dialed) != 2 || dialed[0] != "8.8.8.8:443" || dialed[1] != "8.8.4.4:443" {
		t.Fatalf("unchecked DNS dial: %v", dialed)
	}
}

func TestPublicProxyRejectsUnapprovedDestinationsAndDependencyErrors(t *testing.T) {
	for _, tt := range []struct {
		name, method, authority string
		resolver                staticResolver
		dialError               bool
		status                  int
	}{
		{name: "foreign redirect", method: "CONNECT", authority: "evil.example:443", status: 403},
		{name: "loopback bypass", method: "CONNECT", authority: "127.0.0.1:443", status: 403},
		{name: "wrong port", method: "CONNECT", authority: "gitlab.example.org:8443", status: 403},
		{name: "plaintext", method: "GET", authority: "gitlab.example.org:443", status: 403},
		{name: "DNS failure", method: "CONNECT", authority: "gitlab.example.org:443", resolver: staticResolver{err: errors.New("dns down")}, status: 403},
		{name: "DNS deadline", method: "CONNECT", authority: "gitlab.example.org:443", resolver: staticResolver{err: context.DeadlineExceeded}, status: 403},
		{name: "dial failure", method: "CONNECT", authority: "gitlab.example.org:443", resolver: staticResolver{addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}, dialError: true, status: 502},
	} {
		t.Run(tt.name, func(t *testing.T) {
			proxy := &publicOriginProxy{host: "gitlab.example.org", expiresAt: time.Now().Add(time.Minute), resolver: tt.resolver, dial: func(context.Context, string, string) (net.Conn, error) {
				if !tt.dialError {
					t.Error("unexpected upstream dial")
				}
				return nil, errors.New("upstream down")
			}}
			req := httptest.NewRequest(tt.method, "http://"+tt.authority, nil)
			req.Host = tt.authority
			if tt.method == http.MethodConnect {
				req.URL = &url.URL{Host: tt.authority}
			}
			recorder := httptest.NewRecorder()
			proxy.ServeHTTP(recorder, req)
			if recorder.Code != tt.status {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
