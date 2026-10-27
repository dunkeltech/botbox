package mitmproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

var (
	// ErrPolicyUnavailable indicates dynamic egress policy lookup failed; requests fail closed.
	ErrPolicyUnavailable = errors.New("egress policy unavailable")
	// ErrHostForbidden indicates the destination host is not currently permitted by egress policy.
	ErrHostForbidden = errors.New("destination host forbidden")
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
	Revision     int64
	AllowedHosts []string
	BlockedHosts []string
	Rules        []SecretRule
}

func clonePolicySnapshot(snap PolicySnapshot) PolicySnapshot {
	out := PolicySnapshot{
		Policy:   snap.Policy,
		Revision: snap.Revision,
	}
	if len(snap.AllowedHosts) > 0 {
		out.AllowedHosts = append([]string(nil), snap.AllowedHosts...)
	}
	if len(snap.BlockedHosts) > 0 {
		out.BlockedHosts = append([]string(nil), snap.BlockedHosts...)
	}
	if len(snap.Rules) > 0 {
		out.Rules = append([]SecretRule(nil), snap.Rules...)
	}
	return out
}

func (h *Handler) staticSnapshot() PolicySnapshot {
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

	blocked := make([]string, 0, len(h.blocked)+len(h.cfg.BlockedPatterns))
	for host, ok := range h.blocked {
		if ok {
			blocked = append(blocked, host)
		}
	}
	blocked = append(blocked, h.cfg.BlockedPatterns...)

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
		BlockedHosts: blocked,
		Rules:        rules,
	}
}

func (h *Handler) resolveSnapshot(ctx context.Context) (PolicySnapshot, error) {
	if h.cfg.DynamicPolicy != nil {
		snap, err := h.cfg.DynamicPolicy(ctx)
		if err != nil {
			h.failClosedOnRefreshError()
			return PolicySnapshot{}, err
		}
		if snap.Policy == "" {
			snap.Policy = PolicyAllowlist
		}
		adopted, err := h.adoptSnapshot(ctx, snap, false)
		if err != nil {
			return PolicySnapshot{}, err
		}
		if !adopted {
			return PolicySnapshot{}, fmt.Errorf("%w: stale policy revision %d", ErrPolicyUnavailable, snap.Revision)
		}
		return snap, nil
	}

	h.passthroughMu.Lock()
	if h.hasAdopted {
		snap := clonePolicySnapshot(h.adoptedSnap)
		h.passthroughMu.Unlock()
		return snap, nil
	}
	h.passthroughMu.Unlock()

	return h.staticSnapshot(), nil
}

// NormalizeHostPattern normalizes a hostname or wildcard pattern for comparison.
func NormalizeHostPattern(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}

func normalizeHostPattern(host string) string {
	return NormalizeHostPattern(host)
}

