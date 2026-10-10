// Package docker implements compute.Driver against the Docker Engine HTTP API.
//
// Only the control-plane API process communicates with the Docker socket; sandboxes
// never receive the Docker socket, run unprivileged with all capabilities dropped,
// and connect only to per-bot internal networks with no internet egress route.
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/dunkeltech/botbox/backend/internal/agent"
	"github.com/dunkeltech/botbox/backend/internal/compute"
	agentv1 "github.com/dunkeltech/botbox/backend/internal/gen/proto/agent/v1"
)

const (
	defaultDockerSocket  = "/var/run/docker.sock"
	defaultAPIVersion    = "v1.45"
	defaultWebsockPort   = 6080
	defaultAgentGRPCPort = 9090
)

// Options configures how the Docker compute driver connects to the Docker Engine API.
type Options struct {
	// SocketPath overrides /var/run/docker.sock when BaseURL is empty.
	SocketPath string
	// BaseURL overrides the HTTP base URL (primarily for mock/integration tests).
	BaseURL string
	// HTTPClient overrides the HTTP transport used to call the Docker Engine API.
	HTTPClient *http.Client
	// SeccompProfile optionally embeds a seccomp JSON string into SecurityOpt.
	SeccompProfile string
	// AgentDialer optionally overrides how gRPC connections to a container agent are dialed.
	AgentDialer func(ctx context.Context, addr string) (*grpc.ClientConn, error)
}

// Driver implements compute.Driver using Docker Engine HTTP API calls
// and per-sandbox gRPC AgentService calls.
type Driver struct {
	client         *http.Client
	baseURL        string
	seccompProfile string
	agentDialer    func(ctx context.Context, addr string) (*grpc.ClientConn, error)

	mu     sync.RWMutex
	tokens map[compute.EnvID]string
}

// ContainerHostConfig captures Docker HostConfig security and resource constraints.
type ContainerHostConfig struct {
	Privileged  bool     `json:"Privileged"`
	CapDrop     []string `json:"CapDrop"`
	CapAdd      []string `json:"CapAdd,omitempty"`
	SecurityOpt []string `json:"SecurityOpt"`
	NanoCPUs    int64    `json:"NanoCPUs"`
	Memory      int64    `json:"Memory"`
	PidsLimit   *int64   `json:"PidsLimit"`
	ShmSize     int64    `json:"ShmSize"`
	Binds       []string `json:"Binds"`
	NetworkMode string   `json:"NetworkMode"`
}

// ContainerNetworkingConfig attaches a container to a single isolated bot network.
type ContainerNetworkingConfig struct {
	EndpointsConfig map[string]map[string]any `json:"EndpointsConfig"`
}

// ContainerCreateSpec is the JSON body posted to Docker Engine /containers/create.
type ContainerCreateSpec struct {
	Image            string                    `json:"Image"`
	Env              []string                  `json:"Env,omitempty"`
	Labels           map[string]string         `json:"Labels"`
	HostConfig       ContainerHostConfig       `json:"HostConfig"`
	NetworkingConfig ContainerNetworkingConfig `json:"NetworkingConfig"`
}

// NewDriver creates a Docker ComputeDriver using either opts.BaseURL or opts.SocketPath.
func NewDriver(opts Options) (*Driver, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	client := opts.HTTPClient

	if baseURL == "" {
		socketPath := strings.TrimSpace(opts.SocketPath)
		if socketPath == "" {
			socketPath = defaultDockerSocket
		}
		baseURL = "http://docker/" + defaultAPIVersion
		if client == nil {
			dialer := &net.Dialer{Timeout: 5 * time.Second}
			client = &http.Client{
				Timeout: 30 * time.Second,
				Transport: &http.Transport{
					DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
						return dialer.DialContext(ctx, "unix", socketPath)
					},
				},
			}
		}
	} else {
		if !strings.Contains(baseURL, "/v1.") {
			baseURL += "/" + defaultAPIVersion
		}
		if client == nil {
			client = &http.Client{Timeout: 30 * time.Second}
		}
	}

	return &Driver{
		client:         client,
		baseURL:        baseURL,
		seccompProfile: strings.TrimSpace(opts.SeccompProfile),
		agentDialer:    opts.AgentDialer,
		tokens:         make(map[compute.EnvID]string),
	}, nil
}

