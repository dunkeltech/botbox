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
	for _, fail := range []string{"", "create", "network", "start", "external network"} {
		t.Run(fail, func(t *testing.T) {
			var mu sync.Mutex
			configs := map[string]map[string]any{}
			deleted := map[string]bool{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				path := strings.TrimPrefix(r.URL.Path, "/v1.45")
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
					_ = json.NewEncoder(w).Encode(map[string]bool{"Internal": fail != "external network"})
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
