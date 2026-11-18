// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/dunkeltech/botbox/backend/internal/gen/proto/agent/v1"
)

const (
	// DefaultHomeDir is the sandbox home directory confining agent file and exec operations.
	DefaultHomeDir = "/home/bot"
	// MaxFileBytes bounds ReadFile and WriteFile payloads to 1 MiB.
	MaxFileBytes = 1 << 20
	// MaxExecOutputBytes bounds captured stdout and stderr per Exec invocation.
	MaxExecOutputBytes = 1 << 20
	// DefaultExecTimeout bounds Exec calls when no explicit timeout_ms is supplied.
	DefaultExecTimeout = 30 * time.Second
	// MaxExecTimeout caps user-requested Exec timeouts.
	MaxExecTimeout = 5 * time.Minute
)

// ResolveHomePath resolves requestedPath strictly within homeDir and rejects any
// path traversal ("..") or symlink target escaping outside homeDir.
func ResolveHomePath(homeDir, requestedPath string) (string, error) {
	if strings.TrimSpace(homeDir) == "" {
		homeDir = DefaultHomeDir
	}
	homeAbs, err := filepath.Abs(filepath.Clean(homeDir))
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	realHome := homeAbs
	if evaluated, evalErr := filepath.EvalSymlinks(homeAbs); evalErr == nil {
		realHome = evaluated
	} else if !errors.Is(evalErr, fs.ErrNotExist) {
		return "", fmt.Errorf("evaluate home directory symlinks: %w", evalErr)
	}

	rel, err := normalizeRelativeToHome(homeAbs, realHome, requestedPath)
	if err != nil {
		return "", err
	}

	candidate := filepath.Clean(filepath.Join(realHome, rel))
	if !isWithinBase(realHome, candidate) {
		return "", status.Error(codes.PermissionDenied, "path escapes sandbox home directory")
	}

	// Verify symlinks on the target or nearest existing ancestor so symlinks cannot
	// point writes or reads outside /home/bot.
	if err := verifySymlinksWithinHome(realHome, candidate); err != nil {
		return "", err
	}

	return candidate, nil
}

// ToVirtualHomePath converts an absolute path inside homeDir to a canonical "/home/bot" path.
func ToVirtualHomePath(homeDir, resolvedPath string) string {
	if strings.TrimSpace(homeDir) == "" {
		homeDir = DefaultHomeDir
	}
	homeAbs, err := filepath.Abs(filepath.Clean(homeDir))
	if err != nil {
		return DefaultHomeDir
	}
	realHome := homeAbs
	if evaluated, evalErr := filepath.EvalSymlinks(homeAbs); evalErr == nil {
		realHome = evaluated
	}
	rel, err := filepath.Rel(realHome, resolvedPath)
	if err != nil || rel == "." {
		return DefaultHomeDir
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return DefaultHomeDir
	}
	return DefaultHomeDir + "/" + filepath.ToSlash(rel)
}

func normalizeRelativeToHome(homeAbs, realHome, requestedPath string) (string, error) {
	trimmed := strings.TrimSpace(requestedPath)
	if trimmed == "" || trimmed == "." {
		return ".", nil
	}

	if filepath.IsAbs(trimmed) {
		cleanedAbs := filepath.Clean(trimmed)
		// Map virtual "/home/bot" prefixes into the active sandbox homeDir (supports temp homes in tests).
		if cleanedAbs == DefaultHomeDir {
			return ".", nil
		}
		if strings.HasPrefix(cleanedAbs, DefaultHomeDir+"/") {
			return strings.TrimPrefix(cleanedAbs, DefaultHomeDir+"/"), nil
		}
		if isWithinBase(realHome, cleanedAbs) {
			rel, err := filepath.Rel(realHome, cleanedAbs)
			if err != nil {
				return "", status.Error(codes.PermissionDenied, "invalid sandbox path")
			}
			return rel, nil
		}
		if isWithinBase(homeAbs, cleanedAbs) {
			rel, err := filepath.Rel(homeAbs, cleanedAbs)
			if err != nil {
				return "", status.Error(codes.PermissionDenied, "invalid sandbox path")
			}
			return rel, nil
		}
		return "", status.Error(codes.PermissionDenied, "absolute path outside sandbox home directory")
	}

	cleanedRel := filepath.Clean(trimmed)
	if cleanedRel == ".." || strings.HasPrefix(cleanedRel, ".."+string(filepath.Separator)) {
		return "", status.Error(codes.PermissionDenied, "relative path escapes sandbox home directory")
	}
	return cleanedRel, nil
}

