// SPDX-License-Identifier: AGPL-3.0-only

package docker_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/compute/docker"
)

func TestProxyProvisioningKeepsCredentialsOutsideSandbox(t *testing.T) {
	for _, fail := range []string{"", "create", "network", "start", "external network", "legacy image"} {
		t.Run(fail, func(t *testing.T) {
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
						_ = json.NewEncoder(w).Encode(map[string]any{"Id": name, "Config": config, "State": map[string]any{"Running": true}})
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
			opts := docker.Options{BaseURL: server.URL, Proxy: &docker.ProxyOptions{Image: "botbox/egress-proxy:dev", DatabaseURL: "postgres://fixture-db-password@db/botbox", KeySpec: "fixture-key-must-not-leak"}}
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
			if !strings.Contains(string(raw), "http://egress:8081") || !strings.Contains(string(raw), "BOTBOX_CA_URL=") {
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
			host := proxy["HostConfig"].(map[string]any)
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
			if !deleted["botbox-bot-bot"] || !deleted["botbox-egress-bot"] {
				t.Fatalf("cleanup=%v", deleted)
			}
		})
	}
	if _, err := docker.NewDriver(docker.Options{Proxy: &docker.ProxyOptions{Image: "proxy"}}); err == nil {
		t.Fatal("incomplete proxy configuration admitted")
	}
}

func TestBrokerTokenAndURLStayInProxyNeverInSandbox(t *testing.T) {
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
	if !strings.Contains(string(proxyRaw), "EGRESS_PROXY_AUTH_BROKER_URL=http://host.docker.internal:8085") ||
		!strings.Contains(string(proxyRaw), "EGRESS_PROXY_AUTH_BROKER_TOKEN="+secretBrokerToken) {
		t.Fatalf("proxy missing isolated broker bridge env: %s", proxyRaw)
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
		"Labels":   map[string]string{"botbox.workspace_id": workspace, "botbox.bot_id": bot},
		"IPAM":     map[string]any{"Config": []map[string]string{{"Subnet": "172.28.0.0/16"}}},
	})
}

func writeProxyImage(w http.ResponseWriter, secure bool) {
	labels := map[string]string{}
	if secure {
		labels["botbox.proxy_ingress"] = "bot-network-v1"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"Id": "sha256:test-image", "Config": map[string]any{"Labels": labels},
	})
}

func TestUpgradeProxyIngressRemovesLegacySidecarBeforeCreatingReplacement(t *testing.T) {
	var mu sync.Mutex
	var removed, created bool
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			_, _ = w.Write([]byte(`[
				{"Id":"proxy-legacy","Names":["/botbox-egress-bot"],"Labels":{"botbox.proxy":"true","botbox.workspace_id":"ws","botbox.bot_id":"bot"}},
				{"Id":"sandbox","State":"running","Names":["/botbox-bot-bot"],"Labels":{"botbox.managed":"true","botbox.workspace_id":"ws","botbox.bot_id":"bot"}}
			]`))
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/proxy-legacy"):
			removed = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/networks/"):
			writeProxyNetwork(w, true, "ws", "bot")
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/images/"):
			writeProxyImage(w, true)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/botbox-egress-bot/json"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/create"):
			if !removed {
				t.Error("legacy sidecar still active when replacement was created")
			}
			created = true
			_, _ = w.Write([]byte(`{"Id":"proxy-current"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/connect"),
			r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/proxy-current/start"):
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
	if err := driver.UpgradeProxyIngress(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !removed || !created {
		t.Fatal("legacy sidecar was not replaced")
	}
}
