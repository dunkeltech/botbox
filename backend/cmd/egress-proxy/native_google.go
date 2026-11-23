// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"net"
	"net/http"
	"slices"
	"strings"

	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
)

// The browser completes sign-in outside the container. The CLI uses OAuth
// token exchange and userinfo, then connects to Cloud Code for model requests.
var nativeGoogleHosts = [...]string{
	"oauth2.googleapis.com", "www.googleapis.com",
	"daily-cloudcode-pa.googleapis.com", "cloudcode-pa.googleapis.com",
}

func isNativeGoogleHost(host string) bool {
	return slices.Contains(nativeGoogleHosts[:], strings.TrimSuffix(strings.ToLower(host), "."))
}

func newNativeGoogleConnect(connect *mitmproxy.Handler, enabled func(context.Context) (bool, error), serve func(http.ResponseWriter, *http.Request, string, http.RoundTripper)) http.Handler {
	allowed := func(ctx context.Context) bool {
		ok, err := enabled(ctx)
		return err == nil && ok
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port, err := net.SplitHostPort(strings.ToLower(r.Host))
		if err != nil || port != "443" || !isNativeGoogleHost(host) || !allowed(r.Context()) {
			http.Error(w, "metered LLM endpoint required", http.StatusForbidden)
			return
		}
		switch strings.TrimSuffix(host, ".") {
		case "oauth2.googleapis.com", "www.googleapis.com":
			connect.ServeUnmodified(w, r)
		default:
			connect.ServeInspected(w, r, func(iw http.ResponseWriter, ir *http.Request, host string, transport http.RoundTripper) {
				if !allowed(ir.Context()) {
					http.Error(iw, "Cloud Code access denied", http.StatusForbidden)
					return
				}
				serve(iw, ir, host, transport)
			})
		}
	})
}
