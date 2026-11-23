// SPDX-License-Identifier: AGPL-3.0-only

package mitmproxy

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// ServeInspected terminates an authorized CONNECT and dispatches HTTPS requests
// to a fixed, trusted application handler. The provided transport verifies the
// origin certificate and dials only public addresses for the CONNECT target.
func (h *Handler) ServeInspected(w http.ResponseWriter, r *http.Request, serve func(http.ResponseWriter, *http.Request, string, http.RoundTripper)) {
	host, port, snap, err := h.inspectionTarget(r)
	if err != nil || serve == nil {
		http.Error(w, "inspection unavailable", http.StatusForbidden)
		return
	}
	transport := h.interceptTransport(host, port, true)
	transport.ResponseHeaderTimeout = 2 * time.Minute
	defer transport.CloseIdleConnections()
	h.serveTLS(w, r, host, true, http.HandlerFunc(func(iw http.ResponseWriter, ir *http.Request) {
		authority := strings.ToLower(ir.Host)
		if (authority != host && authority != host+":443") || ir.URL.IsAbs() || ir.Method == http.MethodConnect {
			http.Error(iw, "invalid inspected authority", http.StatusForbidden)
			return
		}
		current, policyErr := h.resolveSnapshot(ir.Context())
		if policyErr != nil || current.Revision < snap.Revision || !h.isHostAllowed(current, host) {
			http.Error(iw, "inspection unavailable", http.StatusForbidden)
			return
		}
		serve(iw, ir, host, transport)
	}))
}

// ServeUnmodified authorizes a native TLS tunnel without applying any
// workspace credential rule to it. Adding such a rule closes active tunnels.
func (h *Handler) ServeUnmodified(w http.ResponseWriter, r *http.Request) {
	host, port, snap, err := h.inspectionTarget(r)
	if err != nil {
		http.Error(w, "tunnel unavailable", http.StatusForbidden)
		return
	}
	if _, hasRule := h.firstHostRule(snap, host); hasRule {
		http.Error(w, "workspace credential rule conflicts with native login", http.StatusForbidden)
		return
	}
	h.handlePassthrough(w, r, host, port, snap.Revision, true)
}

// verifyTunnelServerName confirms the TLS ClientHello targets the allowed
// origin. Without this check, a shared Google IP could receive a model
// handshake through a tunnel that was opened for OAuth. No flight from the
// TLS parser is sent to either peer.
func verifyTunnelServerName(conn net.Conn, wire *bufio.ReadWriter, host string) bool {
	var hello bytes.Buffer
	probe := &tlsHelloProbe{Conn: conn, reader: io.TeeReader(io.LimitReader(wire, 64<<10), &hello)}
	seen := false
	parsed := errors.New("TLS ClientHello parsed")
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	server := tls.Server(probe, &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetConfigForClient: func(info *tls.ClientHelloInfo) (*tls.Config, error) {
			seen = strings.EqualFold(info.ServerName, host)
			return nil, parsed
		},
	})
	if err := server.Handshake(); !seen || !errors.Is(err, parsed) {
		return false
	}
	wire.Reader = bufio.NewReader(io.MultiReader(bytes.NewReader(hello.Bytes()), wire.Reader))
	return true
}

type tlsHelloProbe struct {
	net.Conn
	reader io.Reader
}

func (p *tlsHelloProbe) Read(data []byte) (int, error)  { return p.reader.Read(data) }
func (p *tlsHelloProbe) Write(data []byte) (int, error) { return len(data), nil }

func (h *Handler) inspectionTarget(r *http.Request) (string, string, PolicySnapshot, error) {
	host, port, err := parseConnectTarget(r.Host)
	if err != nil || port != "443" || r.Method != http.MethodConnect {
		return "", "", PolicySnapshot{}, errors.New("invalid CONNECT target")
	}
	snap, err := h.resolveSnapshot(r.Context())
	if err != nil || !h.isHostAllowed(snap, host) {
		return "", "", PolicySnapshot{}, errors.New("CONNECT denied")
	}
	return host, port, snap, nil
}
