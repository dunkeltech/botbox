package authbroker

import (
	"context"
	"github.com/dunkeltech/botbox/backend/internal/netguard"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type publicOriginProxy struct {
	host        string
	resolver    netguard.Resolver
	dial        func(context.Context, string, string) (net.Conn, error)
	server      *http.Server
	url         string
	mu          sync.Mutex
	closed      bool
	connections map[net.Conn]bool
	expiresAt   time.Time
	expiryTimer *time.Timer
}

func newPublicOriginProxy(origin string) (*publicOriginProxy, error) {
	canonical, err := StrictLoginOrigin(origin)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	p := &publicOriginProxy{host: strings.TrimSuffix(strings.TrimPrefix(canonical, "https://"), ":443"), resolver: net.DefaultResolver, dial: dialer.DialContext, url: "http://" + listener.Addr().String(), connections: map[net.Conn]bool{}, expiresAt: time.Now().Add(DefaultBrokerSessionTTL)}
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	p.expiryTimer = time.AfterFunc(DefaultBrokerSessionTTL, p.close)
	go func() { _ = p.server.Serve(listener) }()
	return p, nil
}

func (p *publicOriginProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only the bound origin can receive traffic; redirects and third-party resources fail closed.
	if !time.Now().Before(p.expiresAt) || r.Method != http.MethodConnect || r.Host != p.host+":443" || r.URL.Host != r.Host || r.URL.User != nil {
		http.Error(w, "destination blocked", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	addresses, err := netguard.ResolvePublicAddresses(ctx, p.resolver, p.host)
	if err != nil {
		http.Error(w, "destination blocked", http.StatusForbidden)
		return
	}
	var upstream net.Conn
	for _, address := range addresses {
		// Dial the validated literal address so a second DNS lookup cannot rebind the destination.
		upstream, err = p.dial(ctx, "tcp", net.JoinHostPort(address.Unmap().String(), "443"))
		if err == nil {
			break
		}
	}
	if err != nil {
		http.Error(w, "destination unavailable", http.StatusBadGateway)
		return
	}
	defer func() { _ = upstream.Close() }()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "tunnel unavailable", http.StatusInternalServerError)
		return
	}
	client, buffer, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.connections[client] = true
	p.connections[upstream] = true
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.connections, client); delete(p.connections, upstream); p.mu.Unlock() }()
	// Bound hijacked connections as well as browser sessions, even if a peer never closes.
	deadline := p.expiresAt
	_ = client.SetDeadline(deadline)
	_ = upstream.SetDeadline(deadline)
	if _, err = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err = buffer.Flush(); err != nil {
		return
	}
	done := make(chan struct{})
	go func() { _, _ = io.Copy(upstream, buffer); _ = upstream.Close(); _ = client.Close(); close(done) }()
	_, _ = io.Copy(client, upstream)
	_ = client.Close()
	_ = upstream.Close()
	<-done
}

func (p *publicOriginProxy) close() {
	p.mu.Lock()
	p.closed = true
	if p.expiryTimer != nil {
		p.expiryTimer.Stop()
	}
	for conn := range p.connections {
		_ = conn.Close()
	}
	p.mu.Unlock()
	_ = p.server.Close()
}