// BuildContainerSpec builds and validates the container creation payload for cfg,
// enforcing AGENTS.md §7 sandbox security and isolation invariants.
func BuildContainerSpec(cfg compute.BotConfig, seccompProfile string) (ContainerCreateSpec, string, string, error) {
	if strings.TrimSpace(cfg.WorkspaceID) == "" || strings.TrimSpace(cfg.BotID) == "" {
		return ContainerCreateSpec{}, "", "", fmt.Errorf("%w: workspace_id and bot_id are required", compute.ErrInvalidConfig)
	}

	image := strings.TrimSpace(cfg.Image)
	if image == "" {
		image = compute.DefaultSandboxImage
	}
	cpuMillis := cfg.CPUMillis
	if cpuMillis <= 0 {
		cpuMillis = compute.DefaultCPUMillis
	}
	memoryMiB := cfg.MemoryMiB
	if memoryMiB <= 0 {
		memoryMiB = compute.DefaultMemoryMiB
	}
	volumeName := strings.TrimSpace(cfg.VolumeRef)
	if volumeName == "" {
		volumeName = "botbox-home-" + sanitizeIdentifier(cfg.BotID)
	}
	netName := "botbox-net-" + sanitizeIdentifier(cfg.BotID)
	pidsLimit := int64(compute.DefaultPidsLimit)

	secOpts := []string{"no-new-privileges:true"}
	if strings.TrimSpace(seccompProfile) != "" {
		secOpts = append(secOpts, "seccomp="+strings.TrimSpace(seccompProfile))
	}

	env := []string{
		"DISPLAY=:0",
		"VNC_RESOLUTION=1280x800",
		"BOTBOX_HOME_DIR=/home/bot",
		"AGENT_GRPC_ADDR=0.0.0.0:9090",
		"NO_PROXY=localhost,127.0.0.1,::1",
		"no_proxy=localhost,127.0.0.1,::1",
	}
	if strings.TrimSpace(cfg.AgentToken) != "" {
		env = append(env, "BOTBOX_AGENT_TOKEN="+strings.TrimSpace(cfg.AgentToken))
	}
	if proxyURL := strings.TrimSpace(cfg.ProxyURL); proxyURL != "" {
		env = append(env,
			"HTTP_PROXY="+proxyURL,
			"HTTPS_PROXY="+proxyURL,
			"http_proxy="+proxyURL,
			"https_proxy="+proxyURL,
		)
	}
	if caPath := strings.TrimSpace(cfg.CACertPath); caPath != "" {
		env = append(env, "BOTBOX_CA_CERT="+caPath)
	} else if strings.TrimSpace(cfg.ProxyURL) != "" {
		env = append(env, "BOTBOX_CA_CERT=/run/botbox/ca.crt")
	}

	spec := ContainerCreateSpec{
		Image: image,
		Env:   env,
		Labels: map[string]string{
			"botbox.managed":      "true",
			"botbox.workspace_id": cfg.WorkspaceID,
			"botbox.bot_id":       cfg.BotID,
		},
		HostConfig: ContainerHostConfig{
			Privileged:  false,
			CapDrop:     []string{"ALL"},
			SecurityOpt: secOpts,
			NanoCPUs:    int64(cpuMillis) * 1_000_000,
			Memory:      int64(memoryMiB) * 1024 * 1024,
			PidsLimit:   &pidsLimit,
			ShmSize:     compute.DefaultShmSizeBytes,
			Binds:       []string{volumeName + ":/home/bot"},
			NetworkMode: netName,
		},
		NetworkingConfig: ContainerNetworkingConfig{
			EndpointsConfig: map[string]map[string]any{
				netName: {},
			},
		},
	}

	if err := ValidateContainerSpec(spec); err != nil {
		return ContainerCreateSpec{}, "", "", err
	}
	return spec, netName, volumeName, nil
}

