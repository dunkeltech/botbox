package mitmproxy

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
)

const defaultMaxMediatedResponseBytes int64 = 2 << 20 // 2 MiB

// Git packfiles routinely exceed API payloads. Keep buffering bounded within
// the proxy's memory budget while preserving full-response inspection (ADR-0023).
const maxGitMediatedResponseBytes int64 = 64 << 20 // 64 MiB

// HTTPSAdapter defines an explicitly reviewed upstream HTTPS integration that may
// receive egress-proxy injected credentials from an in-chat secret request.
// Host allowlisting alone never grants credential injection rights (ADR-0016).
type HTTPSAdapter struct {
	ID               string
	Label            string
	Host             string
	Port             string
	AllowedMethods   []string
	AllowedPaths     []string
	AllowedSuffixes  []string
	HeaderName       string
	Scheme           AuthScheme
	FixedUsername    string
	DefaultEnvName   string
	AllowRedirects   bool
	AllowBinaryPack  bool
	MaxResponseBytes int64
}

var reviewedHTTPSAdapters = map[string]HTTPSAdapter{
	"github_api": {
		ID:               "github_api",
		Label:            "GitHub REST / GraphQL API",
		Host:             "api.github.com",
		Port:             "443",
		AllowedMethods:   []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"},
		AllowedPaths:     []string{"/user", "/users/", "/repos/", "/orgs/", "/gists", "/graphql", "/search/"},
		HeaderName:       "Authorization",
		Scheme:           SchemeBearer,
		DefaultEnvName:   "GH_TOKEN",
		AllowRedirects:   false,
		MaxResponseBytes: defaultMaxMediatedResponseBytes,
	},
	"gitlab_api": {
		ID:               "gitlab_api",
		Label:            "GitLab REST API",
		Host:             "gitlab.com",
		Port:             "443",
		AllowedMethods:   []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"},
		AllowedPaths:     []string{"/api/v4/"},
		HeaderName:       "Authorization",
		Scheme:           SchemeBearer,
		DefaultEnvName:   "GITLAB_TOKEN",
		AllowRedirects:   false,
		MaxResponseBytes: defaultMaxMediatedResponseBytes,
	},
	"gitlab_git": {
		ID:               "gitlab_git",
		Label:            "GitLab Git over HTTPS",
		Host:             "gitlab.com",
		Port:             "443",
		AllowedMethods:   []string{"GET", "POST"},
		AllowedSuffixes:  []string{".git/info/refs", ".git/git-upload-pack", ".git/git-receive-pack"},
		HeaderName:       "Authorization",
		Scheme:           SchemeBasic,
		FixedUsername:    "oauth2",
		DefaultEnvName:   "GITLAB_TOKEN",
		AllowRedirects:   false,
		AllowBinaryPack:  true,
		MaxResponseBytes: maxGitMediatedResponseBytes,
	},
	"npm_registry": {
		ID:               "npm_registry",
		Label:            "npm Registry",
		Host:             "registry.npmjs.org",
		Port:             "443",
		AllowedMethods:   []string{"GET", "HEAD", "PUT", "POST", "DELETE"},
		AllowedPaths:     []string{"/"},
		HeaderName:       "Authorization",
		Scheme:           SchemeBearer,
		DefaultEnvName:   "NPM_TOKEN",
		AllowRedirects:   false,
		MaxResponseBytes: defaultMaxMediatedResponseBytes,
	},
	"pypi_upload": {
		ID:               "pypi_upload",
		Label:            "PyPI Package Upload",
		Host:             "upload.pypi.org",
		Port:             "443",
		AllowedMethods:   []string{"POST"},
		AllowedPaths:     []string{"/legacy/"},
		HeaderName:       "Authorization",
		Scheme:           SchemeBasic,
		FixedUsername:    "__token__",
		DefaultEnvName:   "TWINE_PASSWORD",
		AllowRedirects:   false,
		MaxResponseBytes: defaultMaxMediatedResponseBytes,
	},
	"pypi_simple": {
		ID:               "pypi_simple",
		Label:            "PyPI Simple / JSON API",
		Host:             "pypi.org",
		Port:             "443",
		AllowedMethods:   []string{"GET", "HEAD"},
		AllowedPaths:     []string{"/simple/", "/pypi/"},
		HeaderName:       "Authorization",
		Scheme:           SchemeBearer,
		DefaultEnvName:   "PIP_INDEX_TOKEN",
		AllowRedirects:   false,
		MaxResponseBytes: defaultMaxMediatedResponseBytes,
	},
}

