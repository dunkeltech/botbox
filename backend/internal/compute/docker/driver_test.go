// SPDX-License-Identifier: AGPL-3.0-only

package docker_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/compute/docker"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	"google.golang.org/grpc"
)

func TestBuildAndValidateContainerSpecSecurityInvariants(t *testing.T) {
	t.Parallel()

	spec, netName, volName, err := docker.BuildContainerSpec(compute.BotConfig{
		WorkspaceID: "ws-1",
		BotID:       "bot-42",
		Image:       "botbox/sandbox:dev",
		CPUMillis:   2000,
		MemoryMiB:   2048,
		AgentToken:  "agent-secret-42",
	}, "")
	if err != nil {
		t.Fatalf("BuildContainerSpec failed: %v", err)
	}

	if netName != "botbox-net-bot-42" || volName != "botbox-home-bot-42" {
		t.Fatalf("unexpected names: net=%q vol=%q", netName, volName)
	}
	if spec.HostConfig.Privileged {
		t.Fatal("sandbox container must never be privileged")
	}
	if _, ok := spec.ExposedPorts["9090/tcp"]; !ok {
		t.Fatal("agent port must be exposed")
	}
	bindings := spec.HostConfig.PortBindings["9090/tcp"]
	if len(bindings) != 1 || bindings[0].HostIP != "127.0.0.1" || bindings[0].HostPort != "" {
		t.Fatalf("agent must publish on loopback with a dynamic port, got %+v", bindings)
	}
	for _, group := range spec.HostConfig.PortBindings {
		for _, binding := range group {
			if binding.HostIP == "0.0.0.0" || binding.HostIP == "::" {
				t.Fatalf("agent publication must not listen on every interface: %+v", binding)
			}
		}
	}
	if spec.HostConfig.NanoCPUs != 2_000_000_000 || spec.HostConfig.Memory != 2048*1024*1024 {
		t.Fatalf("unexpected resources: cpus=%d mem=%d", spec.HostConfig.NanoCPUs, spec.HostConfig.Memory)
	}
	if spec.HostConfig.PidsLimit == nil || *spec.HostConfig.PidsLimit != compute.DefaultPidsLimit {
		t.Fatalf("unexpected PidsLimit: %v", spec.HostConfig.PidsLimit)
	}

	// Negative security invariant validations.
	privileged := spec
	privileged.HostConfig.Privileged = true
	if err := docker.ValidateContainerSpec(privileged); !errors.Is(err, compute.ErrSecurityViolation) {
		t.Fatalf("privileged error = %v, want ErrSecurityViolation", err)
	}

	sockMount := spec
	sockMount.HostConfig.Binds = []string{"/var/run/docker.sock:/var/run/docker.sock"}
	if err := docker.ValidateContainerSpec(sockMount); !errors.Is(err, compute.ErrSecurityViolation) {
		t.Fatalf("docker.sock mount error = %v, want ErrSecurityViolation", err)
	}

	netAdmin := spec
	netAdmin.HostConfig.CapAdd = []string{"NET_ADMIN"}
	if err := docker.ValidateContainerSpec(netAdmin); !errors.Is(err, compute.ErrSecurityViolation) {
		t.Fatalf("NET_ADMIN cap error = %v, want ErrSecurityViolation", err)
	}

	// Raw credentials must never enter the sandbox container environment.
	rawSecretSpec := spec
	rawSecretSpec.Env = append(append([]string(nil), spec.Env...), "GITHUB_TOKEN=ghp_real_secret_value")
	if err := docker.ValidateContainerSpec(rawSecretSpec); !errors.Is(err, compute.ErrSecurityViolation) {
		t.Fatalf("raw secret env error = %v, want ErrSecurityViolation", err)
	}

	placeholderSpec := spec
	placeholderSpec.Env = append(append([]string(nil), spec.Env...), "ANTHROPIC_API_KEY=botbox-placeholder")
	if err := docker.ValidateContainerSpec(placeholderSpec); err != nil {
		t.Fatalf("placeholder env should be permitted, got %v", err)
	}

	// ProxyURL and CACertPath wire proxy env vars into the sandbox cleanly.
	proxySpec, _, _, err := docker.BuildContainerSpec(compute.BotConfig{
		WorkspaceID: "ws-1",
		BotID:       "bot-42",
		Image:       "botbox/sandbox:dev",
		CPUMillis:   2000,
		MemoryMiB:   2048,
		ProxyURL:    "http://127.0.0.1:8081",
	}, "")
	if err != nil {
		t.Fatalf("BuildContainerSpec with proxy failed: %v", err)
	}
	envMap := make(map[string]string, len(proxySpec.Env))
	for _, kv := range proxySpec.Env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			envMap[k] = v
		}
	}
	for _, want := range []struct {
		k string
		v string
	}{
		{"NO_PROXY", "localhost,127.0.0.1,::1"},
		{"HTTP_PROXY", "http://127.0.0.1:8081"},
		{"HTTPS_PROXY", "http://127.0.0.1:8081"},
		{"BOTBOX_CA_CERT", "/run/botbox/ca.crt"},
	} {
		if envMap[want.k] != want.v {
			t.Fatalf("proxy env %s = %q, want %q", want.k, envMap[want.k], want.v)
		}
	}

	egressSpec, _, _, err := docker.BuildContainerSpec(compute.BotConfig{
		WorkspaceID: "ws-1",
		BotID:       "bot-42",
		Image:       "botbox/sandbox:dev",
		CPUMillis:   2000,
		MemoryMiB:   2048,
		ProxyURL:    "http://egress:8081",
	}, "")
	if err != nil {
		t.Fatalf("BuildContainerSpec with egress proxy failed: %v", err)
	}
	hasEgressNoProxy := false
	for _, kv := range egressSpec.Env {
		if kv == "NO_PROXY=localhost,127.0.0.1,::1,egress" {
			hasEgressNoProxy = true
		}
	}
	if !hasEgressNoProxy {
		t.Fatalf("expected egress in NO_PROXY, got %v", egressSpec.Env)
	}
}

