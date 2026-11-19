// SPDX-License-Identifier: AGPL-3.0-only

package authbroker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRendererSandboxStatus(t *testing.T) {
	for _, test := range []struct {
		name string
		rows map[string]string
		want bool
	}{
		{"isolated renderer", map[string]string{"Layer 1 Sandbox": "Namespace", "PID namespaces": "Yes", "Network namespaces": "Yes", "Seccomp-BPF sandbox": "Yes"}, true},
		{"disabled seccomp", map[string]string{"Layer 1 Sandbox": "Namespace", "PID namespaces": "Yes", "Network namespaces": "Yes", "Seccomp-BPF sandbox": "No"}, false},
		{"missing namespace", map[string]string{"PID namespaces": "Yes", "Network namespaces": "Yes", "Seccomp-BPF sandbox": "Yes"}, false},
		{"disabled PID isolation", map[string]string{"Layer 1 Sandbox": "Namespace", "PID namespaces": "No", "Network namespaces": "Yes", "Seccomp-BPF sandbox": "Yes"}, false},
		{"missing diagnostics", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := rendererSandboxActive(test.rows); got != test.want {
				t.Fatalf("sandbox active = %v, want %v", got, test.want)
			}
		})
	}
}

func TestCDPRejectsReplacementAfterSandboxVerification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/version" {
			t.Error("attempted to open an unverified browser connection")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": "ws://" + r.Host + "/devtools/browser/replaced"})
	}))
	defer server.Close()
	engine := NewCDPBrowsingEngine(server.URL)
	engine.verifiedBrowser = "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/verified"
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := engine.dialBrowser(ctx); err == nil || err.Error() != "verified browser instance changed" {
		t.Fatalf("replacement browser was not rejected at discovery: %v", err)
	}
}
