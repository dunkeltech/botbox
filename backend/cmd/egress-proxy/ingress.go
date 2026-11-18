// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
)

func botNetworkIngress(subnet string, interfaces []net.Addr) (string, netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(subnet)
	if err != nil || !prefix.Addr().Is4() || prefix != prefix.Masked() || prefix.Bits() == 0 {
		return "", netip.Prefix{}, errors.New("valid bot network subnet required")
	}
	var local netip.Addr
	for _, iface := range interfaces {
		address, err := netip.ParsePrefix(iface.String())
		if err != nil || address.Masked() != prefix || address.Addr().IsLoopback() {
			continue
		}
		if local.IsValid() {
			return "", netip.Prefix{}, errors.New("ambiguous bot network interface")
		}
		local = address.Addr()
	}
	if !local.IsValid() {
		return "", netip.Prefix{}, errors.New("bot network interface unavailable")
	}
	return netip.AddrPortFrom(local, 8081).String(), prefix, nil
}

func restrictBotNetwork(prefix netip.Prefix, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil || !prefix.Contains(peer.Addr().Unmap()) {
			http.Error(w, "bot network required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