// ValidateContainerSpec verifies mandatory non-privileged container invariants.
func ValidateContainerSpec(spec ContainerCreateSpec) error {
	if spec.HostConfig.Privileged {
		return fmt.Errorf("%w: privileged containers are forbidden", compute.ErrSecurityViolation)
	}
	if !slices.Contains(spec.HostConfig.CapDrop, "ALL") {
		return fmt.Errorf("%w: CapDrop must include ALL", compute.ErrSecurityViolation)
	}
	for _, capName := range spec.HostConfig.CapAdd {
		upper := strings.ToUpper(strings.TrimSpace(capName))
		if upper == "NET_ADMIN" || upper == "SYS_ADMIN" || upper == "ALL" {
			return fmt.Errorf("%w: capability %s is forbidden", compute.ErrSecurityViolation, upper)
		}
	}
	if !slices.Contains(spec.HostConfig.SecurityOpt, "no-new-privileges:true") {
		return fmt.Errorf("%w: no-new-privileges:true is required", compute.ErrSecurityViolation)
	}
	for _, bind := range spec.HostConfig.Binds {
		if strings.Contains(strings.ToLower(bind), "docker.sock") {
			return fmt.Errorf("%w: Docker socket bind mounts are forbidden", compute.ErrSecurityViolation)
		}
	}
	for _, entry := range spec.Env {
		key, val, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		upperKey := strings.ToUpper(strings.TrimSpace(key))
		switch upperKey {
		case "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GITHUB_TOKEN", "GITLAB_TOKEN", "GOOGLE_API_KEY":
			trimmed := strings.TrimSpace(val)
			if trimmed != "" && !strings.EqualFold(trimmed, "botbox-placeholder") && !strings.EqualFold(trimmed, "placeholder") {
				return fmt.Errorf("%w: raw secret env var %s is forbidden in sandbox", compute.ErrSecurityViolation, upperKey)
			}
		}
	}
	return nil
}

// CreateEnvironment provisions the named volume, per-bot internal network, and unprivileged container.
func (d *Driver) CreateEnvironment(ctx context.Context, cfg compute.BotConfig) (compute.EnvID, error) {
	spec, netName, volumeName, err := BuildContainerSpec(cfg, d.seccompProfile)
	if err != nil {
		return "", err
	}

	if err := d.ensureVolume(ctx, volumeName, spec.Labels); err != nil {
		return "", err
	}
	if err := d.ensureInternalNetwork(ctx, netName, spec.Labels); err != nil {
		return "", err
	}

	containerName := "botbox-bot-" + sanitizeIdentifier(cfg.BotID)
	var createResp struct {
		ID string `json:"Id"`
	}
	statusCode, err := d.doJSON(ctx, http.MethodPost, "/containers/create?name="+url.QueryEscape(containerName), spec, &createResp)
	if err != nil {
		if statusCode == http.StatusConflict {
			inspected, inspErr := d.inspectContainer(ctx, containerName)
			if inspErr != nil {
				return "", inspErr
			}
			id := compute.EnvID(inspected.ID)
			d.storeToken(id, cfg.AgentToken)
			return id, nil
		}
		return "", err
	}
	if strings.TrimSpace(createResp.ID) == "" {
		return "", errors.New("docker returned empty container id")
	}

	id := compute.EnvID(createResp.ID)
	d.storeToken(id, cfg.AgentToken)
	return id, nil
}

// StartEnvironment starts the provisioned sandbox container.
func (d *Driver) StartEnvironment(ctx context.Context, id compute.EnvID) error {
	if strings.TrimSpace(string(id)) == "" {
		return compute.ErrEnvironmentNotFound
	}
	statusCode, err := d.doJSON(ctx, http.MethodPost, "/containers/"+url.PathEscape(string(id))+"/start", nil, nil)
	if err != nil {
		if statusCode == http.StatusNotFound {
			return compute.ErrEnvironmentNotFound
		}
		if statusCode == http.StatusNotModified {
			return nil
		}
		return err
	}
	return nil
}

// StopEnvironment stops the sandbox container while retaining its /home/bot volume.
func (d *Driver) StopEnvironment(ctx context.Context, id compute.EnvID) error {
	if strings.TrimSpace(string(id)) == "" {
		return compute.ErrEnvironmentNotFound
	}
	statusCode, err := d.doJSON(ctx, http.MethodPost, "/containers/"+url.PathEscape(string(id))+"/stop?t=10", nil, nil)
	if err != nil {
		if statusCode == http.StatusNotFound {
			return compute.ErrEnvironmentNotFound
		}
		if statusCode == http.StatusNotModified {
			return nil
		}
		return err
	}
	return nil
}