func isWithinBase(baseDir, targetPath string) bool {
	rel, err := filepath.Rel(baseDir, targetPath)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func verifySymlinksWithinHome(realHome, candidate string) error {
	// Reject dangling or external symlinks even if the final file does not exist yet.
	if info, err := os.Lstat(candidate); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			evaluated, evalErr := filepath.EvalSymlinks(candidate)
			if evalErr != nil || !isWithinBase(realHome, evaluated) {
				return status.Error(codes.PermissionDenied, "symlink target escapes sandbox home directory")
			}
		}
	}

	probe := candidate
	for {
		evaluated, err := filepath.EvalSymlinks(probe)
		if err == nil {
			if !isWithinBase(realHome, evaluated) {
				return status.Error(codes.PermissionDenied, "path escapes sandbox home directory via symlink")
			}
			return nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return status.Error(codes.PermissionDenied, "failed to verify path containment")
		}
		parent := filepath.Dir(probe)
		if parent == probe || !isWithinBase(realHome, parent) {
			return status.Error(codes.PermissionDenied, "path escapes sandbox home directory")
		}
		probe = parent
	}
}

// GetInfo reports sandbox display size, agent uptime, version, and home directory.
func (s *Server) GetInfo(ctx context.Context, req *agentv1.GetInfoRequest) (*agentv1.GetInfoResponse, error) {
	if err := checkRPCContext(ctx, "get_info"); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "get_info request is required")
	}

	uptime := int64(s.now().Sub(s.startedAt).Seconds())
	if uptime < 0 {
		uptime = 0
	}

	return &agentv1.GetInfoResponse{
		DisplayWidth:  DesktopWidth,
		DisplayHeight: DesktopHeight,
		UptimeSeconds: uptime,
		Version:       s.version,
		HomeDir:       DefaultHomeDir,
	}, nil
}

// Exec executes a bounded command inside workdir (defaulting to the sandbox home directory).
func (s *Server) Exec(ctx context.Context, req *agentv1.ExecRequest) (*agentv1.ExecResponse, error) {
	if err := checkRPCContext(ctx, "exec"); err != nil {
		return nil, err
	}
	if req == nil || strings.TrimSpace(req.Command) == "" {
		return nil, status.Error(codes.InvalidArgument, "command is required")
	}

	workdir, err := ResolveHomePath(s.homeDir, req.Workdir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(workdir)
	if err != nil || !info.IsDir() {
		return nil, status.Error(codes.InvalidArgument, "workdir must be an existing directory inside /home/bot")
	}

	timeout := DefaultExecTimeout
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
		if timeout > MaxExecTimeout {
			timeout = MaxExecTimeout
		}
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	//nolint:gosec // intentional sandbox command execution strictly inside resolved workdir
	cmd := exec.CommandContext(execCtx, strings.TrimSpace(req.Command), req.Args...)
	cmd.Dir = workdir
	cmd.Env = sanitizedSandboxEnv(workdir)

	stdoutBuf := &cappedBuffer{limit: MaxExecOutputBytes}
	stderrBuf := &cappedBuffer{limit: MaxExecOutputBytes}
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf

	runErr := cmd.Run()
	if execCtx.Err() != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			return nil, status.Error(codes.DeadlineExceeded, "command execution timed out")
		}
		return nil, status.Error(codes.Canceled, "command execution canceled")
	}

	exitCode := int32(0)
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			//nolint:gosec // POSIX process exit codes fit in int32
			exitCode = int32(exitErr.ExitCode())
		} else {
			return nil, status.Errorf(codes.InvalidArgument, "failed to start command: %v", runErr)
		}
	}

	return &agentv1.ExecResponse{
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		ExitCode: exitCode,
	}, nil
}

