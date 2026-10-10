package mitmproxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"time"
)

// AuthScheme defines how an injected egress secret is formatted on upstream requests.
type AuthScheme string

// Supported HTTP credential formatting schemes for selective MITM injection.
const (
	SchemeBearer AuthScheme = "bearer"
	SchemeBasic  AuthScheme = "basic"
	SchemeRaw    AuthScheme = "raw"
)

// AuditAction classifies an egress proxy decision without capturing any secret material.
type AuditAction string

// Proxy audit action labels recorded for egress observability.
const (
	ActionBlockedHost       AuditAction = "blocked_host"
	ActionTunnelPassthrough AuditAction = "tunnel_passthrough" //nolint:gosec // audit label constant, not a credential
	ActionMITMTokenAdded    AuditAction = "mitm_token_added"   //nolint:gosec // audit label constant, not a credential
)

// SecretRule binds a destination hostname (and optional subdomain/path scope) to an
// injected credential kept outside the sandbox.
type SecretRule struct {
	ID              string
	Scope           string
	Name            string
	DomainMatch     string
	MatchSubdomains bool
	PathPrefix      string
	HeaderName      string
	Scheme          AuthScheme
	Username        string
	Value           string
}

// AuditEvent records an allow/block/token-injection decision for observability without leaking secrets.
type AuditEvent struct {
	Host        string
	Port        string
	Method      string
	Path        string
	Action      AuditAction
	Status      int
	SecretID    string
	SecretScope string
	SecretName  string
	Bytes       int64
	LatencyMs   int64
}

// Config configures the selective TLS-intercepting egress CONNECT proxy.
type Config struct {
	CA               *BotCA
	Policy           EgressPolicy
	AllowedHosts     map[string]bool
	AllowedPatterns  []string
	Secrets          map[string]SecretRule
	Rules            []SecretRule
	DynamicPolicy    func(context.Context) (PolicySnapshot, error)
	UpstreamRootCAs  *x509.CertPool
	DialContext      func(ctx context.Context, network, addr string) (net.Conn, error)
	DialTimeout      time.Duration
	Logger           *slog.Logger
	OnAudit          func(AuditEvent)
	AllowCustomPorts bool
}

// Handler implements HTTP CONNECT with allowlist filtering, raw TLS passthrough for non-secret hosts,
// and selective TLS MITM credential injection for secret-bound hosts.
type Handler struct {
	cfg     Config
	allowed map[string]bool
	secrets map[string]SecretRule
	logger  *slog.Logger
}

// NewHandler validates cfg and returns the selective TLS interception proxy handler.
func NewHandler(cfg Config) (*Handler, error) {
	if cfg.CA == nil {
		return nil, errors.New("BotCA is required")
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.DialContext == nil {
		dialer := &net.Dialer{Timeout: cfg.DialTimeout}
		cfg.DialContext = dialer.DialContext
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	allowed := make(map[string]bool, len(cfg.AllowedHosts))
	for h, ok := range cfg.AllowedHosts {
		if norm := normalizeHost(h); norm != "" && ok {
			allowed[norm] = true
		}
	}

	secrets := make(map[string]SecretRule, len(cfg.Secrets))
	for h, rule := range cfg.Secrets {
		norm := normalizeHost(h)
		if norm == "" {
			return nil, errors.New("secret rule host cannot be empty")
		}
		if rule.Value == "" {
			return nil, fmt.Errorf("secret rule for %s requires a value", norm)
		}
		secrets[norm] = rule
	}

	return &Handler{
		cfg:     cfg,
		allowed: allowed,
		secrets: secrets,
		logger:  cfg.Logger,
	}, nil
}

// ServeHTTP enforces host allowlisting on HTTP CONNECT tunnels and triggers selective MITM only when required.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "CONNECT method required", http.StatusMethodNotAllowed)
		return
	}

	host, port, err := parseConnectTarget(r.Host)
	if err != nil {
		http.Error(w, "invalid CONNECT target", http.StatusBadRequest)
		return
	}
	if !h.cfg.AllowCustomPorts && port != "443" {
		h.emitAudit(AuditEvent{Host: host, Port: port, Method: r.Method, Action: ActionBlockedHost, Status: http.StatusForbidden})
		http.Error(w, "destination port forbidden", http.StatusForbidden)
		return
	}

	snap, err := h.resolveSnapshot(r.Context())
	if err != nil {
		h.logger.Warn("resolve egress policy failed", slog.String("host", host), slog.Any("error", err))
		http.Error(w, "egress policy unavailable", http.StatusBadGateway)
		return
	}
	if !h.isHostAllowed(snap, host) {
		h.emitAudit(AuditEvent{Host: host, Port: port, Method: r.Method, Action: ActionBlockedHost, Status: http.StatusForbidden})
		http.Error(w, "destination host forbidden", http.StatusForbidden)
		return
	}

	rule, needsMITM := h.firstHostRule(snap, host)
	if !needsMITM {
		h.handlePassthrough(w, r, host, port)
		return
	}
	h.handleMITM(w, r, host, port, rule)
}