// DestroyEnvironment removes the sandbox container cleanly.
func (d *Driver) DestroyEnvironment(ctx context.Context, id compute.EnvID) error {
	if strings.TrimSpace(string(id)) == "" {
		return compute.ErrEnvironmentNotFound
	}
	statusCode, err := d.doJSON(ctx, http.MethodDelete, "/containers/"+url.PathEscape(string(id))+"?force=true", nil, nil)
	if err != nil {
		if statusCode == http.StatusNotFound {
			return compute.ErrEnvironmentNotFound
		}
		return err
	}
	d.mu.Lock()
	delete(d.tokens, id)
	d.mu.Unlock()
	return nil
}

// Status returns the Docker container's mapped compute.EnvStatus.
func (d *Driver) Status(ctx context.Context, id compute.EnvID) (compute.EnvStatus, error) {
	info, err := d.inspectContainer(ctx, string(id))
	if err != nil {
		return "", err
	}
	if info.State.Running {
		return compute.StatusRunning, nil
	}
	switch strings.ToLower(info.State.Status) {
	case "created", "restarting":
		return compute.StatusCreating, nil
	case "dead":
		return compute.StatusError, nil
	default:
		return compute.StatusStopped, nil
	}
}

// Stats queries Docker /stats?stream=false and returns CPU/memory/volume telemetry.
func (d *Driver) Stats(ctx context.Context, id compute.EnvID) (compute.EnvStats, error) {
	if strings.TrimSpace(string(id)) == "" {
		return compute.EnvStats{}, compute.ErrEnvironmentNotFound
	}
	var raw dockerStatsResponse
	statusCode, err := d.doJSON(ctx, http.MethodGet, "/containers/"+url.PathEscape(string(id))+"/stats?stream=false", nil, &raw)
	if err != nil {
		if statusCode == http.StatusNotFound {
			return compute.EnvStats{}, compute.ErrEnvironmentNotFound
		}
		return compute.EnvStats{}, err
	}

	cpuPercent := computeCPUPercent(&raw)
	memUsedMiB := bytesToClampedMiB(raw.MemoryStats.Usage)
	memLimitMiB := bytesToClampedMiB(raw.MemoryStats.Limit)
	if memLimitMiB == 0 {
		memLimitMiB = compute.DefaultMemoryMiB
	}

	return compute.EnvStats{
		CPUPercent:     cpuPercent,
		MemoryUsedMiB:  memUsedMiB,
		MemoryLimitMiB: memLimitMiB,
		VolumeUsedMiB:  0,
		VolumeLimitMiB: compute.DefaultVolumeMiB,
	}, nil
}

// GetVNCStreamURL returns the ws:// URL of websockify on the container's internal IP.
func (d *Driver) GetVNCStreamURL(ctx context.Context, id compute.EnvID) (string, error) {
	ip, err := d.runningContainerIP(ctx, id)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("ws://%s:%d", ip, defaultWebsockPort), nil
}

// ExecCommand executes a command inside /home/bot via the in-sandbox gRPC agent.
func (d *Driver) ExecCommand(ctx context.Context, id compute.EnvID, workdir string, cmd []string) (compute.ExecResult, error) {
	if len(cmd) == 0 {
		return compute.ExecResult{}, fmt.Errorf("%w: command is empty", compute.ErrInvalidConfig)
	}
	client, rpcCtx, closeFn, err := d.agentClient(ctx, id)
	if err != nil {
		return compute.ExecResult{}, err
	}
	defer closeFn()

	resp, err := client.Exec(rpcCtx, &agentv1.ExecRequest{
		Command: cmd[0],
		Args:    cmd[1:],
		Workdir: workdir,
	})
	if err != nil {
		return compute.ExecResult{}, err
	}
	return compute.ExecResult{
		Stdout:   resp.Stdout,
		Stderr:   resp.Stderr,
		ExitCode: int(resp.ExitCode),
	}, nil
}

