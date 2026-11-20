// SPDX-License-Identifier: AGPL-3.0-only

// Package compute defines the sandbox compute driver interface and implementations
// used to isolate, run, inspect, and control bot environments.
package compute

import (
	"context"
	"errors"
	"net"
	"time"
)

// EnvID uniquely identifies a provisioned bot sandbox environment in a driver.
type EnvID string

// EnvStatus represents the lifecycle state of a sandbox environment.
type EnvStatus string

const (
	// StatusCreating indicates the container or pod is being provisioned.
	StatusCreating EnvStatus = "creating"
	// StatusRunning indicates the sandbox desktop and agent are active.
	StatusRunning EnvStatus = "running"
	// StatusStopped indicates the sandbox is stopped with its home volume preserved.
	StatusStopped EnvStatus = "stopped"
	// StatusError indicates provisioning or runtime failure.
	StatusError EnvStatus = "error"
)

// Default sandbox settings use the resource limits from ADR-0002.
const (
	DefaultDriverDocker = "docker"
	DefaultDriverFake   = "fake"
	DefaultSandboxImage = "botbox/sandbox:dev"
	DefaultCPUMillis    = 2000
	DefaultMemoryMiB    = 2048
	DefaultVolumeMiB    = 10240
	DefaultPidsLimit    = 512
	DefaultShmSizeBytes = 512 * 1024 * 1024
)

var (
	// ErrEnvironmentNotFound indicates the target sandbox does not exist in the driver.
	ErrEnvironmentNotFound = errors.New("sandbox environment not found")
	// ErrEnvironmentNotRunning indicates the operation requires a running sandbox.
	ErrEnvironmentNotRunning = errors.New("sandbox environment is not running")
	// ErrInvalidConfig indicates missing or invalid sandbox configuration fields.
	ErrInvalidConfig = errors.New("invalid sandbox configuration")
	// ErrSecurityViolation indicates an attempted violation of sandbox isolation invariants.
	ErrSecurityViolation = errors.New("sandbox security invariant violation")
)

// BotConfig specifies provisioning configuration and resource limits for a bot sandbox.
type BotConfig struct {
	WorkspaceID string
	BotID       string
	Image       string
	CPUMillis   int
	MemoryMiB   int
	VolumeRef   string
	AgentToken  string
	ProxyURL    string
	CACertPath  string
}

// EnvStats reports live CPU, memory, and persistent home volume utilization.
type EnvStats struct {
	CPUPercent     float64
	MemoryUsedMiB  int
	MemoryLimitMiB int
	VolumeUsedMiB  int
	VolumeLimitMiB int
}

// ExecResult captures bounded command output and process exit code.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// FileEntry describes a file or directory inside a bot's /home/bot workspace.
type FileEntry struct {
	Name      string
	Path      string
	IsDir     bool
	SizeBytes int64
	UpdatedAt time.Time
}

// Driver provisions and controls isolated bot desktop sandboxes.
type Driver interface {
	CreateEnvironment(ctx context.Context, cfg BotConfig) (EnvID, error)
	StartEnvironment(ctx context.Context, id EnvID) error
	StopEnvironment(ctx context.Context, id EnvID) error
	DestroyEnvironment(ctx context.Context, id EnvID) error
	Status(ctx context.Context, id EnvID) (EnvStatus, error)
	Stats(ctx context.Context, id EnvID) (EnvStats, error)
	GetVNCStreamURL(ctx context.Context, id EnvID) (string, error)
	ExecCommand(ctx context.Context, id EnvID, workdir string, cmd []string) (ExecResult, error)
	ListFiles(ctx context.Context, id EnvID, dirPath string) ([]FileEntry, error)
	ReadFile(ctx context.Context, id EnvID, filePath string) ([]byte, error)
	WriteFile(ctx context.Context, id EnvID, filePath string, content []byte) error
	Reconcile(ctx context.Context, known []EnvID) (missing []EnvID, err error)
}

// VNCDialer opens a byte stream to websockify inside a running sandbox.
// Docker Desktop gives the container a bridge address that this Go process
// cannot route (EHOSTUNREACH, ADR-0013). The desktop relay uses the stream
// instead of dialing that address. The stream is the websockify TCP port,
// before the WebSocket handshake.
type VNCDialer interface {
	DialVNC(ctx context.Context, id EnvID) (net.Conn, error)
}
