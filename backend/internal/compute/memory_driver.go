// SPDX-License-Identifier: AGPL-3.0-only

package compute

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/dunkeltech/botbox/backend/internal/agent"
	agentv1 "github.com/dunkeltech/botbox/backend/internal/gen/proto/agent/v1"
)

type memoryEnv struct {
	cfg     BotConfig
	status  EnvStatus
	homeDir string
	agent   *agent.Server
	vncSrv  *mockRFBServer
}

// MemoryDriver is a deterministic in-process ComputeDriver for local development
// and automated integration tests without requiring a live Docker engine.
type MemoryDriver struct {
	mu      sync.Mutex
	rootDir string
	ownsDir bool
	envs    map[EnvID]*memoryEnv
}

// MemoryDriverOptions configures optional root storage for MemoryDriver.
type MemoryDriverOptions struct {
	RootDir string
}

// NewMemoryDriver creates an isolated in-memory compute driver rooted at a temp directory.
func NewMemoryDriver(opts ...MemoryDriverOptions) (*MemoryDriver, error) {
	var opt MemoryDriverOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	rootDir := strings.TrimSpace(opt.RootDir)
	ownsDir := false
	if rootDir == "" {
		dir, err := os.MkdirTemp("", "botbox-memory-driver-*")
		if err != nil {
			return nil, fmt.Errorf("create memory driver temp dir: %w", err)
		}
		rootDir = dir
		ownsDir = true
	} else if err := os.MkdirAll(rootDir, 0o750); err != nil {
		return nil, fmt.Errorf("create memory driver root dir: %w", err)
	}

	return &MemoryDriver{
		rootDir: rootDir,
		ownsDir: ownsDir,
		envs:    make(map[EnvID]*memoryEnv),
	}, nil
}

// Close releases active listeners and removes the temporary root directory if owned.
func (d *MemoryDriver) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, env := range d.envs {
		if env.vncSrv != nil {
			env.vncSrv.Close(context.Background())
			env.vncSrv = nil
		}
	}
	if d.ownsDir && d.rootDir != "" {
		_ = os.RemoveAll(d.rootDir)
	}
}

// CreateEnvironment provisions a stopped sandbox record with a persistent home directory.
func (d *MemoryDriver) CreateEnvironment(ctx context.Context, cfg BotConfig) (EnvID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(cfg.BotID) == "" || strings.TrimSpace(cfg.WorkspaceID) == "" {
		return "", ErrInvalidConfig
	}

	id := EnvID("fake-" + cfg.BotID)
	d.mu.Lock()
	defer d.mu.Unlock()

	if existing, ok := d.envs[id]; ok {
		if cfg.Image != "" {
			existing.cfg.Image = cfg.Image
		}
		if cfg.CPUMillis > 0 {
			existing.cfg.CPUMillis = cfg.CPUMillis
		}
		if cfg.MemoryMiB > 0 {
			existing.cfg.MemoryMiB = cfg.MemoryMiB
		}
		return id, nil
	}

	homeDir := filepath.Join(d.rootDir, "home-"+sanitizeName(cfg.BotID))
	if err := os.MkdirAll(homeDir, 0o750); err != nil {
		return "", fmt.Errorf("create bot home dir: %w", err)
	}
	_ = os.MkdirAll(filepath.Join(homeDir, "workspace"), 0o750)

	if cfg.Image == "" {
		cfg.Image = DefaultSandboxImage
	}
	if cfg.CPUMillis <= 0 {
		cfg.CPUMillis = DefaultCPUMillis
	}
	if cfg.MemoryMiB <= 0 {
		cfg.MemoryMiB = DefaultMemoryMiB
	}
	if cfg.VolumeRef == "" {
		cfg.VolumeRef = "botbox-home-" + cfg.BotID
	}

	d.envs[id] = &memoryEnv{
		cfg:     cfg,
		status:  StatusStopped,
		homeDir: homeDir,
		agent: agent.NewServerWithOptions(agent.ServerOptions{
			Hostname: "bot-" + sanitizeName(cfg.BotID),
			HomeDir:  homeDir,
		}),
	}
	return id, nil
}

// StartEnvironment transitions the sandbox to StatusRunning.
func (d *MemoryDriver) StartEnvironment(ctx context.Context, id EnvID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	env, ok := d.envs[id]
	if !ok {
		return ErrEnvironmentNotFound
	}
	env.status = StatusRunning
	return nil
}

