// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	agentv1 "github.com/dunkeltech/botbox/backend/internal/gen/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RunTerminal executes a bot command in the work sandbox. It binds the
// process group to both the turn's RPC and the desktop takeover gate.
func (s *Server) RunTerminal(stream grpc.BidiStreamingServer[agentv1.RunTerminalRequest, agentv1.RunTerminalResponse]) error {
	frame, err := stream.Recv()
	if err != nil {
		return err
	}
	start := frame.GetStart()
	if start == nil || strings.TrimSpace(start.Command) == "" || len(start.Command) > 4096 ||
		len(start.Args) > 256 || start.TimeoutMs < 0 || start.TimeoutMs > MaxExecTimeout.Milliseconds() {
		return status.Error(codes.InvalidArgument, "invalid terminal command")
	}
	workdir, err := ResolveHomePath(s.homeDir, start.Workdir)
	if err != nil {
		return err
	}
	if info, err := os.Stat(workdir); err != nil || !info.IsDir() {
		return status.Error(codes.InvalidArgument, "invalid terminal working directory")
	}
	if s.desktop == nil || s.desktop.control == nil {
		return status.Error(codes.FailedPrecondition, "desktop control unavailable")
	}
	parent, release, err := s.desktop.control.Bind(stream.Context())
	if err != nil {
		return status.Error(codes.FailedPrecondition, "desktop control unavailable")
	}
	defer release()
	timeout := DefaultExecTimeout
	if start.TimeoutMs > 0 {
		timeout = time.Duration(start.TimeoutMs) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	//nolint:gosec // arguments are executed as the unprivileged work sandbox user
	cmd := exec.CommandContext(ctx, strings.TrimSpace(start.Command), start.Args...)
	cmd.Dir = workdir
	cmd.Env = sanitizedSandboxEnv(s.homeDir)
	for _, key := range []string{
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
		"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE", "GIT_SSL_CAINFO",
	} {
		if value := os.Getenv(key); value != "" {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	output := &terminalOutput{stream: stream, available: MaxExecOutputBytes, cancel: cancel}
	cmd.Stdout = terminalWriter{output: output}
	cmd.Stderr = terminalWriter{output: output, stderr: true}
	input, err := cmd.StdinPipe()
	if err != nil {
		return status.Error(codes.Internal, "terminal input unavailable")
	}
	defer func() { _ = input.Close() }()
	if err := cmd.Start(); err != nil {
		return status.Error(codes.InvalidArgument, "could not start terminal command")
	}
	go copyTerminalInput(stream, input, cancel)
	runErr := cmd.Wait()
	// Descendants may outlive a successful shell exit. A turn-owned terminal
	// must terminate them even if the foreground process exited on its own.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if runErr != nil {
		var exit *exec.ExitError
		if !errors.As(runErr, &exit) {
			return status.Error(codes.Internal, "terminal command failed")
		}
	}
	//nolint:gosec // POSIX process exit codes fit in int32
	return stream.Send(&agentv1.RunTerminalResponse{Exited: true, ExitCode: int32(cmd.ProcessState.ExitCode())})
}

func copyTerminalInput(stream grpc.BidiStreamingServer[agentv1.RunTerminalRequest, agentv1.RunTerminalResponse], input io.WriteCloser, cancel context.CancelFunc) {
	defer func() { _ = input.Close() }()
	for {
		frame, err := stream.Recv()
		if err != nil || frame.GetCloseInput() {
			return
		}
		data := frame.GetInput()
		if len(data) == 0 || len(data) > 64<<10 {
			cancel()
			return
		}
		if _, err := input.Write(data); err != nil {
			return
		}
	}
}

type terminalOutput struct {
	stream    grpc.BidiStreamingServer[agentv1.RunTerminalRequest, agentv1.RunTerminalResponse]
	cancel    context.CancelFunc
	mu        sync.Mutex
	available int
	truncated bool
}

type terminalWriter struct {
	output *terminalOutput
	stderr bool
}

func (w terminalWriter) Write(data []byte) (int, error) {
	o := w.output
	o.mu.Lock()
	defer o.mu.Unlock()
	size := min(len(data), o.available)
	o.available -= size
	truncated := size < len(data) && !o.truncated
	o.truncated = o.truncated || truncated
	if size != 0 || truncated {
		if err := o.stream.Send(&agentv1.RunTerminalResponse{Data: data[:size], Stderr: w.stderr, Truncated: truncated}); err != nil {
			o.cancel()
			return 0, err
		}
	}
	return len(data), nil
}
