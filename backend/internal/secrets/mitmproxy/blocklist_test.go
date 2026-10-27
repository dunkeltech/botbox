package mitmproxy_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
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

func TestBlocklistPrecedenceWildcardsAndSecrets(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		policy    mitmproxy.EgressPolicy
		allowed   []string
		blocked   []string
		host      string
		wantAllow bool
		wantBlock string
	}{
		{
			name:      "exact block overrides open mode",
			policy:    mitmproxy.PolicyOpen,
			blocked:   []string{"telemetry.example.net"},
			host:      "telemetry.example.net",
			wantAllow: false,
			wantBlock: "telemetry.example.net",
		},
		{
			name:      "normalized host and trailing dot match block",
			policy:    mitmproxy.PolicyAllowlist,
			allowed:   []string{"api.example.com"},
			blocked:   []string{"API.Example.COM."},
			host:      "api.example.com.",
			wantAllow: false,
			wantBlock: "api.example.com",
		},
		{
			name:      "wildcard block matches nested subdomain",
			policy:    mitmproxy.PolicyOpen,
			blocked:   []string{"*.tracking.example"},
			host:      "a.b.tracking.example",
			wantAllow: false,
			wantBlock: "*.tracking.example",
		},
		{
			name:      "wildcard block does not match apex domain",
			policy:    mitmproxy.PolicyOpen,
			blocked:   []string{"*.tracking.example"},
			host:      "tracking.example",
			wantAllow: true,
			wantBlock: "",
		},
		{
			name:      "broader allow with narrower deny exception",
			policy:    mitmproxy.PolicyAllowlist,
			allowed:   []string{"*.example.com"},
			blocked:   []string{"api.example.com"},
			host:      "api.example.com",
			wantAllow: false,
			wantBlock: "api.example.com",
		},
		{
			name:      "broader allow still permits sibling subdomain",
			policy:    mitmproxy.PolicyAllowlist,
			allowed:   []string{"*.example.com"},
			blocked:   []string{"api.example.com"},
			host:      "docs.example.com",
			wantAllow: true,
			wantBlock: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotAllow, gotBlock := mitmproxy.EvaluateHostAccess(tc.policy, tc.allowed, tc.blocked, tc.host)
			if gotAllow != tc.wantAllow || gotBlock != tc.wantBlock {
				t.Fatalf("got (%v, %q), want (%v, %q)", gotAllow, gotBlock, tc.wantAllow, tc.wantBlock)
			}
		})
	}

	// Verify explicit block overrides active secret rules without leaking secret value.
	//nolint:gosec // synthetic test fixture credential
	const secretVal = "ghp_block_override_secret_9988"
	var upstreamHits atomic.Int32
	upstream, upstreamRoots := newHostTLSServer(t, "api.github.com", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	ca, err := mitmproxy.NewBotCA("bot-block-secret")
	if err != nil {
		t.Fatalf("NewBotCA: %v", err)
	}
	var auditMu sync.Mutex
	var auditEvents []mitmproxy.AuditEvent
	handler, err := mitmproxy.NewHandler(mitmproxy.Config{
		CA:              ca,
		UpstreamRootCAs: upstreamRoots,
		Policy:          mitmproxy.PolicyOpen,
		AllowedPatterns: []string{"api.github.com"},
		BlockedPatterns: []string{"*.github.com"},
		Rules: []mitmproxy.SecretRule{{
			ID:          "sec-1",
			Scope:       "bot",
			Name:        "GH",
			DomainMatch: "api.github.com",
			HeaderName:  "Authorization",
			Scheme:      mitmproxy.SchemeBearer,
			Value:       secretVal,
		}},
		DialContext: dialLocalTestServer(t, upstream),
		OnAudit: func(ev mitmproxy.AuditEvent) {
			auditMu.Lock()
			auditEvents = append(auditEvents, ev)
			auditMu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	proxySrv := httptest.NewServer(handler)
	defer proxySrv.Close()
	client := newProxiedClient(t, proxySrv, ca, upstream)

	resp, err := client.Get("https://api.github.com/user")
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected wildcard block to override secret rule and open mode")
	}
	if upstreamHits.Load() != 0 {
		t.Fatalf("blocked host reached upstream %d times", upstreamHits.Load())
	}
	auditMu.Lock()
	defer auditMu.Unlock()
	if len(auditEvents) != 1 || auditEvents[0].Action != mitmproxy.ActionBlockedHost {
		t.Fatalf("unexpected audit events: %+v", auditEvents)
	}
}

func openRawTunnel(t *testing.T, proxyAddr, targetHost string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if _, err := io.WriteString(conn, "CONNECT "+targetHost+":443 HTTP/1.1\r\nHost: "+targetHost+":443\r\n\r\n"); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if !strings.Contains(statusLine, "200 Connection Established") {
		t.Fatalf("unexpected CONNECT status: %q", statusLine)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}
	return conn, br
}

func TestPassthroughTunnelClosedOnExactAndWildcardBlockAndRefreshFailure(t *testing.T) {
	t.Parallel()

	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	defer func() { _ = echoListener.Close() }()
	go func() {
		for {
			c, err := echoListener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()

	ca, err := mitmproxy.NewBotCA("bot-tunnel-close")
	if err != nil {
		t.Fatalf("NewBotCA: %v", err)
	}

	var mu sync.Mutex
	snap := mitmproxy.PolicySnapshot{
		Policy:       mitmproxy.PolicyAllowlist,
		Revision:     1,
		AllowedHosts: []string{"sub.example.com", "keep.example.org"},
	}
	var failRefresh atomic.Bool
	var ackOrder []string
	var ackMu sync.Mutex

	nowMu := sync.Mutex{}
	curTime := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

	handler, err := mitmproxy.NewHandler(mitmproxy.Config{
		CA:                 ca,
		PolicyFreshnessMax: time.Second,
		Now: func() time.Time {
			nowMu.Lock()
			defer nowMu.Unlock()
			return curTime
		},
		DynamicPolicy: func(context.Context) (mitmproxy.PolicySnapshot, error) {
			if failRefresh.Load() {
				return mitmproxy.PolicySnapshot{}, errors.New("db unavailable")
			}
			mu.Lock()
			defer mu.Unlock()
			return snap, nil
		},
		OnPolicyAdopted: func(_ context.Context, rev int64) error {
			ackMu.Lock()
			ackOrder = append(ackOrder, fmt.Sprintf("ack:%d", rev))
			ackMu.Unlock()
			return nil
		},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, echoListener.Addr().String())
		},
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	proxySrv := httptest.NewServer(handler)
	defer proxySrv.Close()
	proxyURL, _ := url.Parse(proxySrv.URL)

	// Open two passthrough tunnels: sub.example.com and keep.example.org.
	subConn, subReader := openRawTunnel(t, proxyURL.Host, "sub.example.com")
	keepConn, keepReader := openRawTunnel(t, proxyURL.Host, "keep.example.org")

	assertEcho := func(c net.Conn, r *bufio.Reader, msg string) error {
		if _, err := io.WriteString(c, msg); err != nil {
			return err
		}
		buf := make([]byte, len(msg))
		_, err := io.ReadFull(r, buf)
		if err != nil {
			return err
		}
		if string(buf) != msg {
			return errors.New("mismatch")
		}
		return nil
	}

	if err := assertEcho(subConn, subReader, "ping1\n"); err != nil {
		t.Fatalf("sub tunnel initial echo: %v", err)
	}
	if err := assertEcho(keepConn, keepReader, "keep1\n"); err != nil {
		t.Fatalf("keep tunnel initial echo: %v", err)
	}

	// Wildcard block *.example.com at revision 2 closes sub.example.com while keep.example.org stays open.
	mu.Lock()
	snap = mitmproxy.PolicySnapshot{
		Policy:       mitmproxy.PolicyAllowlist,
		Revision:     2,
		AllowedHosts: []string{"sub.example.com", "keep.example.org"},
		BlockedHosts: []string{"*.example.com"},
	}
	mu.Unlock()

	if err := handler.RevalidatePassthrough(context.Background()); err != nil {
		t.Fatalf("RevalidatePassthrough rev 2: %v", err)
	}
	if handler.AcknowledgedRevision() != 2 {
		t.Fatalf("acknowledged revision = %d, want 2", handler.AcknowledgedRevision())
	}

	// Stale snapshot (revision 1) must be ignored.
	adopted, err := handler.AdoptSnapshot(context.Background(), mitmproxy.PolicySnapshot{
		Policy:       mitmproxy.PolicyAllowlist,
		Revision:     1,
		AllowedHosts: []string{"sub.example.com", "keep.example.org"},
	})
	if err != nil || adopted {
		t.Fatalf("expected stale revision to be ignored, adopted=%v err=%v", adopted, err)
	}

	// Blocked tunnel must fail reads/writes.
	_ = subConn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if err := assertEcho(subConn, subReader, "after-block\n"); err == nil {
		t.Fatal("expected sub.example.com tunnel to be closed after wildcard block")
	}
	// Unrelated tunnel stays usable.
	if err := assertEcho(keepConn, keepReader, "keep2\n"); err != nil {
		t.Fatalf("expected keep.example.org tunnel to remain open: %v", err)
	}

	// Advancing clock beyond 1s freshness window without refresh closes remaining tunnels fail-closed.
	nowMu.Lock()
	curTime = curTime.Add(1500 * time.Millisecond)
	nowMu.Unlock()
	if handler.CheckFreshness() {
		t.Fatal("expected CheckFreshness to report stale after >1s")
	}
	_ = keepConn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if err := assertEcho(keepConn, keepReader, "after-stale\n"); err == nil {
		t.Fatal("expected keep.example.org tunnel to close when freshness window expired")
	}

	// Refresh failure also closes any active tunnel and fails closed on CONNECT.
	failRefresh.Store(false)
	if err := handler.RevalidatePassthrough(context.Background()); err != nil {
		t.Fatalf("recover refresh: %v", err)
	}
	keepConn2, keepReader2 := openRawTunnel(t, proxyURL.Host, "keep.example.org")
	if err := assertEcho(keepConn2, keepReader2, "keep3\n"); err != nil {
		t.Fatalf("reopened keep tunnel: %v", err)
	}
	failRefresh.Store(true)
	if err := handler.RevalidatePassthrough(context.Background()); err == nil {
		t.Fatal("expected RevalidatePassthrough error when DB fails")
	}
	_ = keepConn2.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if err := assertEcho(keepConn2, keepReader2, "after-db-fail\n"); err == nil {
		t.Fatal("expected tunnel to close on DB refresh failure")
	}
}

func TestConnectDialRegisterRaceAgainstConcurrentBlock(t *testing.T) {
	t.Parallel()

	ca, err := mitmproxy.NewBotCA("bot-dial-race")
	if err != nil {
		t.Fatalf("NewBotCA: %v", err)
	}

	dialEntered := make(chan struct{})
	releaseDial := make(chan struct{})
	var dialCtxCanceled atomic.Bool

	handler, err := mitmproxy.NewHandler(mitmproxy.Config{
		CA: ca,
		DynamicPolicy: func(context.Context) (mitmproxy.PolicySnapshot, error) {
			return mitmproxy.PolicySnapshot{
				Policy:       mitmproxy.PolicyAllowlist,
				Revision:     1,
				AllowedHosts: []string{"race.example.com"},
			}, nil
		},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			close(dialEntered)
			select {
			case <-releaseDial:
				c1, c2 := net.Pipe()
				_ = c2.Close()
				return c1, nil
			case <-ctx.Done():
				dialCtxCanceled.Store(true)
				return nil, ctx.Err()
			}
		},
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	proxySrv := httptest.NewServer(handler)
	defer proxySrv.Close()
	proxyURL, _ := url.Parse(proxySrv.URL)

	conn, err := net.Dial("tcp", proxyURL.Host)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer func() { _ = conn.Close() }()

	respCh := make(chan string, 1)
	go func() {
		_, _ = io.WriteString(conn, "CONNECT race.example.com:443 HTTP/1.1\r\nHost: race.example.com:443\r\n\r\n")
		raw, _ := io.ReadAll(conn)
		respCh <- string(raw)
	}()

	<-dialEntered
	// Adopt a block while DialContext is mid-flight!
	if _, err := handler.AdoptSnapshot(context.Background(), mitmproxy.PolicySnapshot{
		Policy:       mitmproxy.PolicyAllowlist,
		Revision:     2,
		AllowedHosts: []string{"race.example.com"},
		BlockedHosts: []string{"race.example.com"},
	}); err != nil {
		t.Fatalf("AdoptSnapshot: %v", err)
	}
	close(releaseDial)

	rawResp := <-respCh
	if strings.Contains(rawResp, "200 Connection Established") {
		t.Fatalf("in-flight CONNECT escaped concurrent block: %q", rawResp)
	}
	if !strings.Contains(rawResp, "403") {
		t.Fatalf("expected 403 Forbidden for blocked in-flight CONNECT, got: %q", rawResp)
	}
	if !dialCtxCanceled.Load() {
		t.Fatal("expected in-flight dial context to be canceled immediately on block")
	}
}

func TestExistingMITMConnectionRejectsSubsequentRequestAfterBlock(t *testing.T) {
	t.Parallel()

	//nolint:gosec // synthetic test fixture credential
	const secretVal = "tok_mitm_revoke_12345"
	var upstreamHits atomic.Int32
	upstream, upstreamRoots := newHostTLSServer(t, "api.example.com", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	ca, err := mitmproxy.NewBotCA("bot-mitm-block")
	if err != nil {
		t.Fatalf("NewBotCA: %v", err)
	}

	var mu sync.Mutex
	snap := mitmproxy.PolicySnapshot{
		Policy:       mitmproxy.PolicyAllowlist,
		Revision:     1,
		AllowedHosts: []string{"api.example.com"},
		Rules: []mitmproxy.SecretRule{{
			ID:          "sec-1",
			Scope:       "bot",
			Name:        "API",
			DomainMatch: "api.example.com",
			HeaderName:  "Authorization",
			Scheme:      mitmproxy.SchemeBearer,
			Value:       secretVal,
		}},
	}

	var auditLog bytes.Buffer
	handler, err := mitmproxy.NewHandler(mitmproxy.Config{
		CA:              ca,
		UpstreamRootCAs: upstreamRoots,
		DialContext:     dialLocalTestServer(t, upstream),
		DynamicPolicy: func(context.Context) (mitmproxy.PolicySnapshot, error) {
			mu.Lock()
			defer mu.Unlock()
			return snap, nil
		},
		OnAudit: func(ev mitmproxy.AuditEvent) {
			mu.Lock()
			auditLog.WriteString(string(ev.Action) + ":" + ev.Host + "\n")
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	proxySrv := httptest.NewServer(handler)
	defer proxySrv.Close()
	client := newProxiedClient(t, proxySrv, ca, upstream)
	// Reuse the same persistent HTTP/TLS MITM connection across requests.
	client.Transport.(*http.Transport).DisableKeepAlives = false

	resp1, err := client.Get("https://api.example.com/first")
	if err != nil {
		t.Fatalf("first MITM request: %v", err)
	}
	_, _ = io.ReadAll(resp1.Body)
	_ = resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d", resp1.StatusCode)
	}

	// Block host while MITM keep-alive connection remains open.
	mu.Lock()
	snap.Revision = 2
	snap.BlockedHosts = []string{"api.example.com"}
	mu.Unlock()

	resp2, err := client.Get("https://api.example.com/second")
	if err != nil {
		t.Fatalf("second MITM request transport error: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 on existing MITM connection after block, got %d (%s)", resp2.StatusCode, string(body2))
	}
	if strings.Contains(string(body2), secretVal) {
		t.Fatal("secret leaked in blocked response")
	}
	if upstreamHits.Load() != 1 {
		t.Fatalf("blocked request reached upstream (hits=%d)", upstreamHits.Load())
	}
	_ = tls.VersionTLS12
}
