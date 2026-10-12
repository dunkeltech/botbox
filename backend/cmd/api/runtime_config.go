package main

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// proxyDatabaseURL keeps control-plane credentials outside the sandbox while
// translating a host-local development database into Docker's host endpoint.
func proxyDatabaseURL(database, override string) (string, error) {
	explicit := strings.TrimSpace(override) != ""
	raw := strings.TrimSpace(database)
	if explicit {
		raw = strings.TrimSpace(override)
	}
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return "", errors.New("proxy database configuration requires a Postgres URL")
	}
	if !explicit && (parsed.Hostname() == "localhost" || net.ParseIP(parsed.Hostname()).IsLoopback()) {
		port := parsed.Port()
		if port == "" {
			port = "5432"
		}
		parsed.Host = net.JoinHostPort("host.docker.internal", port)
	}
	return parsed.String(), nil
}