// LookupAdapter returns a reviewed CLI HTTPS adapter definition by ID.
func LookupAdapter(id string) (HTTPSAdapter, bool) {
	a, ok := reviewedHTTPSAdapters[strings.TrimSpace(strings.ToLower(id))]
	if !ok {
		return HTTPSAdapter{}, false
	}
	return a, true
}

// ListAdapters returns all reviewed CLI HTTPS adapters sorted stably by ID.
func ListAdapters() []HTTPSAdapter {
	ids := make([]string, 0, len(reviewedHTTPSAdapters))
	for id := range reviewedHTTPSAdapters {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make([]HTTPSAdapter, 0, len(ids))
	for _, id := range ids {
		out = append(out, reviewedHTTPSAdapters[id])
	}
	return out
}

// MatchesRequest validates canonical host, port, HTTP method, and cleaned path against the adapter policy.
func (a HTTPSAdapter) MatchesRequest(host, port, method, rawPath string) bool {
	if normalizeHostPattern(host) != normalizeHostPattern(a.Host) {
		return false
	}
	if strings.TrimSpace(port) != a.Port {
		return false
	}
	upperMethod := strings.ToUpper(strings.TrimSpace(method))
	if !slices.Contains(a.AllowedMethods, upperMethod) {
		return false
	}
	clean := cleanRequestPath(rawPath)
	if len(a.AllowedSuffixes) > 0 {
		for _, suffix := range a.AllowedSuffixes {
			if strings.HasSuffix(clean, suffix) {
				return true
			}
		}
		return false
	}
	for _, prefix := range a.AllowedPaths {
		if prefix == "/" {
			return strings.HasPrefix(clean, "/")
		}
		if pathMatchesPrefix(clean, prefix) {
			return true
		}
	}
	return false
}

// CanonicalOrigin returns the canonical HTTPS origin string ("https://host:port").
func (a HTTPSAdapter) CanonicalOrigin() string {
	return "https://" + a.Host + ":" + a.Port
}

func cleanRequestPath(rawPath string) string {
	base := sanitizeAuditPath(rawPath)
	if base == "" {
		return "/"
	}
	if !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	cleaned := path.Clean(base)
	if strings.HasSuffix(base, "/") && cleaned != "/" {
		cleaned += "/"
	}
	return cleaned
}

var envVarNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)

var reservedEnvVarNames = map[string]bool{
	"PATH":                   true,
	"HOME":                   true,
	"USER":                   true,
	"LOGNAME":                true,
	"SHELL":                  true,
	"TERM":                   true,
	"LANG":                   true,
	"LC_ALL":                 true,
	"TMPDIR":                 true,
	"PWD":                    true,
	"LD_PRELOAD":             true,
	"LD_LIBRARY_PATH":        true,
	"DYLD_INSERT_LIBRARIES":  true,
	"DYLD_LIBRARY_PATH":      true,
	"HTTP_PROXY":             true,
	"HTTPS_PROXY":            true,
	"ALL_PROXY":              true,
	"NO_PROXY":               true,
	"SSL_CERT_FILE":          true,
	"SSL_CERT_DIR":           true,
	"NODE_EXTRA_CA_CERTS":    true,
	"REQUESTS_CA_BUNDLE":     true,
	"CURL_CA_BUNDLE":         true,
	"GIT_SSL_CAINFO":         true,
	"ANTHROPIC_API_KEY":      true,
	"ANTHROPIC_AUTH_TOKEN":   true,
	"ANTHROPIC_BASE_URL":     true,
	"OPENAI_API_KEY":         true,
	"OPENAI_BASE_URL":        true,
	"OPENROUTER_API_KEY":     true,
	"GEMINI_API_KEY":         true,
	"GOOGLE_API_KEY":         true,
	"DATABASE_URL":           true,
	"NATS_URL":               true,
	"CLAUDE_CODE_USE_VERTEX": true,
}

