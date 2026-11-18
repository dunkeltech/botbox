// SPDX-License-Identifier: AGPL-3.0-only

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
	if !explicit && isLoopbackHost(parsed.Hostname()) {
		port := parsed.Port()
		if port == "" {
			port = "5432"
		}
		parsed.Host = net.JoinHostPort("host.docker.internal", port)
	}
	return parsed.String(), nil
}

// proxyNATSURL translates a host-local NATS endpoint into Docker's host endpoint
// so per-bot egress proxy containers can receive revision-aware invalidations.
func proxyNATSURL(natsURL, override string) string {
	explicit := strings.TrimSpace(override) != ""
	raw := strings.TrimSpace(natsURL)
	if explicit {
		raw = strings.TrimSpace(override)
	}
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return raw
	}
	if !explicit && isLoopbackHost(parsed.Hostname()) {
		port := parsed.Port()
		if port == "" {
			port = "4222"
		}
		parsed.Host = net.JoinHostPort("host.docker.internal", port)
	}
	return parsed.String()
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