// StopEnvironment stops the sandbox while preserving its persistent home directory.
func (d *MemoryDriver) StopEnvironment(ctx context.Context, id EnvID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	env, ok := d.envs[id]
	if !ok {
		return ErrEnvironmentNotFound
	}
	env.status = StatusStopped
	if env.vncSrv != nil {
		env.vncSrv.Close(ctx)
		env.vncSrv = nil
	}
	return nil
}

// DestroyEnvironment removes the sandbox and its backing directory.
func (d *MemoryDriver) DestroyEnvironment(ctx context.Context, id EnvID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	env, ok := d.envs[id]
	if ok {
		if env.vncSrv != nil {
			env.vncSrv.Close(ctx)
		}
		delete(d.envs, id)
	}
	d.mu.Unlock()

	if !ok {
		return ErrEnvironmentNotFound
	}
	if env.homeDir != "" {
		_ = os.RemoveAll(env.homeDir)
	}
	return nil
}

// Status returns the current sandbox lifecycle status.
func (d *MemoryDriver) Status(ctx context.Context, id EnvID) (EnvStatus, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	env, ok := d.envs[id]
	if !ok {
		return "", ErrEnvironmentNotFound
	}
	return env.status, nil
}

// Stats returns deterministic CPU, memory, and volume utilization metrics.
func (d *MemoryDriver) Stats(ctx context.Context, id EnvID) (EnvStats, error) {
	if err := ctx.Err(); err != nil {
		return EnvStats{}, err
	}
	d.mu.Lock()
	env, ok := d.envs[id]
	if !ok {
		d.mu.Unlock()
		return EnvStats{}, ErrEnvironmentNotFound
	}
	cfg := env.cfg
	status := env.status
	homeDir := env.homeDir
	d.mu.Unlock()

	volUsedBytes := dirSizeBytes(homeDir)
	volUsedMiB := int(volUsedBytes / (1024 * 1024))
	if volUsedBytes > 0 && volUsedMiB == 0 {
		volUsedMiB = 1
	}

	if status != StatusRunning {
		return EnvStats{
			CPUPercent:     0,
			MemoryUsedMiB:  0,
			MemoryLimitMiB: cfg.MemoryMiB,
			VolumeUsedMiB:  volUsedMiB,
			VolumeLimitMiB: DefaultVolumeMiB,
		}, nil
	}

	return EnvStats{
		CPUPercent:     18.5,
		MemoryUsedMiB:  min(420, cfg.MemoryMiB),
		MemoryLimitMiB: cfg.MemoryMiB,
		VolumeUsedMiB:  volUsedMiB,
		VolumeLimitMiB: DefaultVolumeMiB,
	}, nil
}

// GetVNCStreamURL returns an active local ws:// RFB endpoint for a running sandbox.
func (d *MemoryDriver) GetVNCStreamURL(ctx context.Context, id EnvID) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	env, ok := d.envs[id]
	if !ok {
		return "", ErrEnvironmentNotFound
	}
	if env.status != StatusRunning {
		return "", ErrEnvironmentNotRunning
	}
	if env.vncSrv == nil {
		srv, err := startMockRFBServer(ctx)
		if err != nil {
			return "", err
		}
		env.vncSrv = srv
	}
	return env.vncSrv.URL(), nil
}

// ExecCommand executes cmd inside the sandbox home directory via the embedded agent.
func (d *MemoryDriver) ExecCommand(ctx context.Context, id EnvID, workdir string, cmd []string) (ExecResult, error) {
	if len(cmd) == 0 {
		return ExecResult{}, fmt.Errorf("%w: empty command", ErrInvalidConfig)
	}
	env, err := d.runningEnv(id)
	if err != nil {
		return ExecResult{}, err
	}
	resp, err := env.agent.Exec(ctx, &agentv1.ExecRequest{
		Command: cmd[0],
		Args:    cmd[1:],
		Workdir: workdir,
	})
	if err != nil {
		return ExecResult{}, err
	}
	return ExecResult{
		Stdout:   resp.Stdout,
		Stderr:   resp.Stderr,
		ExitCode: int(resp.ExitCode),
	}, nil
}

