// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/egresslink"
)

func runRelay(parent context.Context, logger *slog.Logger, identity egresslink.Identity) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	addr, prefix, err := boundIngress(os.Getenv("EGRESS_PROXY_BOT_SUBNET"))
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.New("egress relay listener unavailable")
	}
	defer listener.Close() //nolint:errcheck // listener cleanup
	context.AfterFunc(ctx, func() { _ = listener.Close() })
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: identity.TLS(false)}
	logger.Info("egress proxy listening", "addr", addr)
	limit := make(chan struct{}, 128)
	var active sync.WaitGroup
	defer func() { cancel(); active.Wait() }()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return errors.New("accept egress relay connection")
		}
		peer, err := netip.ParseAddrPort(conn.RemoteAddr().String())
		if err != nil || !prefix.Contains(peer.Addr().Unmap()) {
			_ = conn.Close()
			continue
		}
		select {
		case limit <- struct{}{}:
			active.Go(func() {
				defer func() { <-limit }()
				defer conn.Close() //nolint:errcheck // stream cleanup
				stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
				defer stop()
				upstream, err := dialer.DialContext(ctx, "tcp", "egress-upstream:8081")
				if err != nil {
					_, _ = io.WriteString(conn, "HTTP/1.1 503 Service Unavailable\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
					return
				}
				defer upstream.Close() //nolint:errcheck // stream cleanup
				relayConnection(conn.(*net.TCPConn), upstream.(*tls.Conn))
			})
		default:
			_ = conn.Close()
		}
	}
}

func relayConnection(bot *net.TCPConn, gateway *tls.Conn) {
	// Forward bytes without interpreting HTTP. CONNECT owns the connection
	// after its response, and SSE must preserve upstream flush behavior.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(gateway, bot)
		_ = gateway.CloseWrite()
	}()
	_, _ = io.Copy(bot, gateway)
	_ = bot.Close()
	_ = gateway.Close()
	<-done
}

func boundIngress(subnet string) (string, netip.Prefix, error) {
	interfaces, err := net.InterfaceAddrs()
	if err != nil {
		return "", netip.Prefix{}, errors.New("inspect egress interfaces")
	}
	return botNetworkIngress(subnet, interfaces)
}

func checkRelay(subnet string) error {
	addr, _, err := boundIngress(subnet)
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout:       2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{},
	}
	resp, err := client.Get("http://" + addr + "/healthz") //nolint:gosec // verified local interface
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // response cleanup
	if resp.StatusCode != http.StatusOK {
		return errors.New("egress worker not ready")
	}
	return nil
}