func TestAgentClientDialsLoopbackPublication(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		ports string
		want  string
	}{
		{name: "loopback", ports: `{"9090/tcp":[{"HostIp":"127.0.0.1","HostPort":"44000"}]}`, want: "127.0.0.1:44000"},
		{name: "wildcard becomes loopback", ports: `{"9090/tcp":[{"HostIp":"0.0.0.0","HostPort":"44001"}]}`, want: "127.0.0.1:44001"},
		{name: "lan binding is ignored", ports: `{"9090/tcp":[{"HostIp":"192.168.1.20","HostPort":"44002"}]}`, want: "172.20.0.5:9090"},
		{name: "unpublished bridge", ports: `{}`, want: "172.20.0.5:9090"},
		{name: "empty host port is ignored", ports: `{"9090/tcp":[{"HostIp":"127.0.0.1","HostPort":""}]}`, want: "172.20.0.5:9090"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1.45/containers/cid/json" {
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write([]byte(`{"Id":"cid","State":{"Running":true},"NetworkSettings":{"Ports":` + tc.ports + `,"Networks":{"botbox-net":{"IPAddress":"172.20.0.5"}}}}`))
			}))
			defer srv.Close()
			drv, err := docker.NewDriver(docker.Options{
				BaseURL: srv.URL,
				AgentDialer: func(_ context.Context, addr string) (*grpc.ClientConn, error) {
					got = addr
					return nil, errors.New("stop")
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			err = drv.RunCLI(context.Background(), "cid", "claude", "hi", "", "sonnet", func(engine.Event) error { return nil })
			if got != tc.want {
				t.Fatalf("dialed %q, want %q (err %v)", got, tc.want, err)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("dial error = %v", err)
			}
		})
	}
}

