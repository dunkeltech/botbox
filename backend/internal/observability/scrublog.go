// SPDX-License-Identifier: AGPL-3.0-only

// Package observability provides secret-scrubbing structured logging,
// OpenTelemetry tracing, and Prometheus metrics for bot.Box services.
package observability

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
)

const redactedValue = "[REDACTED]"

var sensitiveKeys = map[string]bool{
	"password":          true,
	"password_hash":     true,
	"secret":            true,
	"secret_value":      true,
	"token":             true,
	"access_token":      true,
	"refresh_token":     true,
	"auth_broker_token": true,
	"authorization":     true,
	"api_key":           true,
	"apikey":            true,
	"kek":               true,
	"private_key":       true,
	"cookie":            true,
	"set-cookie":        true,
	"database_url":      true,
	"dsn":               true,
	"client_secret":     true,
	"wrapped_dek":       true,
	"encrypted_value":   true,
	"setup_secret":      true,
}

var secretValuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]{6,}`),
	regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{6,}\b`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{12,}\b`),
	regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|glpat)-[A-Za-z0-9_-]{8,}\b`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{12,}\b`),
	regexp.MustCompile(`(?i)\b(?:postgres(?:ql)?|nats)://[^:\s/@]+:[^@\s/]+@`),
	regexp.MustCompile(`\bbotbox_refresh=[A-Za-z0-9._~+/=-]{6,}`),
}

// ScrubString replaces recognizable credential material and DSN passwords with [REDACTED].
func ScrubString(s string) string {
	out := s
	for i, re := range secretValuePatterns {
		if i == 0 {
			out = re.ReplaceAllString(out, "$1 "+redactedValue)
			continue
		}
		if i == 5 {
			out = re.ReplaceAllStringFunc(out, func(match string) string {
				scheme, rest, _ := strings.Cut(match, "://")
				user, _, _ := strings.Cut(rest, ":")
				return scheme + "://" + user + ":" + redactedValue + "@"
			})
			continue
		}
		out = re.ReplaceAllString(out, redactedValue)
	}
	return out
}

type scrubbingHandler struct {
	inner  slog.Handler
	groups []string
}

// NewScrubbingHandler wraps inner so sensitive keys and token/DSN patterns are
// scrubbed before records reach logs.
func NewScrubbingHandler(inner slog.Handler) slog.Handler {
	if inner == nil {
		return nil
	}
	return &scrubbingHandler{inner: inner}
}

func (h *scrubbingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *scrubbingHandler) Handle(ctx context.Context, r slog.Record) error {
	clean := slog.NewRecord(r.Time, r.Level, ScrubString(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(scrubAttr(a))
		return true
	})
	return h.inner.Handle(ctx, clean)
}

func (h *scrubbingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cleaned := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		cleaned[i] = scrubAttr(a)
	}
	return &scrubbingHandler{
		inner:  h.inner.WithAttrs(cleaned),
		groups: h.groups,
	}
}

func (h *scrubbingHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	groups := make([]string, len(h.groups)+1)
	copy(groups, h.groups)
	groups[len(h.groups)] = name
	return &scrubbingHandler{
		inner:  h.inner.WithGroup(name),
		groups: groups,
	}
}

func isSensitiveKey(key string) bool {
	norm := strings.ToLower(strings.TrimSpace(key))
	if sensitiveKeys[norm] {
		return true
	}
	for _, suffix := range []string{"_secret", "_token", "_password", "_api_key", "_private_key"} {
		if strings.HasSuffix(norm, suffix) {
			return true
		}
	}
	return false
}

func scrubAttr(a slog.Attr) slog.Attr {
	if a.Key == "" && a.Value.Kind() == slog.KindAny && a.Value.Any() == nil {
		return a
	}
	resolved := a.Value.Resolve()
	if resolved.Kind() == slog.KindGroup {
		members := resolved.Group()
		cleaned := make([]slog.Attr, len(members))
		for i, m := range members {
			cleaned[i] = scrubAttr(m)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(cleaned...)}
	}
	if isSensitiveKey(a.Key) {
		return slog.String(a.Key, redactedValue)
	}
	switch resolved.Kind() {
	case slog.KindString:
		return slog.String(a.Key, ScrubString(resolved.String()))
	case slog.KindAny:
		if err, ok := resolved.Any().(error); ok && err != nil {
			return slog.String(a.Key, ScrubString(err.Error()))
		}
	}
	return slog.Attr{Key: a.Key, Value: resolved}
}
