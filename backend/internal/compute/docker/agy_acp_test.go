// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/compute"
)

func TestACPContainerIsolatesNativeCredentials(t *testing.T) {
	const workspace = "0b25c24d-d74c-4f92-ae39-af64697d6b61"
	const bot = "029e8140-ccde-47d0-922e-e5502c12e663"
	work, workNetwork, _, err := BuildContainerSpec(compute.BotConfig{
		WorkspaceID: workspace, BotID: bot, ProxyURL: "http://egress:8081",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	acp := agyACPSpec(workspace, bot)
	if err := ValidateContainerSpec(acp); err != nil {
		t.Fatal(err)
	}
	if !acp.HostConfig.ReadonlyRootfs || acp.User != "bot" || len(acp.HostConfig.PortBindings) != 0 ||
		acp.HostConfig.NetworkMode == workNetwork || len(acp.NetworkingConfig.EndpointsConfig) != 1 {
		t.Fatal("ACP environment must be isolated from the desktop")
	}
	if !slices.Contains(acp.HostConfig.Binds, agyACPName(bot)+":/home/bot") {
		t.Fatal("ACP must persist its login in a separate volume")
	}
	for _, bind := range acp.HostConfig.Binds {
		if slices.Contains(work.HostConfig.Binds, bind) || strings.Contains(bind, "-private-") {
			t.Fatalf("ACP has access to another trust domain: %s", bind)
		}
	}
	for _, env := range acp.Env {
		if strings.Contains(env, "GEMINI_API_KEY") || strings.Contains(env, "GOOGLE_APPLICATION_CREDENTIALS") ||
			strings.Contains(env, "GOOGLE_GEMINI_BASE_URL") {
			t.Fatalf("ACP environment attempts to configure provider authentication: %s", env)
		}
	}
}

func TestStoppingAgyACPBotStopsItsDedicatedRuntime(t *testing.T) {
	const workspace = "0b25c24d-d74c-4f92-ae39-af64697d6b61"
	const bot = "029e8140-ccde-47d0-922e-e5502c12e663"
	var mu sync.Mutex
	var stopped []string
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /v1.45/containers/work/stop", "POST /v1.45/containers/acp-id/stop":
			mu.Lock()
			stopped = append(stopped, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case "GET /v1.45/containers/work/json":
			_, _ = fmt.Fprintf(w, `{"Config":{"Labels":{"botbox.workspace_id":%q,"botbox.bot_id":%q,%q:%q}}}`,
				workspace, bot, proxyIngressLabel, proxyIngressVersion)
		case "GET /v1.45/containers/" + agyACPName(bot) + "/json":
			_, _ = fmt.Fprintf(w, `{"Id":"acp-id","State":{"Running":true},"Config":{"Labels":{%q:"true","botbox.workspace_id":%q,"botbox.bot_id":%q}},"NetworkSettings":{"Networks":{%q:{}}}}`,
				agyACPLabel, workspace, bot, proxyNetworkName(agyACPProxyNamespace(bot)))
		default:
			t.Errorf("unexpected Docker request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer daemon.Close()
	d, err := NewDriver(Options{BaseURL: daemon.URL, Proxy: &ProxyOptions{
		Image: "egress:dev", DatabaseURL: "postgres://fixture/botbox", KeySpec: "fixture-key",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.StopEnvironment(t.Context(), "work"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(stopped, []string{"/v1.45/containers/work/stop", "/v1.45/containers/acp-id/stop"}) {
		t.Fatalf("stop calls = %v", stopped)
	}
}
