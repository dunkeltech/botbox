package mitmproxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
)

// EgressPolicy controls whether outbound CONNECT destinations are restricted to an allowlist
// or unrestricted (still selectively MITM-ing destinations that have bound secret rules).
type EgressPolicy string

// Outbound egress policy modes supported by the CONNECT proxy.
const (
	PolicyAllowlist EgressPolicy = "allowlist"
	PolicyOpen      EgressPolicy = "open"
)

// PolicySnapshot represents the effective per-bot egress policy and secret injection rules
// evaluated at connection or request time.
type PolicySnapshot struct {
	Policy       EgressPolicy
	AllowedHosts []string
	Rules        []SecretRule
}

func (h *Handler) resolveSnapshot(ctx context.Context) (PolicySnapshot, error) {
	if h.cfg.DynamicPolicy != nil {
		snap, err := h.cfg.DynamicPolicy(ctx)
		if err != nil {
			return PolicySnapshot{}, err
		}
		if snap.Policy == "" {
			snap.Policy = PolicyAllowlist
		}
		return snap, nil
	}

	policy := h.cfg.Policy
	if policy == "" {
		policy = PolicyAllowlist
	}

	allowed := make([]string, 0, len(h.allowed)+len(h.cfg.AllowedPatterns))
	for host, ok := range h.allowed {
		if ok {
			allowed = append(allowed, host)
		}
	}
	allowed = append(allowed, h.cfg.AllowedPatterns...)

	rules := make([]SecretRule, 0, len(h.cfg.Rules)+len(h.secrets))
	rules = append(rules, h.cfg.Rules...)
	for host, r := range h.secrets {
		if r.DomainMatch == "" {
			r.DomainMatch = host
		}
		rules = append(rules, r)
	}

	return PolicySnapshot{
		Policy:       policy,
		AllowedHosts: allowed,
		Rules:        rules,
	}, nil
}

func normalizeHostPattern(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}

func hostMatchesPattern(host, pattern string, matchSubdomains bool) bool {
	host = normalizeHostPattern(host)
	pattern = normalizeHostPattern(pattern)
	if host == "" || pattern == "" {
		return false
	}

	if strings.HasPrefix(pattern, "*.") {
		base := strings.TrimPrefix(pattern, "*.")
		if base == "" {
			return false
		}
		return strings.HasSuffix(host, "."+base)
	}

	if host == pattern {
		return true
	}
	if matchSubdomains {
		return strings.HasSuffix(host, "."+pattern)
	}
	return false
}

func (h *Handler) isHostAllowed(snap PolicySnapshot, host string) bool {
	if snap.Policy == PolicyOpen {
		return true
	}
	host = normalizeHostPattern(host)
	if host == "" {
		return false
	}
	for _, pattern := range snap.AllowedHosts {
		if hostMatchesPattern(host, pattern, false) {
			return true
		}
	}
	return false
}

func (h *Handler) firstHostRule(snap PolicySnapshot, host string) (SecretRule, bool) {
	var best SecretRule
	bestScore := -1
	for _, r := range snap.Rules {
		domain := r.DomainMatch
		if domain == "" {
			continue
		}
		if !hostMatchesPattern(host, domain, r.MatchSubdomains) {
			continue
		}
		score := ruleHostScore(host, r)
		if score > bestScore {
			best = r
			bestScore = score
		}
	}
	return best, bestScore >= 0
}

func (h *Handler) resolveRuleForPath(ctx context.Context, host, cleanPath string, fallback SecretRule) (SecretRule, bool) {
	snap, err := h.resolveSnapshot(ctx)
	if err == nil && len(snap.Rules) > 0 {
		if matched, ok := matchRuleForRequest(snap.Rules, host, cleanPath); ok {
			return matched, true
		}
		// Dynamic or multi-rule list is active; if none matched this path, pass through without injection.
		return SecretRule{}, false
	}

	domain := fallback.DomainMatch
	if domain == "" {
		domain = host
	}
	if fallback.Value == "" || !hostMatchesPattern(host, domain, fallback.MatchSubdomains) {
		return SecretRule{}, false
	}
	if !pathMatchesPrefix(cleanPath, fallback.PathPrefix) {
		return SecretRule{}, false
	}
	return fallback, true
}

func matchRuleForRequest(rules []SecretRule, host, cleanPath string) (SecretRule, bool) {
	var best SecretRule
	bestScore := -1
	for _, r := range rules {
		if r.DomainMatch == "" || r.Value == "" {
			continue
		}
		if !hostMatchesPattern(host, r.DomainMatch, r.MatchSubdomains) {
			continue
		}
		if !pathMatchesPrefix(cleanPath, r.PathPrefix) {
			continue
		}
		score := ruleFullScore(host, r)
		if score > bestScore {
			best = r
			bestScore = score
		}
	}
	return best, bestScore >= 0
}

func ruleHostScore(host string, r SecretRule) int {
	score := 0
	if r.Scope == "bot" {
		score += 100000
	}
	if normalizeHostPattern(host) == normalizeHostPattern(r.DomainMatch) {
		score += 10000
	}
	return score + len(normalizePathPrefix(r.PathPrefix))
}

func ruleFullScore(host string, r SecretRule) int {
	return ruleHostScore(host, r)
}

func normalizePathPrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || prefix == "/" {
		return ""
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return prefix
}

func pathMatchesPrefix(cleanPath, prefix string) bool {
	normPrefix := normalizePathPrefix(prefix)
	if normPrefix == "" {
		return true
	}
	if cleanPath == "" {
		cleanPath = "/"
	}
	trimmed := strings.TrimSuffix(normPrefix, "/")
	if cleanPath == trimmed {
		return true
	}
	return strings.HasPrefix(cleanPath, trimmed+"/")
}

// sanitizeAuditPath strips any query string or fragment so query parameters carrying
// accidental sensitive inputs are never written to egress logs.
func sanitizeAuditPath(rawPath string) string {
	beforeQuery, _, _ := strings.Cut(rawPath, "?")
	beforeFrag, _, _ := strings.Cut(beforeQuery, "#")
	return beforeFrag
}

func copyHeadersPlain(dst, src http.Header) {
	for k, vals := range src {
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

type countingReadCloser struct {
	rc            io.ReadCloser
	contentLength int64
	readBytes     int64
	once          sync.Once
	onDone        func(totalBytes int64)
}

func newCountingReadCloser(rc io.ReadCloser, contentLength int64, onDone func(int64)) *countingReadCloser {
	return &countingReadCloser{
		rc:            rc,
		contentLength: contentLength,
		onDone:        onDone,
	}
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if n > 0 {
		c.readBytes += int64(n)
	}
	if err == io.EOF {
		c.finish()
	}
	return n, err
}

func (c *countingReadCloser) Close() error {
	err := c.rc.Close()
	c.finish()
	return err
}

func (c *countingReadCloser) finish() {
	c.once.Do(func() {
		total := c.readBytes
		if total == 0 && c.contentLength > 0 {
			total = c.contentLength
		}
		if c.onDone != nil {
			c.onDone(total)
		}
	})
}