// ListFiles lists directory contents strictly inside /home/bot.
func (s *Server) ListFiles(ctx context.Context, req *agentv1.ListFilesRequest) (*agentv1.ListFilesResponse, error) {
	if err := checkRPCContext(ctx, "list_files"); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "list_files request is required")
	}

	resolvedDir, err := ResolveHomePath(s.homeDir, req.Path)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(resolvedDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, status.Error(codes.NotFound, "directory not found")
		}
		return nil, status.Errorf(codes.InvalidArgument, "cannot list directory: %v", err)
	}

	out := make([]*agentv1.FileEntry, 0, len(entries))
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		childAbs := filepath.Join(resolvedDir, entry.Name())
		out = append(out, &agentv1.FileEntry{
			Name:      entry.Name(),
			Path:      ToVirtualHomePath(s.homeDir, childAbs),
			IsDir:     entry.IsDir(),
			SizeBytes: info.Size(),
			UpdatedAt: info.ModTime().UTC().Unix(),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})

	return &agentv1.ListFilesResponse{
		Path:    ToVirtualHomePath(s.homeDir, resolvedDir),
		Entries: out,
	}, nil
}

// ReadFile reads up to MaxFileBytes from a file inside /home/bot.
func (s *Server) ReadFile(ctx context.Context, req *agentv1.ReadFileRequest) (*agentv1.ReadFileResponse, error) {
	if err := checkRPCContext(ctx, "read_file"); err != nil {
		return nil, err
	}
	if req == nil || strings.TrimSpace(req.Path) == "" {
		return nil, status.Error(codes.InvalidArgument, "file path is required")
	}

	resolvedPath, err := ResolveHomePath(s.homeDir, req.Path)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, status.Error(codes.NotFound, "file not found")
		}
		return nil, status.Errorf(codes.Internal, "stat file: %v", err)
	}
	if info.IsDir() {
		return nil, status.Error(codes.InvalidArgument, "cannot read a directory")
	}
	if info.Size() > MaxFileBytes {
		return nil, status.Errorf(codes.ResourceExhausted, "file exceeds %d bytes", MaxFileBytes)
	}

	//nolint:gosec // resolvedPath is strictly confined under sandbox homeDir by ResolveHomePath
	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read file: %v", err)
	}

	return &agentv1.ReadFileResponse{
		Path:      ToVirtualHomePath(s.homeDir, resolvedPath),
		Content:   data,
		SizeBytes: int64(len(data)),
	}, nil
}

// WriteFile creates or overwrites a file strictly inside /home/bot.
func (s *Server) WriteFile(ctx context.Context, req *agentv1.WriteFileRequest) (*agentv1.WriteFileResponse, error) {
	if err := checkRPCContext(ctx, "write_file"); err != nil {
		return nil, err
	}
	if req == nil || strings.TrimSpace(req.Path) == "" {
		return nil, status.Error(codes.InvalidArgument, "file path is required")
	}
	if len(req.Content) > MaxFileBytes {
		return nil, status.Errorf(codes.InvalidArgument, "file content exceeds %d bytes", MaxFileBytes)
	}

	resolvedPath, err := ResolveHomePath(s.homeDir, req.Path)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(resolvedPath), 0o750); err != nil {
		return nil, status.Errorf(codes.Internal, "create parent directory: %v", err)
	}
	if err := os.WriteFile(resolvedPath, req.Content, 0o600); err != nil {
		return nil, status.Errorf(codes.Internal, "write file: %v", err)
	}

	return &agentv1.WriteFileResponse{
		Path:      ToVirtualHomePath(s.homeDir, resolvedPath),
		SizeBytes: int64(len(req.Content)),
	}, nil
}

func sanitizedSandboxEnv(homeDir string) []string {
	pathVal := os.Getenv("PATH")
	if pathVal == "" {
		pathVal = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	display := os.Getenv("DISPLAY")
	if display == "" {
		display = ":0"
	}
	return []string{
		"HOME=" + homeDir,
		"PATH=" + pathVal,
		"DISPLAY=" + display,
		"TERM=xterm-256color",
	}
}

type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.buf.Len() < c.limit {
		remaining := c.limit - c.buf.Len()
		if len(p) > remaining {
			_, _ = c.buf.Write(p[:remaining])
			c.truncated = true
		} else {
			_, _ = c.buf.Write(p)
		}
	} else {
		c.truncated = true
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	return c.buf.String()
}
