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

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