// ListFiles lists directory contents inside /home/bot via the in-sandbox gRPC agent.
func (d *Driver) ListFiles(ctx context.Context, id compute.EnvID, dirPath string) ([]compute.FileEntry, error) {
	client, rpcCtx, closeFn, err := d.agentClient(ctx, id)
	if err != nil {
		return nil, err
	}
	defer closeFn()

	resp, err := client.ListFiles(rpcCtx, &agentv1.ListFilesRequest{Path: dirPath})
	if err != nil {
		return nil, err
	}
	entries := make([]compute.FileEntry, 0, len(resp.Entries))
	for _, item := range resp.Entries {
		entries = append(entries, compute.FileEntry{
			Name:      item.Name,
			Path:      item.Path,
			IsDir:     item.IsDir,
			SizeBytes: item.SizeBytes,
			UpdatedAt: time.Unix(item.UpdatedAt, 0).UTC(),
		})
	}
	return entries, nil
}

// ReadFile reads file bytes inside /home/bot via the in-sandbox gRPC agent.
func (d *Driver) ReadFile(ctx context.Context, id compute.EnvID, filePath string) ([]byte, error) {
	client, rpcCtx, closeFn, err := d.agentClient(ctx, id)
	if err != nil {
		return nil, err
	}
	defer closeFn()

	resp, err := client.ReadFile(rpcCtx, &agentv1.ReadFileRequest{Path: filePath})
	if err != nil {
		return nil, err
	}
	return resp.Content, nil
}

// WriteFile writes file bytes inside /home/bot via the in-sandbox gRPC agent.
func (d *Driver) WriteFile(ctx context.Context, id compute.EnvID, filePath string, content []byte) error {
	client, rpcCtx, closeFn, err := d.agentClient(ctx, id)
	if err != nil {
		return err
	}
	defer closeFn()

	_, err = client.WriteFile(rpcCtx, &agentv1.WriteFileRequest{
		Path:    filePath,
		Content: content,
	})
	return err
}