func (h *Handler) handlePassthrough(w http.ResponseWriter, r *http.Request, host, port string) {
	dialCtx, cancel := context.WithTimeout(r.Context(), h.cfg.DialTimeout)
	defer cancel()

	upstreamConn, err := h.cfg.DialContext(dialCtx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		h.logger.Warn("passthrough dial failed", slog.String("host", host), slog.String("error", err.Error()))
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstreamConn.Close()
		http.Error(w, "proxy connection hijacking unsupported", http.StatusInternalServerError)
		return
	}

	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		_ = upstreamConn.Close()
		return
	}
	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		_ = clientConn.Close()
		_ = upstreamConn.Close()
		return
	}

	h.emitAudit(AuditEvent{Host: host, Port: port, Method: http.MethodConnect, Action: ActionTunnelPassthrough, Status: http.StatusOK})
	tunnelBidirectional(clientConn, clientBuf, upstreamConn)
}

func (h *Handler) handleMITM(w http.ResponseWriter, r *http.Request, host, port string, rule SecretRule) {
	leafCert, err := h.cfg.CA.LeafCertificate(host)
	if err != nil {
		http.Error(w, "failed to prepare TLS interception", http.StatusInternalServerError)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "proxy connection hijacking unsupported", http.StatusInternalServerError)
		return
	}

	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer func() { _ = clientConn.Close() }()
	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}

	bufferedConn := &prefixedConn{Conn: clientConn, r: clientBuf.Reader}
	tlsClient := tls.Server(bufferedConn, &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{*leafCert},
	})
	if err := tlsClient.HandshakeContext(r.Context()); err != nil {
		h.logger.Warn("MITM client TLS handshake failed", slog.String("host", host))
		return
	}
	defer func() { _ = tlsClient.Close() }()

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return h.cfg.DialContext(ctx, network, net.JoinHostPort(host, port))
		},
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: host,
			RootCAs:    h.cfg.UpstreamRootCAs,
		},
	}
	defer transport.CloseIdleConnections()

	innerHandler := http.HandlerFunc(func(iw http.ResponseWriter, ir *http.Request) {
		h.forwardMITMRequest(iw, ir, host, port, rule, transport)
	})

	single := newSingleConnListener(tlsClient)
	defer func() { _ = single.Close() }()
	innerSrv := &http.Server{
		Handler:           innerHandler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       5 * time.Second,
	}
	_ = innerSrv.Serve(single)
}

