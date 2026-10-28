// Package authbroker implements an isolated browser authentication broker that
// owns credential-bearing browser profiles, targets, cookies, and login flows
// outside bot sandboxes (ADR-0016, issue #30).
package authbroker

import (
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
)

// DefaultBrokerSessionTTL bounds authenticated browser sessions inside the broker.
const DefaultBrokerSessionTTL = 30 * time.Minute

// LoginAdapter defines an explicitly reviewed web login flow permitted in the
// isolated authentication broker. Generic arbitrary-site login is rejected.
type LoginAdapter struct {
	ID               string
	Label            string
	Origin           string   // Canonical "https://host:port"
	LoginPath        string   // Expected login page path
	FormActionOrigin string   // Canonical "https://host:port" for form submission
	FormActionPath   string   // Expected form action path
	AllowedRedirects []string // Canonical origins allowed after login / navigation
	UsernameSelector string
	PasswordSelector string
	SubmitSelector   string
	SessionTTL       time.Duration
}

var reviewedLoginAdapters = map[string]LoginAdapter{
	"odoo_staging": {
		ID:               "odoo_staging",
		Label:            "Odoo Staging Web Login",
		Origin:           "https://staging.odoo.dunkel.tech:443",
		LoginPath:        "/web/login",
		FormActionOrigin: "https://staging.odoo.dunkel.tech:443",
		FormActionPath:   "/web/login",
		AllowedRedirects: []string{"https://staging.odoo.dunkel.tech:443"},
		UsernameSelector: "input[name='login']",
		PasswordSelector: "input[name='password']",
		SubmitSelector:   "button[type='submit']",
		SessionTTL:       DefaultBrokerSessionTTL,
	},
	"gitlab_web": {
		ID:               "gitlab_web",
		Label:            "GitLab Web Sign-In",
		Origin:           "https://gitlab.com:443",
		LoginPath:        "/users/sign_in",
		FormActionOrigin: "https://gitlab.com:443",
		FormActionPath:   "/users/sign_in",
		AllowedRedirects: []string{"https://gitlab.com:443"},
		UsernameSelector: "input[name='user[login]']",
		PasswordSelector: "input[name='user[password]']",
		SubmitSelector:   "button[type='submit']",
		SessionTTL:       DefaultBrokerSessionTTL,
	},
	"github_web": {
		ID:               "github_web",
		Label:            "GitHub Web Sign-In",
		Origin:           "https://github.com:443",
		LoginPath:        "/login",
		FormActionOrigin: "https://github.com:443",
		FormActionPath:   "/session",
		AllowedRedirects: []string{"https://github.com:443"},
		UsernameSelector: "input[name='login']",
		PasswordSelector: "input[name='password']",
		SubmitSelector:   "input[type='submit']",
		SessionTTL:       DefaultBrokerSessionTTL,
	},
}

// LookupLoginAdapter returns a reviewed browser login adapter by ID.
func LookupLoginAdapter(id string) (LoginAdapter, bool) {
	a, ok := reviewedLoginAdapters[strings.TrimSpace(strings.ToLower(id))]
	if !ok {
		return LoginAdapter{}, false
	}
	return a, true
}

// ResolveLoginAdapter binds reviewed GitLab behavior to an exact HTTPS origin.
// Authorization of a custom instance is a separate workspace and operator policy check.
func ResolveLoginAdapter(id, rawOrigin string) (LoginAdapter, error) {
	a, ok := LookupLoginAdapter(id)
	if !ok {
		return LoginAdapter{}, ErrAdapterNotFound
	}
	if strings.TrimSpace(rawOrigin) == "" {
		return a, nil
	}
	origin, err := StrictLoginOrigin(rawOrigin)
	if err != nil {
		return LoginAdapter{}, err
	}
	if origin == a.Origin {
		return a, nil
	}
	if a.ID != "gitlab_web" {
		return LoginAdapter{}, ErrNavigationForbidden
	}
	a.Origin = origin
	a.FormActionOrigin = origin
	a.AllowedRedirects = []string{origin}
	return a, nil
}

// StrictLoginOrigin rejects URLs that contain anything beyond an HTTPS origin.
func StrictLoginOrigin(raw string) (string, error) {
	origin, err := mitmproxy.StrictHTTPSOrigin(raw)
	if err != nil {
		return "", ErrNavigationForbidden
	}
	return origin, nil
}

// ListLoginAdapters returns all reviewed browser login adapters sorted by ID.
func ListLoginAdapters() []LoginAdapter {
	ids := make([]string, 0, len(reviewedLoginAdapters))
	for id := range reviewedLoginAdapters {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make([]LoginAdapter, 0, len(ids))
	for _, id := range ids {
		out = append(out, reviewedLoginAdapters[id])
	}
	return out
}

// LoginURL returns the canonical HTTPS login URL for the adapter.
func (a LoginAdapter) LoginURL() string {
	return a.Origin + a.LoginPath
}

// PermitsNavigation reports whether rawURL resolves to an allowed HTTPS origin for this adapter.
func (a LoginAdapter) PermitsNavigation(rawURL string) (string, error) {
	canon, _, _, err := mitmproxy.CanonicalHTTPSOrigin(rawURL)
	if err != nil {
		return "", err
	}
	if canon == a.Origin || slices.Contains(a.AllowedRedirects, canon) {
		return canon, nil
	}
	return "", errors.New("destination origin not permitted by login adapter")
}

// MatchesFormDestination validates the resolved form action URL against the adapter policy.
func (a LoginAdapter) MatchesFormDestination(pageURL, rawAction string) bool {
	base, err := url.Parse(strings.TrimSpace(pageURL))
	if err != nil || !strings.EqualFold(base.Scheme, "https") {
		return false
	}
	actionURL := base
	if trimmed := strings.TrimSpace(rawAction); trimmed != "" {
		rel, pErr := url.Parse(trimmed)
		if pErr != nil {
			return false
		}
		actionURL = base.ResolveReference(rel)
	}
	canon, _, _, err := mitmproxy.CanonicalHTTPSOrigin(actionURL.String())
	if err != nil {
		return false
	}
	if canon != a.FormActionOrigin {
		return false
	}
	cleanPath := actionURL.Path
	if cleanPath == "" {
		cleanPath = "/"
	}
	return cleanPath == a.FormActionPath
}

var sensitiveTextPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(cookie|set-cookie|authorization|bearer\s+[a-z0-9._-]+|session[_-]?id\s*[:=]\s*[^\s;]+)`),
	regexp.MustCompile(`(?i)(gh[pousr]_[A-Za-z0-9_]{8,}|glpat-[A-Za-z0-9_-]{8,})`),
}

// SanitizeOutput applies adapter output policy to strip session cookies, tokens,
// and sensitive credential patterns from bot-facing broker text.
func SanitizeOutput(text string, forbiddenValues ...string) string {
	out := text
	for _, v := range forbiddenValues {
		if v != "" {
			out = strings.ReplaceAll(out, v, "[REDACTED]")
		}
	}
	for _, pat := range sensitiveTextPatterns {
		out = pat.ReplaceAllString(out, "[REDACTED]")
	}
	return out
}