// HostMatchesPattern checks whether host matches pattern. Wildcard patterns
// ("*.example.com") match subdomains (including nested subdomains) but never
// the apex domain ("example.com").
func HostMatchesPattern(host, pattern string, matchSubdomains bool) bool {
	host = NormalizeHostPattern(host)
	pattern = NormalizeHostPattern(pattern)
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

func hostMatchesPattern(host, pattern string, matchSubdomains bool) bool {
	return HostMatchesPattern(host, pattern, matchSubdomains)
}

// MatchingHostPattern returns the most specific matching pattern in patterns for host.
// Exact matches take precedence over wildcards; longer wildcards take precedence over shorter ones.
func MatchingHostPattern(patterns []string, host string) (string, bool) {
	normHost := NormalizeHostPattern(host)
	if normHost == "" {
		return "", false
	}
	best := ""
	bestRank := -1
	for _, raw := range patterns {
		pattern := NormalizeHostPattern(raw)
		if pattern == "" {
			continue
		}
		if !HostMatchesPattern(normHost, pattern, false) {
			continue
		}
		rank := len(pattern)
		if !strings.HasPrefix(pattern, "*.") {
			rank += 10000
		}
		if rank > bestRank {
			best = pattern
			bestRank = rank
		}
	}
	return best, bestRank >= 0
}

// EvaluateHostAccess applies the bot.Box egress precedence contract:
// explicit blocks override open mode, explicit allows, and secret-derived hosts.
func EvaluateHostAccess(policy EgressPolicy, allowedPatterns, blockedPatterns []string, host string) (bool, string) {
	normHost := NormalizeHostPattern(host)
	if normHost == "" {
		return false, ""
	}
	if matchedBlock, blocked := MatchingHostPattern(blockedPatterns, normHost); blocked {
		return false, matchedBlock
	}
	if policy == PolicyOpen {
		return true, ""
	}
	if _, allowed := MatchingHostPattern(allowedPatterns, normHost); allowed {
		return true, ""
	}
	return false, ""
}

func (h *Handler) isHostAllowed(snap PolicySnapshot, host string) bool {
	allowed, _ := EvaluateHostAccess(snap.Policy, snap.AllowedHosts, snap.BlockedHosts, host)
	return allowed
}

func (h *Handler) ruleActive(r SecretRule) bool {
	if r.Value == "" || r.Revoked {
		return false
	}
	if !r.ExpiresAt.IsZero() && !h.now().Before(r.ExpiresAt) {
		return false
	}
	return true
}

func (h *Handler) firstHostRule(snap PolicySnapshot, host string) (SecretRule, bool) {
	var best SecretRule
	bestScore := -1
	for _, r := range snap.Rules {
		if !h.ruleActive(r) {
			continue
		}
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

func (h *Handler) resolveRuleForRequest(
	ctx context.Context,
	host, port, method, cleanPath string,
	staticFallback SecretRule,
) (SecretRule, bool, error) {
	snap, err := h.resolveSnapshot(ctx)
	if err != nil {
		// Dynamic policy failures must fail closed and never fall back to a cached credential (ADR-0016).
		return SecretRule{}, false, fmt.Errorf("%w: %v", ErrPolicyUnavailable, err)
	}
	if !h.isHostAllowed(snap, host) {
		return SecretRule{}, false, ErrHostForbidden
	}

	if h.cfg.DynamicPolicy != nil {
		if len(snap.Rules) == 0 {
			return SecretRule{}, false, nil
		}
		matched, ok := h.matchRuleForRequest(snap.Rules, host, port, method, cleanPath)
		return matched, ok, nil
	}

	if len(snap.Rules) > 0 {
		matched, ok := h.matchRuleForRequest(snap.Rules, host, port, method, cleanPath)
		return matched, ok, nil
	}

	if !h.ruleActive(staticFallback) {
		return SecretRule{}, false, nil
	}
	domain := staticFallback.DomainMatch
	if domain == "" {
		domain = host
	}
	if !hostMatchesPattern(host, domain, staticFallback.MatchSubdomains) {
		return SecretRule{}, false, nil
	}
	if !pathMatchesPrefix(cleanPath, staticFallback.PathPrefix) {
		return SecretRule{}, false, nil
	}
	if staticFallback.AdapterID != "" {
		adapter, ok := LookupAdapter(staticFallback.AdapterID)
		if !ok || !adapter.MatchesRequest(host, port, method, cleanPath) {
			return SecretRule{}, false, nil
		}
	}
	return staticFallback, true, nil
}

func (h *Handler) matchRuleForRequest(rules []SecretRule, host, port, method, cleanPath string) (SecretRule, bool) {
	var best SecretRule
	bestScore := -1
	for _, r := range rules {
		if r.DomainMatch == "" || !h.ruleActive(r) {
			continue
		}
		if !hostMatchesPattern(host, r.DomainMatch, r.MatchSubdomains) {
			continue
		}
		if !pathMatchesPrefix(cleanPath, r.PathPrefix) {
			continue
		}
		if r.Port != "" && strings.TrimSpace(port) != strings.TrimSpace(r.Port) {
			continue
		}
		if r.AdapterID != "" {
			adapter, ok := LookupAdapter(r.AdapterID)
			if !ok || !adapter.MatchesRequest(host, port, method, cleanPath) {
				continue
			}
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
	switch r.Scope {
	case "lease":
		score += 200000
	case "bot":
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
