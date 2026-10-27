package mitmproxy

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
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
	ID               string
	Scope            string
	Name             string
	AdapterID        string
	DomainMatch      string
	Port             string
	MatchSubdomains  bool
	PathPrefix       string
	HeaderName       string
	Scheme           AuthScheme
	Username         string
	Value            string
	PlaceholderToken string
	ExpiresAt        time.Time
	Revoked          bool
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
	CA                 *BotCA
	Policy             EgressPolicy
	AllowedHosts       map[string]bool
	AllowedPatterns    []string
	BlockedHosts       map[string]bool
	BlockedPatterns    []string
	Secrets            map[string]SecretRule
	Rules              []SecretRule
	DynamicPolicy      func(context.Context) (PolicySnapshot, error)
	OnPolicyAdopted    func(context.Context, int64) error
	PolicyFreshnessMax time.Duration
	UpstreamRootCAs    *x509.CertPool
	DialContext        func(ctx context.Context, network, addr string) (net.Conn, error)
	DialTimeout        time.Duration
	Logger             *slog.Logger
	OnAudit            func(AuditEvent)
	AllowCustomPorts   bool
	Now                func() time.Time
}

type pendingDial struct {
	host     string
	revision int64
	cancel   context.CancelFunc
	aborted  atomic.Bool
}