func (h *Handler) forwardMITMRequest(
	w http.ResponseWriter,
	r *http.Request,
	host, port string,
	rule SecretRule,
	transport *http.Transport,
) {
	outReq := r.Clone(r.Context())
	outReq.URL.Scheme = "https"
	outReq.URL.Host = net.JoinHostPort(host, port)
	if r.Host != "" {
		outReq.Host = r.Host
	} else {
		outReq.Host = host
	}
	outReq.RequestURI = ""

	cleanPath := sanitizeAuditPath(r.URL.Path)
	activeRule, shouldInject := h.resolveRuleForPath(r.Context(), host, cleanPath, rule)
	auditAction := ActionTunnelPassthrough
	var scrubRule SecretRule
	var secretID, secretScope, secretName string
	if shouldInject {
		stripPlaceholderAuthHeaders(outReq.Header)
		headerName, headerVal := formatSecretHeader(activeRule)
		outReq.Header.Set(headerName, headerVal)
		auditAction = ActionMITMTokenAdded
		scrubRule = activeRule
		secretID = activeRule.ID
		secretScope = activeRule.Scope
		secretName = activeRule.Name
	}

	start := time.Now()
	resp, err := transport.RoundTrip(outReq)
	if err != nil {
		latencyMs := time.Since(start).Milliseconds()
		msg := "upstream request failed: " + err.Error()
		errLog := err.Error()
		if scrubRule.Value != "" {
			msg = scrubSecretValues(msg, scrubRule)
			errLog = scrubSecretValues(errLog, scrubRule)
		}
		h.logger.Warn("MITM upstream failed", slog.String("host", host), slog.String("error", errLog))
		h.emitAudit(AuditEvent{
			Host:        host,
			Port:        port,
			Method:      r.Method,
			Path:        cleanPath,
			Action:      auditAction,
			Status:      http.StatusBadGateway,
			SecretID:    secretID,
			SecretScope: secretScope,
			SecretName:  secretName,
			LatencyMs:   latencyMs,
		})
		http.Error(w, msg, http.StatusBadGateway)
		return
	}

	countedBody := newCountingReadCloser(resp.Body, resp.ContentLength, func(totalBytes int64) {
		h.emitAudit(AuditEvent{
			Host:        host,
			Port:        port,
			Method:      r.Method,
			Path:        cleanPath,
			Action:      auditAction,
			Status:      resp.StatusCode,
			SecretID:    secretID,
			SecretScope: secretScope,
			SecretName:  secretName,
			Bytes:       totalBytes,
			LatencyMs:   time.Since(start).Milliseconds(),
		})
	})
	resp.Body = countedBody
	defer func() { _ = countedBody.Close() }()

	if scrubRule.Value != "" {
		copyResponseHeaders(w.Header(), resp.Header, scrubRule)
	} else {
		copyHeadersPlain(w.Header(), resp.Header)
	}

	if resp.StatusCode >= http.StatusBadRequest {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
		if readErr != nil {
			http.Error(w, "failed reading upstream error response", http.StatusBadGateway)
			return
		}
		if scrubRule.Value != "" {
			body = scrubSecretBytes(body, scrubRule)
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body) // #nosec G705 -- reverse proxy forwards scrubbed upstream error response
		return
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (h *Handler) emitAudit(ev AuditEvent) {
	if h.cfg.OnAudit != nil {
		h.cfg.OnAudit(ev)
	}
}

func parseConnectTarget(raw string) (string, string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(raw))
	if err != nil {
		return "", "", err
	}
	cleanHost := normalizeHost(host)
	if cleanHost == "" || strings.TrimSpace(port) == "" {
		return "", "", errors.New("empty host or port")
	}
	return cleanHost, port, nil
}

func stripPlaceholderAuthHeaders(h http.Header) {
	for _, key := range []string{"Authorization", "Proxy-Authorization", "X-Api-Key", "X-Goog-Api-Key", "Private-Token"} {
		h.Del(key)
	}
}

func formatSecretHeader(rule SecretRule) (string, string) {
	name := strings.TrimSpace(rule.HeaderName)
	if name == "" {
		name = "Authorization"
	}
	switch rule.Scheme {
	case SchemeBasic:
		user := rule.Username
		if user == "" {
			user = "x-access-token"
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(user + ":" + rule.Value))
		return name, "Basic " + encoded
	case SchemeRaw:
		return name, rule.Value
	case SchemeBearer, "":
		return name, "Bearer " + rule.Value
	default:
		return name, "Bearer " + rule.Value
	}
}

func copyResponseHeaders(dst, src http.Header, rule SecretRule) {
	for k, vals := range src {
		canon := textproto.CanonicalMIMEHeaderKey(k)
		if canon == "Connection" || canon == "Keep-Alive" || canon == "Proxy-Authenticate" ||
			canon == "Proxy-Authorization" || canon == "Te" || canon == "Trailers" ||
			canon == "Transfer-Encoding" || canon == "Upgrade" {
			continue
		}
		for _, v := range vals {
			dst.Add(canon, scrubSecretValues(v, rule))
		}
	}
}

func scrubSecretValues(s string, rule SecretRule) string {
	out := strings.ReplaceAll(s, rule.Value, "[REDACTED]")
	if rule.Scheme == SchemeBasic {
		_, hdr := formatSecretHeader(rule)
		b64 := strings.TrimPrefix(hdr, "Basic ")
		out = strings.ReplaceAll(out, b64, "[REDACTED]")
	}
	return out
}

func scrubSecretBytes(b []byte, rule SecretRule) []byte {
	out := bytes.ReplaceAll(b, []byte(rule.Value), []byte("[REDACTED]"))
	if rule.Scheme == SchemeBasic {
		_, hdr := formatSecretHeader(rule)
		b64 := strings.TrimPrefix(hdr, "Basic ")
		out = bytes.ReplaceAll(out, []byte(b64), []byte("[REDACTED]"))
	}
	return out
}

func tunnelBidirectional(client net.Conn, clientBuf *bufio.ReadWriter, upstream net.Conn) {
	defer func() {
		_ = client.Close()
		_ = upstream.Close()
	}()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		if clientBuf != nil && clientBuf.Reader.Buffered() > 0 {
			if _, err := io.CopyN(upstream, clientBuf.Reader, int64(clientBuf.Reader.Buffered())); err != nil {
				return
			}
		}
		_, _ = io.Copy(upstream, client)
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()

	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, upstream)
		if cw, ok := client.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()

	wg.Wait()
}

type prefixedConn struct {
	net.Conn
	r io.Reader
}

func (p *prefixedConn) Read(b []byte) (int, error) {
	return p.r.Read(b)
}

type singleConnListener struct {
	mu   sync.Mutex
	conn net.Conn
	done chan struct{}
}

func newSingleConnListener(conn net.Conn) *singleConnListener {
	l := &singleConnListener{
		done: make(chan struct{}),
	}
	l.conn = &closeNotifyConn{
		Conn:    conn,
		onClose: func() { _ = l.Close() },
	}
	return l
}

type closeNotifyConn struct {
	net.Conn
	onClose func()
}

func (c *closeNotifyConn) Close() error {
	err := c.Conn.Close()
	if c.onClose != nil {
		c.onClose()
	}
	return err
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	c := l.conn
	l.conn = nil
	l.mu.Unlock()
	if c != nil {
		return c, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *singleConnListener) Close() error {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	return nil
}

func (l *singleConnListener) Addr() net.Addr {
	return dummyAddr("mitm-single-conn")
}

type dummyAddr string

func (d dummyAddr) Network() string { return "tcp" }
func (d dummyAddr) String() string  { return string(d) }