// ValidateEnvPlaceholderName ensures an environment placeholder variable name is syntactically safe
// and cannot overwrite system, proxy, TLS trust, or LLM provider variables.
func ValidateEnvPlaceholderName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil
	}
	if !envVarNameRe.MatchString(trimmed) {
		return errors.New("invalid environment variable name")
	}
	if reservedEnvVarNames[trimmed] || strings.HasPrefix(trimmed, "BOTBOX_") {
		return fmt.Errorf("reserved environment variable name: %s", trimmed)
	}
	return nil
}

// CanonicalHTTPSOrigin parses and validates an HTTPS URL and returns its canonical "https://host:port", host, and port.
func CanonicalHTTPSOrigin(raw string) (origin, host, port string, err error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", "", errors.New("target origin is required")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", "", "", errors.New("invalid target URL")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", "", "", errors.New("only https targets are allowed")
	}
	if u.User != nil {
		return "", "", "", errors.New("target URL must not contain userinfo")
	}
	h := normalizeHostPattern(u.Hostname())
	if h == "" || strings.ContainsAny(h, " /\\*?@") {
		return "", "", "", errors.New("invalid target host")
	}
	if ip := net.ParseIP(h); ip != nil {
		return "", "", "", errors.New("raw IP targets are not allowed")
	}
	p := u.Port()
	if p == "" {
		p = "443"
	}
	if p != "443" {
		return "", "", "", errors.New("non-443 ports are not supported")
	}
	return "https://" + h + ":" + p, h, p, nil
}

// StrictHTTPSOrigin validates a canonical HTTPS DNS origin on port 443, without URL payloads.
func StrictHTTPSOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", errors.New("invalid HTTPS origin")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return "", errors.New("invalid HTTPS origin")
	}
	origin, host, _, err := CanonicalHTTPSOrigin(raw)
	if err != nil {
		return "", errors.New("invalid HTTPS origin")
	}
	// Use ASCII DNS names (IDNs must be supplied as punycode) to avoid ambiguous display.
	if len(host) > 253 || !strings.Contains(host, ".") {
		return "", errors.New("invalid HTTPS origin")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("invalid HTTPS origin")
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return "", errors.New("invalid HTTPS origin")
			}
		}
	}
	return origin, nil
}

// IsGitLabAdapter reports whether an adapter uses reviewed GitLab API or Git rules.
func IsGitLabAdapter(id string) bool { return id == "gitlab_api" || id == "gitlab_git" }

// ResolveAdapter binds reviewed GitLab behavior to an explicit HTTPS destination.
// Callers must authorize custom origins independently; this function only validates shape.
func ResolveAdapter(id, rawOrigin string) (HTTPSAdapter, error) {
	a, ok := LookupAdapter(id)
	if !ok {
		return HTTPSAdapter{}, errors.New("unsupported HTTPS adapter")
	}
	if strings.TrimSpace(rawOrigin) == "" {
		return a, nil
	}
	origin, err := StrictHTTPSOrigin(rawOrigin)
	if err != nil {
		return HTTPSAdapter{}, err
	}
	if origin == a.CanonicalOrigin() {
		return a, nil
	}
	if !IsGitLabAdapter(a.ID) {
		return HTTPSAdapter{}, errors.New("target origin does not match adapter")
	}
	_, a.Host, a.Port, err = CanonicalHTTPSOrigin(origin)
	if a.ID == "gitlab_api" {
		a.HeaderName = "PRIVATE-TOKEN"
		a.Scheme = SchemeRaw
	}
	return a, err
}

// MatchesEscapedRequest refuses encoded traversal while allowing encoded GitLab project paths.
func (a HTTPSAdapter) MatchesEscapedRequest(host, port, method, escapedPath string) bool {
	candidate := escapedPath
	for range 8 {
		if !a.MatchesRequest(host, port, method, candidate) {
			return false
		}
		decoded, err := url.PathUnescape(candidate)
		if err != nil {
			return false
		}
		if decoded == candidate {
			return true
		}
		candidate = decoded
	}
	return false
}