// Handler implements HTTP CONNECT with allowlist/blocklist filtering, raw TLS passthrough
// for non-secret hosts, and selective TLS MITM credential injection with response mediation.
type Handler struct {
	cfg     Config
	allowed map[string]bool
	blocked map[string]bool
	secrets map[string]SecretRule
	logger  *slog.Logger

	passthroughMu    sync.Mutex
	passthroughConns map[string]map[net.Conn]struct{}
	pendingDials     map[*pendingDial]struct{}
	hasAdopted       bool
	adoptedSnap      PolicySnapshot
	adoptedRevision  int64
	lastFreshAt      time.Time
	policyStale      bool

	ackMu             sync.Mutex
	hasAcked          bool
	lastAckedRevision int64
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

	blocked := make(map[string]bool, len(cfg.BlockedHosts))
	for h, ok := range cfg.BlockedHosts {
		if norm := normalizeHost(h); norm != "" && ok {
			blocked[norm] = true
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
		cfg:              cfg,
		allowed:          allowed,
		blocked:          blocked,
		secrets:          secrets,
		logger:           cfg.Logger,
		passthroughConns: make(map[string]map[net.Conn]struct{}),
		pendingDials:     make(map[*pendingDial]struct{}),
	}, nil
}

func (h *Handler) now() time.Time {
	if h.cfg.Now != nil {
		return h.cfg.Now()
	}
	return time.Now()
}

// AdoptedRevision returns the highest policy revision adopted by this handler.
func (h *Handler) AdoptedRevision() int64 {
	h.passthroughMu.Lock()
	defer h.passthroughMu.Unlock()
	return h.adoptedRevision
}

// InvalidateHost closes active raw passthrough tunnels and aborts pending dials matching
// host or wildcard pattern so subsequent connections re-evaluate current policy.
func (h *Handler) InvalidateHost(host string) {
	norm := normalizeHostPattern(host)
	if norm == "" {
		return
	}
	var conns []net.Conn
	var cancels []context.CancelFunc

	h.passthroughMu.Lock()
	for connHost, set := range h.passthroughConns {
		if HostMatchesPattern(connHost, norm, false) {
			for c := range set {
				conns = append(conns, c)
			}
			delete(h.passthroughConns, connHost)
		}
	}
	for pd := range h.pendingDials {
		if HostMatchesPattern(pd.host, norm, false) {
			pd.aborted.Store(true)
			cancels = append(cancels, pd.cancel)
		}
	}
	h.passthroughMu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	for _, c := range conns {
		_ = c.Close()
	}
}

func (h *Handler) failClosedOnRefreshError() {
	var conns []net.Conn
	var cancels []context.CancelFunc

	h.passthroughMu.Lock()
	h.policyStale = true
	for host, set := range h.passthroughConns {
		for c := range set {
			conns = append(conns, c)
		}
		delete(h.passthroughConns, host)
	}
	for pd := range h.pendingDials {
		pd.aborted.Store(true)
		cancels = append(cancels, pd.cancel)
	}
	h.passthroughMu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	for _, c := range conns {
		_ = c.Close()
	}
}

// CheckFreshness verifies that the last authoritative policy refresh is within
// PolicyFreshnessMax. When stale, all active passthrough tunnels are closed and
// pending dials aborted so the proxy fails closed.
func (h *Handler) CheckFreshness() bool {
	if h.cfg.PolicyFreshnessMax <= 0 {
		return true
	}
	var conns []net.Conn
	var cancels []context.CancelFunc

	h.passthroughMu.Lock()
	if !h.hasAdopted {
		h.passthroughMu.Unlock()
		return true
	}
	if !h.policyStale && h.now().Sub(h.lastFreshAt) <= h.cfg.PolicyFreshnessMax {
		h.passthroughMu.Unlock()
		return true
	}
	h.policyStale = true
	for host, set := range h.passthroughConns {
		for c := range set {
			conns = append(conns, c)
		}
		delete(h.passthroughConns, host)
	}
	for pd := range h.pendingDials {
		pd.aborted.Store(true)
		cancels = append(cancels, pd.cancel)
	}
	h.passthroughMu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	for _, c := range conns {
		_ = c.Close()
	}
	return false
}

// AdoptSnapshot atomically adopts snap if its revision is not older than the
// currently adopted revision, closes any active passthrough tunnels (and aborts
// in-flight dials) that are now forbidden or require secret mediation, and then
// notifies OnPolicyAdopted after tunnel closure completes.
func (h *Handler) AdoptSnapshot(ctx context.Context, snap PolicySnapshot) (bool, error) {
	return h.adoptSnapshot(ctx, snap, true)
}

func (h *Handler) adoptSnapshot(ctx context.Context, snap PolicySnapshot, notify bool) (bool, error) {
	if snap.Policy == "" {
		snap.Policy = PolicyAllowlist
	}
	var conns []net.Conn
	var cancels []context.CancelFunc

	h.passthroughMu.Lock()
	if h.hasAdopted && snap.Revision < h.adoptedRevision {
		h.passthroughMu.Unlock()
		return false, nil
	}
	h.adoptedSnap = clonePolicySnapshot(snap)
	h.adoptedRevision = snap.Revision
	h.hasAdopted = true
	h.lastFreshAt = h.now()
	h.policyStale = false

	for host, set := range h.passthroughConns {
		_, needsMITM := h.firstHostRule(snap, host)
		if !h.isHostAllowed(snap, host) || needsMITM {
			for c := range set {
				conns = append(conns, c)
			}
			delete(h.passthroughConns, host)
		}
	}
	for pd := range h.pendingDials {
		_, needsMITM := h.firstHostRule(snap, pd.host)
		if !h.isHostAllowed(snap, pd.host) || needsMITM {
			pd.aborted.Store(true)
			cancels = append(cancels, pd.cancel)
		}
	}
	h.passthroughMu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	for _, c := range conns {
		_ = c.Close()
	}

	if notify && h.cfg.OnPolicyAdopted != nil {
		h.ackMu.Lock()
		needsAck := !h.hasAcked || snap.Revision > h.lastAckedRevision
		if needsAck {
			if err := h.cfg.OnPolicyAdopted(ctx, snap.Revision); err != nil {
				h.ackMu.Unlock()
				return true, err
			}
			h.hasAcked = true
			h.lastAckedRevision = snap.Revision
		}
		h.ackMu.Unlock()
	}
	return true, nil
}

// AcknowledgedRevision returns the highest policy revision acknowledged after tunnel closure.
func (h *Handler) AcknowledgedRevision() int64 {
	h.ackMu.Lock()
	defer h.ackMu.Unlock()
	return h.lastAckedRevision
}

// RevalidatePassthrough reloads the authoritative policy snapshot (when DynamicPolicy
// is configured), adopts it, closes any affected passthrough tunnels, and fails
// closed on refresh errors.
func (h *Handler) RevalidatePassthrough(ctx context.Context) error {
	if h.cfg.DynamicPolicy != nil {
		snap, err := h.cfg.DynamicPolicy(ctx)
		if err != nil {
			h.failClosedOnRefreshError()
			return err
		}
		_, err = h.adoptSnapshot(ctx, snap, true)
		return err
	}

	h.passthroughMu.Lock()
	hasAdopted := h.hasAdopted
	snap := h.adoptedSnap
	h.passthroughMu.Unlock()
	if !hasAdopted {
		snap = h.staticSnapshot()
	}
	_, err := h.adoptSnapshot(ctx, snap, false)
	return err
}

// StartRevalidation periodically refreshes and revalidates active passthrough tunnels
// until stop is called or ctx is canceled.
func (h *Handler) StartRevalidation(ctx context.Context, interval time.Duration) func() {
	if interval <= 0 {
		return func() {}
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if err := h.RevalidatePassthrough(runCtx); err != nil {
					h.CheckFreshness()
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func (h *Handler) unregisterPassthrough(host string, c net.Conn) {
	norm := normalizeHostPattern(host)
	if norm == "" || c == nil {
		return
	}
	h.passthroughMu.Lock()
	defer h.passthroughMu.Unlock()
	if set := h.passthroughConns[norm]; set != nil {
		delete(set, c)
		if len(set) == 0 {
			delete(h.passthroughConns, norm)
		}
	}
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
		h.handlePassthrough(w, r, host, port, snap.Revision)
		return
	}
	h.handleMITM(w, r, host, port, rule)
}

func (h *Handler) handlePassthrough(w http.ResponseWriter, r *http.Request, host, port string, checkedRevision int64) {
	norm := normalizeHostPattern(host)
	dialCtx, cancel := context.WithTimeout(r.Context(), h.cfg.DialTimeout)
	defer cancel()

	pd := &pendingDial{
		host:     norm,
		revision: checkedRevision,
		cancel:   cancel,
	}

	h.passthroughMu.Lock()
	if h.policyStale || (h.hasAdopted && !h.isHostAllowed(h.adoptedSnap, host)) {
		stale := h.policyStale
		h.passthroughMu.Unlock()
		if stale {
			http.Error(w, "egress policy unavailable", http.StatusBadGateway)
			return
		}
		h.emitAudit(AuditEvent{Host: host, Port: port, Method: r.Method, Action: ActionBlockedHost, Status: http.StatusForbidden})
		http.Error(w, "destination host forbidden", http.StatusForbidden)
		return
	}
	h.pendingDials[pd] = struct{}{}
	h.passthroughMu.Unlock()

	upstreamConn, err := h.cfg.DialContext(dialCtx, "tcp", net.JoinHostPort(host, port))

	h.passthroughMu.Lock()
	delete(h.pendingDials, pd)
	aborted := pd.aborted.Load()
	stale := h.policyStale
	denied := false
	nowMITM := false
	if h.hasAdopted {
		if !h.isHostAllowed(h.adoptedSnap, host) {
			denied = true
		} else if _, needsMITM := h.firstHostRule(h.adoptedSnap, host); needsMITM {
			nowMITM = true
		}
	}
	h.passthroughMu.Unlock()

	if aborted || denied || nowMITM || stale {
		if upstreamConn != nil {
			_ = upstreamConn.Close()
		}
		if denied || aborted {
			h.emitAudit(AuditEvent{Host: host, Port: port, Method: r.Method, Action: ActionBlockedHost, Status: http.StatusForbidden})
			http.Error(w, "destination host forbidden", http.StatusForbidden)
			return
		}
		http.Error(w, "egress policy unavailable", http.StatusBadGateway)
		return
	}
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

	h.passthroughMu.Lock()
	stillValid := !h.policyStale
	if stillValid && h.hasAdopted {
		if !h.isHostAllowed(h.adoptedSnap, host) {
			stillValid = false
		} else if _, needsMITM := h.firstHostRule(h.adoptedSnap, host); needsMITM {
			stillValid = false
		}
	}
	if stillValid {
		set := h.passthroughConns[norm]
		if set == nil {
			set = make(map[net.Conn]struct{})
			h.passthroughConns[norm] = set
		}
		set[clientConn] = struct{}{}
	}
	h.passthroughMu.Unlock()

	if !stillValid {
		_ = clientConn.Close()
		_ = upstreamConn.Close()
		return
	}
	defer h.unregisterPassthrough(host, clientConn)

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
	activeRule, shouldInject, policyErr := h.resolveRuleForRequest(r.Context(), host, port, r.Method, cleanPath, rule)
	if policyErr != nil {
		if errors.Is(policyErr, ErrHostForbidden) {
			h.emitAudit(AuditEvent{Host: host, Port: port, Method: r.Method, Path: cleanPath, Action: ActionBlockedHost, Status: http.StatusForbidden})
			http.Error(w, "destination host forbidden", http.StatusForbidden)
			return
		}
		h.logger.Warn("MITM policy evaluation failed", slog.String("host", host), slog.Any("error", policyErr))
		http.Error(w, "egress policy unavailable", http.StatusServiceUnavailable)
		return
	}

	auditAction := ActionTunnelPassthrough
	var scrubRule SecretRule
	var secretID, secretScope, secretName string
	var activeAdapter *HTTPSAdapter
	if shouldInject {
		stripPlaceholderAuthHeaders(outReq.Header)
		headerName, headerVal := formatSecretHeader(activeRule)
		outReq.Header.Set(headerName, headerVal)
		auditAction = ActionMITMTokenAdded
		scrubRule = activeRule
		secretID = activeRule.ID
		secretScope = activeRule.Scope
		secretName = activeRule.Name
		if activeRule.AdapterID != "" {
			if ad, ok := LookupAdapter(activeRule.AdapterID); ok {
				activeAdapter = &ad
			}
		}
	} else {
		stripBotboxPlaceholders(outReq.Header)
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
		mediateAndWriteResponse(w, resp, scrubRule, activeAdapter)
		return
	}

	copyHeadersPlain(w.Header(), resp.Header)
	if resp.StatusCode >= http.StatusBadRequest {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
		if readErr != nil {
			http.Error(w, "failed reading upstream error response", http.StatusBadGateway)
			return
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body) // #nosec G705 -- reverse proxy forwards upstream error response
		return
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func mediateAndWriteResponse(w http.ResponseWriter, resp *http.Response, rule SecretRule, adapter *HTTPSAdapter) {
	maxBytes := defaultMaxMediatedResponseBytes
	if adapter != nil && adapter.MaxResponseBytes > 0 {
		maxBytes = adapter.MaxResponseBytes
	}

	rawBody, err := readBounded(resp.Body, maxBytes)
	if err != nil {
		http.Error(w, "upstream response rejected by mediation policy", http.StatusBadGateway)
		return
	}

	encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	var decoded []byte
	decodedCompressed := false
	switch encoding {
	case "", "identity":
		decoded = rawBody
	case "gzip":
		gr, gerr := gzip.NewReader(bytes.NewReader(rawBody))
		if gerr != nil {
			http.Error(w, "malformed compressed upstream response", http.StatusBadGateway)
			return
		}
		decoded, err = readBounded(gr, maxBytes)
		_ = gr.Close()
		if err != nil {
			http.Error(w, "upstream compressed response rejected", http.StatusBadGateway)
			return
		}
		decodedCompressed = true
	case "deflate":
		decoded, err = decompressDeflateBounded(rawBody, maxBytes)
		if err != nil {
			http.Error(w, "upstream compressed response rejected", http.StatusBadGateway)
			return
		}
		decodedCompressed = true
	default:
		http.Error(w, "unsupported response encoding for credentialed request", http.StatusBadGateway)
		return
	}

	// Never leak secrets through redirect Location headers.
	if loc := resp.Header.Get("Location"); loc != "" && containsAnySecretForm([]byte(loc), rule) {
		http.Error(w, "upstream redirect blocked by credential mediation", http.StatusBadGateway)
		return
	}

	contentType := resp.Header.Get("Content-Type")
	var mediatedBody []byte
	switch classifyMediatedBody(contentType, decoded, adapter) {
	case bodyClassText:
		mediatedBody = scrubSecretBytes(decoded, rule)
	case bodyClassAllowedBinary:
		if containsAnySecretForm(decoded, rule) {
			http.Error(w, "upstream binary response blocked by credential mediation", http.StatusBadGateway)
			return
		}
		mediatedBody = decoded
	default:
		http.Error(w, "unsupported binary response for credentialed request", http.StatusBadGateway)
		return
	}

	copyResponseHeaders(w.Header(), resp.Header, rule)
	if decodedCompressed || !bytes.Equal(mediatedBody, rawBody) {
		w.Header().Del("Content-Encoding")
		w.Header().Del("Content-Length")
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(mediatedBody) // #nosec G705 -- reverse proxy writes secret-scrubbed response body
}

type mediatedBodyClass int

const (
	bodyClassReject mediatedBodyClass = iota
	bodyClassText
	bodyClassAllowedBinary
)

func classifyMediatedBody(contentType string, body []byte, adapter *HTTPSAdapter) mediatedBodyClass {
	if len(body) == 0 {
		return bodyClassText
	}
	mediaType := ""
	if strings.TrimSpace(contentType) != "" {
		parsed, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			return bodyClassReject
		}
		mediaType = strings.ToLower(parsed)
	}

	if adapter != nil && adapter.AllowBinaryPack {
		if mediaType == "application/x-git-upload-pack-result" ||
			mediaType == "application/x-git-receive-pack-result" ||
			mediaType == "application/x-git-upload-pack-advertisement" ||
			mediaType == "application/x-git-receive-pack-advertisement" {
			return bodyClassAllowedBinary
		}
	}

	if mediaType == "" {
		if !bytes.ContainsRune(body, 0) && utf8.Valid(body) {
			return bodyClassText
		}
		return bodyClassReject
	}
	if strings.HasPrefix(mediaType, "text/") ||
		mediaType == "application/json" ||
		strings.HasSuffix(mediaType, "+json") ||
		mediaType == "application/xml" ||
		strings.HasSuffix(mediaType, "+xml") ||
		mediaType == "application/javascript" ||
		mediaType == "application/x-www-form-urlencoded" {
		if bytes.ContainsRune(body, 0) {
			return bodyClassReject
		}
		return bodyClassText
	}
	return bodyClassReject
}

func readBounded(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = defaultMaxMediatedResponseBytes
	}
	buf, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > maxBytes {
		return nil, errors.New("response exceeds size limit")
	}
	return buf, nil
}

func decompressDeflateBounded(raw []byte, maxBytes int64) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(raw))
	if err == nil {
		out, readErr := readBounded(zr, maxBytes)
		_ = zr.Close()
		return out, readErr
	}
	fr := flate.NewReader(bytes.NewReader(raw))
	defer func() { _ = fr.Close() }()
	return readBounded(fr, maxBytes)
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

func stripBotboxPlaceholders(h http.Header) {
	for _, key := range []string{"Authorization", "Proxy-Authorization", "X-Api-Key", "X-Goog-Api-Key", "Private-Token"} {
		for _, v := range h.Values(key) {
			if strings.Contains(v, "botbox-placeholder-") {
				h.Del(key)
				break
			}
		}
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

func secretRepresentations(rule SecretRule) []string {
	if rule.Value == "" {
		return nil
	}
	seen := make(map[string]struct{})
	var forms []string
	add := func(s string) {
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		forms = append(forms, s)
	}

	_, hdr := formatSecretHeader(rule)
	add(hdr)
	if rule.Scheme == SchemeBasic {
		b64 := strings.TrimPrefix(hdr, "Basic ")
		add(b64)
		add(url.QueryEscape(b64))
		user := rule.Username
		if user == "" {
			user = "x-access-token"
		}
		add(user + ":" + rule.Value)
		add(base64.RawStdEncoding.EncodeToString([]byte(user + ":" + rule.Value)))
		add(base64.URLEncoding.EncodeToString([]byte(user + ":" + rule.Value)))
		add(base64.RawURLEncoding.EncodeToString([]byte(user + ":" + rule.Value)))
	}
	add(rule.Value)
	add(url.QueryEscape(rule.Value))
	add(url.PathEscape(rule.Value))
	add(base64.StdEncoding.EncodeToString([]byte(rule.Value)))
	add(base64.RawStdEncoding.EncodeToString([]byte(rule.Value)))
	add(base64.URLEncoding.EncodeToString([]byte(rule.Value)))
	add(base64.RawURLEncoding.EncodeToString([]byte(rule.Value)))
	return forms
}

func containsAnySecretForm(b []byte, rule SecretRule) bool {
	for _, form := range secretRepresentations(rule) {
		if bytes.Contains(b, []byte(form)) {
			return true
		}
	}
	return false
}

func scrubSecretValues(s string, rule SecretRule) string {
	out := s
	for _, form := range secretRepresentations(rule) {
		out = strings.ReplaceAll(out, form, "[REDACTED]")
	}
	return out
}

func scrubSecretBytes(b []byte, rule SecretRule) []byte {
	out := b
	for _, form := range secretRepresentations(rule) {
		out = bytes.ReplaceAll(out, []byte(form), []byte("[REDACTED]"))
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