// ListFiles lists directory contents strictly within /home/bot.
func (d *MemoryDriver) ListFiles(ctx context.Context, id EnvID, dirPath string) ([]FileEntry, error) {
	env, err := d.runningEnv(id)
	if err != nil {
		return nil, err
	}
	resp, err := env.agent.ListFiles(ctx, &agentv1.ListFilesRequest{Path: dirPath})
	if err != nil {
		return nil, err
	}
	entries := make([]FileEntry, 0, len(resp.Entries))
	for _, entry := range resp.Entries {
		entries = append(entries, FileEntry{
			Name:      entry.Name,
			Path:      entry.Path,
			IsDir:     entry.IsDir,
			SizeBytes: entry.SizeBytes,
			UpdatedAt: time.Unix(entry.UpdatedAt, 0).UTC(),
		})
	}
	return entries, nil
}

// ReadFile reads file bytes strictly within /home/bot.
func (d *MemoryDriver) ReadFile(ctx context.Context, id EnvID, filePath string) ([]byte, error) {
	env, err := d.runningEnv(id)
	if err != nil {
		return nil, err
	}
	resp, err := env.agent.ReadFile(ctx, &agentv1.ReadFileRequest{Path: filePath})
	if err != nil {
		return nil, err
	}
	return resp.Content, nil
}

// WriteFile writes file bytes strictly within /home/bot.
func (d *MemoryDriver) WriteFile(ctx context.Context, id EnvID, filePath string, content []byte) error {
	env, err := d.runningEnv(id)
	if err != nil {
		return err
	}
	_, err = env.agent.WriteFile(ctx, &agentv1.WriteFileRequest{
		Path:    filePath,
		Content: content,
	})
	return err
}

// Reconcile returns any known environment IDs missing or no longer active in memory.
func (d *MemoryDriver) Reconcile(ctx context.Context, known []EnvID) ([]EnvID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	var missing []EnvID
	for _, id := range known {
		env, ok := d.envs[id]
		if !ok || env.status != StatusRunning {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

func (d *MemoryDriver) runningEnv(id EnvID) (*memoryEnv, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	env, ok := d.envs[id]
	if !ok {
		return nil, ErrEnvironmentNotFound
	}
	if env.status != StatusRunning {
		return nil, ErrEnvironmentNotRunning
	}
	return env, nil
}

func sanitizeName(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "bot"
	}
	return b.String()
}

func dirSizeBytes(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, infoErr := d.Info(); infoErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

type mockRFBServer struct {
	lis net.Listener
	srv *http.Server
}

func startMockRFBServer(ctx context.Context) (*mockRFBServer, error) {
	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", serveMockRFB)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		_ = srv.Serve(lis)
	}()
	return &mockRFBServer{lis: lis, srv: srv}, nil
}

func (m *mockRFBServer) URL() string {
	return "ws://" + m.lis.Addr().String()
}

func (m *mockRFBServer) Close(ctx context.Context) {
	shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	_ = m.srv.Shutdown(shutCtx)
	_ = m.lis.Close()
}

func serveMockRFB(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		Subprotocols:       []string{"binary"},
	})
	if err != nil {
		return
	}
	defer func() {
		_ = conn.CloseNow()
	}()

	ctx := r.Context()
	// Minimal RFB 3.8 handshake with SecurityType None (1) and 1280x800 desktop.
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("RFB 003.008\n")); err != nil {
		return
	}
	if _, _, err := conn.Read(ctx); err != nil {
		return
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{1, 1}); err != nil {
		return
	}
	if _, _, err := conn.Read(ctx); err != nil {
		return
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{0, 0, 0, 0}); err != nil {
		return
	}
	if _, _, err := conn.Read(ctx); err != nil {
		return
	}

	serverInit := make([]byte, 24+6)
	binary.BigEndian.PutUint16(serverInit[0:2], 1280)
	binary.BigEndian.PutUint16(serverInit[2:4], 800)
	serverInit[4] = 32 // bits-per-pixel
	serverInit[5] = 24 // depth
	serverInit[6] = 0  // little-endian
	serverInit[7] = 1  // true-color
	binary.BigEndian.PutUint16(serverInit[8:10], 255)
	binary.BigEndian.PutUint16(serverInit[10:12], 255)
	binary.BigEndian.PutUint16(serverInit[12:14], 255)
	serverInit[14] = 16
	serverInit[15] = 8
	serverInit[16] = 0
	binary.BigEndian.PutUint32(serverInit[20:24], 6)
	copy(serverInit[24:], []byte("botbox"))
	if err := conn.Write(ctx, websocket.MessageBinary, serverInit); err != nil {
		return
	}

	for {
		_, _, err := conn.Read(ctx)
		if err != nil && !errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			return
		}
	}
}
