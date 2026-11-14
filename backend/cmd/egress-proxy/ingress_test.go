package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBotNetworkIngress(t *testing.T) {
	t.Parallel()
	interfaces := []net.Addr{
		&net.IPNet{IP: net.ParseIP("172.17.0.3"), Mask: net.CIDRMask(16, 32)},
		&net.IPNet{IP: net.ParseIP("172.28.0.2"), Mask: net.CIDRMask(16, 32)},
	}
	addr, prefix, err := botNetworkIngress("172.28.0.0/16", interfaces)
	if err != nil || addr != "172.28.0.2:8081" {
		t.Fatalf("private ingress address = %q (%v)", addr, err)
	}
	for _, subnet := range []string{"", "0.0.0.0/0", "127.0.0.0/8", "172.30.0.0/16"} {
		if _, _, err := botNetworkIngress(subnet, interfaces); err == nil {
			t.Fatalf("accepted invalid or unavailable subnet %q", subnet)
		}
	}

	handler := restrictBotNetwork(prefix, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tc := range []struct {
		peer string
		want int
	}{
		{"172.28.0.3:12345", http.StatusNoContent},
		{"172.17.0.4:12345", http.StatusForbidden},
		{"127.0.0.1:12345", http.StatusForbidden},
		{"invalid-peer", http.StatusForbidden},
	} {
		t.Run(tc.peer, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req.RemoteAddr = tc.peer
			req.Header.Set("X-Forwarded-For", "172.28.0.3")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("peer %q: got %d, want %d", tc.peer, w.Code, tc.want)
			}
		})
	}
}