func TestDockerDriverLifecycleAndReconcile(t *testing.T) {
	t.Parallel()

	var (
		mu             sync.Mutex
		networkCreated bool
		networkIntern  bool
		running        bool
		containerID    = "cid-abc123"
		createdSpec    docker.ContainerCreateSpec
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		path := strings.TrimPrefix(r.URL.Path, "/v1.45")
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodPost && path == "/volumes/create":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Name":"botbox-home-bot-1"}`))

		case r.Method == http.MethodGet && strings.HasPrefix(path, "/networks/"):
			if !networkCreated {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"Name":"botbox-net-bot-1","Internal":true}`))

		case r.Method == http.MethodPost && path == "/networks/create":
			var req struct {
				Internal bool `json:"Internal"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			networkCreated = true
			networkIntern = req.Internal
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"net-1"}`))

		case r.Method == http.MethodPost && path == "/containers/create":
			if err := json.NewDecoder(r.Body).Decode(&createdSpec); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"cid-abc123"}`))

		case r.Method == http.MethodPost && path == "/containers/"+containerID+"/start":
			running = true
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodPost && path == "/containers/"+containerID+"/stop":
			running = false
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodGet && path == "/containers/"+containerID+"/json":
			state := "exited"
			if running {
				state = "running"
			}
			_, _ = w.Write([]byte(`{"Id":"cid-abc123","State":{"Status":"` + state + `","Running":` + boolString(running) + `},"NetworkSettings":{"Networks":{"botbox-net-bot-1":{"IPAddress":"172.20.0.5"}}}}`))

		case r.Method == http.MethodGet && path == "/containers/"+containerID+"/stats":
			_, _ = w.Write([]byte(`{"cpu_stats":{"cpu_usage":{"total_usage":200},"system_cpu_usage":1000,"online_cpus":2},"precpu_stats":{"cpu_usage":{"total_usage":100},"system_cpu_usage":500},"memory_stats":{"usage":536870912,"limit":2147483648}}`))

		case r.Method == http.MethodGet && path == "/containers/json":
			if running {
				_, _ = w.Write([]byte(`[{"Id":"cid-abc123","State":"running"}]`))
			} else {
				_, _ = w.Write([]byte(`[]`))
			}

		case r.Method == http.MethodDelete && path == "/containers/"+containerID:
			running = false
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	drv, err := docker.NewDriver(docker.Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewDriver failed: %v", err)
	}

	envID, err := drv.CreateEnvironment(context.Background(), compute.BotConfig{
		WorkspaceID: "ws-1",
		BotID:       "bot-1",
		CPUMillis:   2000,
		MemoryMiB:   2048,
	})
	if err != nil {
		t.Fatalf("CreateEnvironment failed: %v", err)
	}
	if envID != compute.EnvID(containerID) {
		t.Fatalf("envID = %q, want %q", envID, containerID)
	}
	if !networkCreated || !networkIntern {
		t.Fatalf("expected internal docker network creation, created=%v internal=%v", networkCreated, networkIntern)
	}
	if createdSpec.HostConfig.Privileged {
		t.Fatal("created container must not be privileged")
	}

	if err := drv.StartEnvironment(context.Background(), envID); err != nil {
		t.Fatalf("StartEnvironment failed: %v", err)
	}
	st, err := drv.Status(context.Background(), envID)
	if err != nil || st != compute.StatusRunning {
		t.Fatalf("Status = (%q, %v), want running", st, err)
	}

	vncURL, err := drv.GetVNCStreamURL(context.Background(), envID)
	if err != nil || vncURL != "ws://172.20.0.5:6080" {
		t.Fatalf("GetVNCStreamURL = (%q, %v), want ws://172.20.0.5:6080", vncURL, err)
	}

	stats, err := drv.Stats(context.Background(), envID)
	if err != nil || stats.MemoryUsedMiB != 512 || stats.MemoryLimitMiB != 2048 || stats.CPUPercent <= 0 {
		t.Fatalf("unexpected stats: %+v err=%v", stats, err)
	}

	if err := drv.StopEnvironment(context.Background(), envID); err != nil {
		t.Fatalf("StopEnvironment failed: %v", err)
	}

	// Verify VNC URL fails when stopped.
	if _, err := drv.GetVNCStreamURL(context.Background(), envID); !errors.Is(err, compute.ErrEnvironmentNotRunning) {
		t.Fatalf("GetVNCStreamURL when stopped err = %v, want ErrEnvironmentNotRunning", err)
	}

	// Reconcile should flag envID and stale-id as missing/stopped.
	missing, err := drv.Reconcile(context.Background(), []compute.EnvID{envID, "stale-id"})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if len(missing) != 2 {
		t.Fatalf("missing = %v, want 2 missing environments", missing)
	}

	if err := drv.DestroyEnvironment(context.Background(), envID); err != nil {
		t.Fatalf("DestroyEnvironment failed: %v", err)
	}
}

func TestDockerDriverHandlesEngineFailure(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"docker daemon unavailable"}`))
	}))
	defer srv.Close()

	drv, err := docker.NewDriver(docker.Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewDriver failed: %v", err)
	}

	if _, err := drv.CreateEnvironment(context.Background(), compute.BotConfig{
		WorkspaceID: "ws-1",
		BotID:       "bot-1",
	}); err == nil || !strings.Contains(err.Error(), "docker daemon unavailable") {
		t.Fatalf("expected docker daemon error, got: %v", err)
	}
}

func TestCreateReusesExistingHomeVolume(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1.45")
		switch {
		case r.Method == http.MethodPost && path == "/volumes/create":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"volume already exists"}`))
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/networks/"):
			_, _ = w.Write([]byte(`{"Internal":true}`))
		case r.Method == http.MethodPost && path == "/containers/create":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"cid-reused-volume"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	drv, err := docker.NewDriver(docker.Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	id, err := drv.CreateEnvironment(context.Background(), compute.BotConfig{
		WorkspaceID: "ws-1",
		BotID:       "bot-1",
		VolumeRef:   "botbox-home-bot-1",
	})
	if err != nil || id != "cid-reused-volume" {
		t.Fatalf("existing home volume was not reused: %v %s", err, id)
	}
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
