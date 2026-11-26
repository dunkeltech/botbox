// SPDX-License-Identifier: AGPL-3.0-only

package docker_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/compute/docker"
)

func TestProxyProvisioningKeepsCredentialsOutsideSandbox(t *testing.T) {
	for _, tc := range []struct{ name, fail, workerNetwork string }{
		{name: "default bridge"},
		{name: "compose control bridge", workerNetwork: "botbox-test-control"},
		{name: "create failure", fail: "create"},
		{name: "network failure", fail: "network"},
		{name: "start failure", fail: "start"},
		{name: "external bot network", fail: "external network"},
		{name: "legacy image", fail: "legacy image"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fail := tc.fail
			var mu sync.Mutex
			configs := map[string]map[string]any{}
			deleted := map[string]bool{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				path := strings.TrimPrefix(r.URL.Path, "/v1.45")
				if r.Method == http.MethodGet && strings.HasPrefix(path, "/images/") {
					writeProxyImage(w, fail != "legacy image")
					return
				}
				if r.Method == "GET" && strings.HasSuffix(path, "/json") {
					name := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json")
					if config, ok := configs[name]; ok {
						host := config["HostConfig"].(map[string]any)
						network := host["NetworkMode"].(string)
						_ = json.NewEncoder(w).Encode(map[string]any{
							"Id": name, "Config": config,
							"NetworkSettings": map[string]any{"Networks": map[string]any{network: map[string]any{}}},
							"State":           map[string]any{"Running": true, "Health": map[string]string{"Status": "healthy"}},
						})
						return
					}
					http.Error(w, "missing", 404)
					return
				}
				if r.Method == "GET" && strings.HasPrefix(path, "/networks/") {
					writeProxyNetwork(w, fail != "external network", "workspace", "bot")
					return
				}
				if path == "/containers/create" {
					var spec map[string]any
					if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					name := r.URL.Query().Get("name")
					if strings.HasPrefix(name, "botbox-egress-") && fail == "create" {
						http.Error(w, "fixture-key-must-not-leak", 500)
						return
					}
					configs[name] = spec
					_ = json.NewEncoder(w).Encode(map[string]string{"Id": name})
					return
				}
				if strings.HasSuffix(path, "/connect") && fail == "network" {
					http.Error(w, "fixture-key-must-not-leak", 500)
					return
				}
				if strings.HasPrefix(path, "/containers/botbox-egress-") && strings.HasSuffix(path, "/start") && fail == "start" {
					http.Error(w, "fixture-key-must-not-leak", 500)
					return
				}
				if r.Method == "DELETE" {
					deleted[strings.TrimPrefix(path, "/containers/")] = true
				}
				w.WriteHeader(200)
			}))
			defer server.Close()
			metricsEnabled := tc.workerNetwork != ""
			opts := docker.Options{BaseURL: server.URL, Proxy: &docker.ProxyOptions{Image: "botbox/egress-proxy:dev", WorkerNetwork: tc.workerNetwork, WorkerMetrics: metricsEnabled, DatabaseURL: "postgres://fixture-db-password@db/botbox", KeySpec: "fixture-key-must-not-leak"}}
			d, err := docker.NewDriver(opts)
			if err != nil {
				t.Fatal(err)
			}
			id, err := d.CreateEnvironment(context.Background(), compute.BotConfig{WorkspaceID: "workspace", BotID: "bot"})
			if fail != "" {
				if err == nil || strings.Contains(err.Error(), "fixture-key") {
					t.Fatalf("failure leaked or admitted: %v", err)
				}
				if _, exists := configs["botbox-bot-bot"]; exists {
					t.Fatal("sandbox created after proxy failure")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			sandbox := configs["botbox-bot-bot"]
			hasAgentToken := false
			for _, entry := range sandbox["Env"].([]any) {
				if value, ok := entry.(string); ok && strings.HasPrefix(value, "BOTBOX_AGENT_TOKEN=") && len(value) > 50 {
					hasAgentToken = true
				}
			}
			if !hasAgentToken {
				t.Fatal("missing generated agent control token")
			}
			raw, _ := json.Marshal(sandbox)
			if strings.Contains(string(raw), "fixture-key") || strings.Contains(string(raw), "fixture-db-password") {
				t.Fatal("control-plane credentials entered sandbox")
			}
			if !strings.Contains(string(raw), "http://egress:8081") || !strings.Contains(string(raw), "botbox-egress-ca-public-bot:/run/botbox/egress-ca:ro") {
				t.Fatal("sandbox proxy configuration missing")
			}
			proxy := configs["botbox-egress-bot"]
			if proxy["Image"] != "sha256:test-image" {
				t.Fatal("proxy was not pinned to the inspected image")
			}
			proxyEnv, _ := json.Marshal(proxy["Env"])
			if !strings.Contains(string(proxyEnv), "EGRESS_PROXY_BOT_SUBNET=172.28.0.0/16") || strings.Contains(string(proxyEnv), "EGRESS_PROXY_ADDR=") {
				t.Fatal("proxy must bind to its bot network")
			}
			for _, forbidden := range []string{"DATABASE_URL", "BOTBOX_SECRETS_KEK", "NATS_URL", "AUTH_BROKER_TOKEN", "fixture-key", "fixture-db-password"} {
				if strings.Contains(string(proxyEnv), forbidden) {
					t.Fatalf("relay received control-plane configuration %q", forbidden)
				}
			}
			worker := configs["botbox-egress-worker-bot"]
			wantNetwork := tc.workerNetwork
			if wantNetwork == "" {
				wantNetwork = "bridge"
			}
			if worker == nil || worker["HostConfig"].(map[string]any)["NetworkMode"] != wantNetwork {
				t.Fatal("trusted worker missing external network")
			}
			workerEnv, _ := json.Marshal(worker["Env"])
			workerNetworking, _ := json.Marshal(worker["NetworkingConfig"])
			if strings.Contains(string(workerEnv), "EGRESS_PROXY_WORKER_METRICS=1") != metricsEnabled ||
				strings.Contains(string(workerNetworking), "botbox-egress-metrics") != metricsEnabled {
				t.Fatal("worker metrics flag and control-network alias must match the opt-in")
			}
			if strings.Contains(string(proxyEnv), "WORKER_METRICS") || strings.Contains(string(raw), "WORKER_METRICS") {
				t.Fatal("worker metrics configuration reached the relay or sandbox")
			}
			host := proxy["HostConfig"].(map[string]any)
			if host["NetworkMode"] != "botbox-egress-net-bot" {
				t.Fatal("relay has a route outside the isolated bot network")
			}
			if host["Privileged"] != false || host["ReadonlyRootfs"] != true || proxy["User"] != "65532:65532" || strings.Contains(string(raw), "docker.sock") {
				t.Fatal("proxy isolation missing")
			}
			if err = d.StartEnvironment(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if err = d.CLIProxyReady(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if err = d.DestroyEnvironment(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if !deleted["botbox-bot-bot"] || !deleted["botbox-egress-bot"] || !deleted["botbox-egress-worker-bot"] {
				t.Fatalf("cleanup=%v", deleted)
			}
		})
	}
	if _, err := docker.NewDriver(docker.Options{Proxy: &docker.ProxyOptions{Image: "proxy"}}); err == nil {
		t.Fatal("incomplete proxy configuration admitted")
	}
	if _, err := docker.NewDriver(docker.Options{Proxy: &docker.ProxyOptions{Image: "proxy", DatabaseURL: "db", KeySpec: "key", WorkerMetrics: true}}); err == nil {
		t.Fatal("worker metrics enabled without a dedicated control network")
	}
}

func TestBrokerTokenAndURLStayInTrustedWorker(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	configs := map[string]map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, "/v1.45")
		if r.Method == http.MethodGet && strings.HasPrefix(path, "/images/") {
			writeProxyImage(w, true)
			return
		}
		if r.Method == "GET" && strings.HasSuffix(path, "/json") {
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		if r.Method == "GET" && strings.HasPrefix(path, "/networks/") {
			writeProxyNetwork(w, true, "ws-b", "bot-b")
			return
		}
		if path == "/containers/create" {
			var spec map[string]any
			if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			name := r.URL.Query().Get("name")
			configs[name] = spec
			_ = json.NewEncoder(w).Encode(map[string]string{"Id": name})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	const secretBrokerToken = "super-secret-broker-token-98765"
	d, err := docker.NewDriver(docker.Options{
		BaseURL:           server.URL,
		AuthBrokerEnabled: true,
		Proxy: &docker.ProxyOptions{ //nolint:gosec // synthetic test fixture
			Image:           "botbox/egress-proxy:dev",
			DatabaseURL:     "postgres://fixture-db/botbox",
			KeySpec:         "fixture-kek-material",
			AuthBrokerURL:   "http://host.docker.internal:8085",
			AuthBrokerToken: secretBrokerToken,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateEnvironment(context.Background(), compute.BotConfig{WorkspaceID: "ws-b", BotID: "bot-b"}); err != nil {
		t.Fatalf("CreateEnvironment failed: %v", err)
	}

	sandboxRaw, _ := json.Marshal(configs["botbox-bot-bot-b"]["Env"])
	if !strings.Contains(string(sandboxRaw), "BOTBOX_AUTH_BROKER_ENABLED=true") {
		t.Fatalf("sandbox missing non-secret broker capability flag: %s", sandboxRaw)
	}
	if strings.Contains(string(sandboxRaw), secretBrokerToken) || strings.Contains(string(sandboxRaw), "host.docker.internal:8085") {
		t.Fatalf("broker token or internal URL leaked into sandbox env: %s", sandboxRaw)
	}

	proxyRaw, _ := json.Marshal(configs["botbox-egress-bot-b"]["Env"])
	if strings.Contains(string(proxyRaw), secretBrokerToken) {
		t.Fatal("browser broker token entered the relay")
	}
	workerRaw, _ := json.Marshal(configs["botbox-egress-worker-bot-b"]["Env"])
	if !strings.Contains(string(workerRaw), "EGRESS_PROXY_AUTH_BROKER_URL=http://host.docker.internal:8085") ||
		!strings.Contains(string(workerRaw), "EGRESS_PROXY_AUTH_BROKER_TOKEN="+secretBrokerToken) {
		t.Fatal("worker missing isolated broker bridge configuration")
	}

	// ValidateContainerSpec rejects any manual broker token injection into sandbox containers.
	if err := docker.ValidateContainerSpec(docker.ContainerCreateSpec{
		Env: []string{"BOTBOX_AUTH_BROKER_TOKEN=" + secretBrokerToken},
		HostConfig: docker.ContainerHostConfig{
			CapDrop:     []string{"ALL"},
			SecurityOpt: []string{"no-new-privileges:true"},
		},
	}); err == nil {
		t.Fatal("expected ValidateContainerSpec to reject broker token env in sandbox")
	}
}

func writeProxyNetwork(w http.ResponseWriter, internal bool, workspace, bot string) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"Internal": internal,
		"Options":  map[string]string{"com.docker.network.bridge.gateway_mode_ipv4": "isolated"},
		"Labels":   map[string]string{"botbox.workspace_id": workspace, "botbox.bot_id": bot},
		"IPAM":     map[string]any{"Config": []map[string]string{{"Subnet": "172.28.0.0/16"}}},
	})
}

func writeProxyImage(w http.ResponseWriter, secure bool) {
	labels := map[string]string{}
	if secure {
		labels["botbox.proxy_ingress"] = "bot-network-v2"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"Id": "sha256:test-image", "Config": map[string]any{"Labels": labels},
	})
}

func TestUpgradeProxyIngressStopsLegacyComputers(t *testing.T) {
	var mu sync.Mutex
	var removed, stopped bool
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			_, _ = w.Write([]byte(`[
				{"Id":"other-proxy","Names":["/botbox-egress-other-bot"],"Labels":{"botbox.proxy":"true","botbox.workspace_id":"other-ws","botbox.bot_id":"other-bot"}},
				{"Id":"other-sandbox","State":"running","Names":["/botbox-bot-other-bot"],"Labels":{"botbox.managed":"true","botbox.workspace_id":"other-ws","botbox.bot_id":"other-bot"}},
				{"Id":"wrong-workspace","State":"running","Names":["/botbox-bot-bot"],"Labels":{"botbox.managed":"true","botbox.workspace_id":"other-ws","botbox.bot_id":"bot"}},
				{"Id":"proxy-legacy","Names":["/botbox-egress-bot"],"Labels":{"botbox.proxy":"true","botbox.workspace_id":"ws","botbox.bot_id":"bot"}},
				{"Id":"sandbox","State":"running","Names":["/botbox-bot-bot"],"Labels":{"botbox.managed":"true","botbox.workspace_id":"ws","botbox.bot_id":"bot"}}
			]`))
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/proxy-legacy"):
			removed = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/sandbox/stop"):
			if !removed {
				t.Error("legacy proxy still holds credentials")
			}
			stopped = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Docker call %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer engine.Close()

	driver, err := docker.NewDriver(docker.Options{BaseURL: engine.URL, Proxy: &docker.ProxyOptions{
		Image: "egress:dev", DatabaseURL: "postgres://db/botbox", KeySpec: "fixture-key",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.UpgradeProxyIngress(t.Context(), map[string]string{"bot": "ws"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !removed || !stopped {
		t.Fatal("legacy sidecar was not removed or legacy computer was not stopped")
	}
}

func TestWorkerNetworkRejectsNonBridgeModes(t *testing.T) {
	for _, network := range []string{"host", "none", "container:shared", "botbox-egress-net-other"} {
		t.Run(network, func(t *testing.T) {
			_, err := docker.NewDriver(docker.Options{Proxy: &docker.ProxyOptions{
				Image: "egress:dev", DatabaseURL: "postgres://db/botbox", KeySpec: "fixture-key", WorkerNetwork: network,
			}})
			if err == nil {
				t.Fatal("unsafe worker network accepted")
			}
		})
	}
}

func TestStartLegacyComputerRetainsStoppedFilesystem(t *testing.T) {
	var steps []string
	var lock sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lock.Lock()
		defer lock.Unlock()
		steps = append(steps, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/legacy/json"):
			_, _ = w.Write([]byte(`{"Id":"legacy","Name":"/botbox-bot-bot","State":{"Running":true},"Config":{"Labels":{"botbox.managed":"true","botbox.egress_proxy":"metered-v1","botbox.workspace_id":"workspace","botbox.bot_id":"bot"}}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/legacy/stop"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/legacy/rename") && r.URL.Query().Get("name") == "botbox-retired-legacy":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Docker request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	driver, err := docker.NewDriver(docker.Options{BaseURL: server.URL, Proxy: &docker.ProxyOptions{
		Image: "egress:dev", DatabaseURL: "postgres://db/botbox", KeySpec: "fixture-key",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.StartEnvironment(t.Context(), "legacy"); !errors.Is(err, compute.ErrEnvironmentNotFound) {
		t.Fatalf("legacy computer did not request safe reprovisioning: %v", err)
	}
	lock.Lock()
	defer lock.Unlock()
	want := []string{"GET /v1.45/containers/legacy/json", "POST /v1.45/containers/legacy/stop", "POST /v1.45/containers/legacy/rename"}
	if !slices.Equal(steps, want) {
		t.Fatalf("legacy upgrade requests = %v, want %v", steps, want)
	}
}