// Reconcile queries Docker for all botbox.managed containers and reports which known IDs
// are missing or not running.
func (d *Driver) Reconcile(ctx context.Context, known []compute.EnvID) ([]compute.EnvID, error) {
	filtersJSON, err := json.Marshal(map[string][]string{
		"label": {"botbox.managed=true"},
	})
	if err != nil {
		return nil, err
	}

	var containers []struct {
		ID    string `json:"Id"`
		State string `json:"State"`
	}
	_, err = d.doJSON(ctx, http.MethodGet, "/containers/json?all=true&filters="+url.QueryEscape(string(filtersJSON)), nil, &containers)
	if err != nil {
		return nil, err
	}

	running := make(map[string]bool, len(containers))
	for _, c := range containers {
		if strings.EqualFold(c.State, "running") {
			running[c.ID] = true
		}
	}

	var missing []compute.EnvID
	for _, id := range known {
		if !running[string(id)] {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

func (d *Driver) ensureVolume(ctx context.Context, name string, labels map[string]string) error {
	payload := map[string]any{
		"Name":   name,
		"Labels": labels,
	}
	_, err := d.doJSON(ctx, http.MethodPost, "/volumes/create", payload, nil)
	return err
}

func (d *Driver) ensureInternalNetwork(ctx context.Context, name string, labels map[string]string) error {
	statusCode, err := d.doJSON(ctx, http.MethodGet, "/networks/"+url.PathEscape(name), nil, nil)
	if err == nil {
		return nil
	}
	if statusCode != http.StatusNotFound {
		return err
	}

	payload := map[string]any{
		"Name":     name,
		"Driver":   "bridge",
		"Internal": true,
		"Labels":   labels,
	}
	code, createErr := d.doJSON(ctx, http.MethodPost, "/networks/create", payload, nil)
	if createErr != nil && code == http.StatusConflict {
		return nil
	}
	return createErr
}

type containerInspectResponse struct {
	ID    string `json:"Id"`
	State struct {
		Status  string `json:"Status"`
		Running bool   `json:"Running"`
	} `json:"State"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

func (d *Driver) inspectContainer(ctx context.Context, idOrName string) (*containerInspectResponse, error) {
	if strings.TrimSpace(idOrName) == "" {
		return nil, compute.ErrEnvironmentNotFound
	}
	var out containerInspectResponse
	statusCode, err := d.doJSON(ctx, http.MethodGet, "/containers/"+url.PathEscape(idOrName)+"/json", nil, &out)
	if err != nil {
		if statusCode == http.StatusNotFound {
			return nil, compute.ErrEnvironmentNotFound
		}
		return nil, err
	}
	return &out, nil
}

func (d *Driver) runningContainerIP(ctx context.Context, id compute.EnvID) (string, error) {
	info, err := d.inspectContainer(ctx, string(id))
	if err != nil {
		return "", err
	}
	if !info.State.Running {
		return "", compute.ErrEnvironmentNotRunning
	}
	for _, endpoint := range info.NetworkSettings.Networks {
		if strings.TrimSpace(endpoint.IPAddress) != "" {
			return endpoint.IPAddress, nil
		}
	}
	return "", errors.New("running container has no assigned internal network IP")
}

func (d *Driver) agentClient(ctx context.Context, id compute.EnvID) (agentv1.AgentServiceClient, context.Context, func(), error) {
	ip, err := d.runningContainerIP(ctx, id)
	if err != nil {
		return nil, nil, nil, err
	}
	addr := fmt.Sprintf("%s:%d", ip, defaultAgentGRPCPort)

	var conn *grpc.ClientConn
	if d.agentDialer != nil {
		conn, err = d.agentDialer(ctx, addr)
	} else {
		conn, err = grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("dial sandbox agent %s: %w", addr, err)
	}

	d.mu.RLock()
	token := d.tokens[id]
	d.mu.RUnlock()

	rpcCtx := ctx
	if token != "" {
		rpcCtx = metadata.NewOutgoingContext(ctx, metadata.Pairs(agent.AgentTokenMetadataKey, token))
	}
	closeFn := func() { _ = conn.Close() }
	return agentv1.NewAgentServiceClient(conn), rpcCtx, closeFn, nil
}

func (d *Driver) storeToken(id compute.EnvID, token string) {
	if strings.TrimSpace(token) == "" {
		return
	}
	d.mu.Lock()
	d.tokens[id] = strings.TrimSpace(token)
	d.mu.Unlock()
}

func (d *Driver) doJSON(ctx context.Context, method, path string, reqBody any, dst any) (int, error) {
	var bodyReader io.Reader
	if reqBody != nil {
		raw, err := json.Marshal(reqBody)
		if err != nil {
			return 0, fmt.Errorf("marshal docker request: %w", err)
		}
		bodyReader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, d.baseURL+path, bodyReader)
	if err != nil {
		return 0, fmt.Errorf("build docker request: %w", err)
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("docker engine %s %s: %w", method, path, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	rawResp, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("read docker response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errPayload struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(rawResp, &errPayload)
		if errPayload.Message != "" {
			return resp.StatusCode, fmt.Errorf("docker API HTTP %d: %s", resp.StatusCode, errPayload.Message)
		}
		return resp.StatusCode, fmt.Errorf("docker API HTTP %d", resp.StatusCode)
	}

	if dst != nil && len(bytes.TrimSpace(rawResp)) > 0 {
		if err := json.Unmarshal(rawResp, dst); err != nil {
			return resp.StatusCode, fmt.Errorf("decode docker response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

type dockerStatsResponse struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage  uint64   `json:"total_usage"`
			PercpuUsage []uint64 `json:"percpu_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs     uint64 `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64 `json:"usage"`
		Limit uint64 `json:"limit"`
	} `json:"memory_stats"`
}

func computeCPUPercent(stats *dockerStatsResponse) float64 {
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(stats.CPUStats.SystemCPUUsage - stats.PreCPUStats.SystemCPUUsage)
	if systemDelta <= 0 || cpuDelta <= 0 {
		return 0
	}
	cpus := float64(stats.CPUStats.OnlineCPUs)
	if cpus == 0 {
		cpus = float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if cpus == 0 {
		cpus = 1
	}
	return (cpuDelta / systemDelta) * cpus * 100.0
}

func bytesToClampedMiB(b uint64) int {
	mib := b / (1024 * 1024)
	if mib > uint64(1<<30) {
		return 1 << 30
	}
	//nolint:gosec // clamped above to <= 1<<30 which always fits in int
	return int(mib)
}

func sanitizeIdentifier(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "bot"
	}
	return b.String()
}
