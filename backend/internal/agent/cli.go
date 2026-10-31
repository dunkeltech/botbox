package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	agentv1 "github.com/dunkeltech/botbox/backend/internal/gen/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RunCLI streams a fixed CLI binary inside the isolated bot sandbox.
// The request context owns the process group, including tool subprocesses.
func (s *Server) RunCLI(req *agentv1.RunCLIRequest, stream grpc.ServerStreamingServer[agentv1.RunCLIResponse]) error {
	if req == nil || strings.TrimSpace(req.Prompt) == "" || len(req.Prompt) > 65536 || len(req.SessionId) > 255 || len(req.Model) > 100 {
		return status.Error(codes.InvalidArgument, "invalid CLI turn")
	}
	if req.ClaudeSubscription && req.Cli != "claude" {
		return status.Error(codes.InvalidArgument, "subscription authentication requires Claude CLI")
	}
	var args []string
	input := []byte(req.Prompt)
	switch req.Cli {
	case "claude":
		args = []string{"-p", "--verbose", "--input-format", "stream-json", "--output-format", "stream-json", "--dangerously-skip-permissions"}
		if req.SessionId != "" {
			args = append(args, "--resume", req.SessionId)
		}
		input, _ = json.Marshal(map[string]any{"type": "user", "message": map[string]string{"role": "user", "content": req.Prompt}})
		input = append(input, '\n')
	case "codex":
		args = []string{"exec", "--json", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check"}
		if req.SessionId != "" {
			args = append(args, "resume", req.SessionId)
		}
		args = append(args, "-")
	case "agy":
		args = []string{"--input-format", "stream-json", "--output-format", "stream-json", "--print-timeout", "600s"}
		if req.SessionId != "" {
			args = append(args, "--conversation", req.SessionId)
		}
		input, _ = json.Marshal(map[string]any{"type": "user", "message": map[string]string{"role": "user", "content": req.Prompt}})
		input = append(input, '\n')
	default:
		return status.Error(codes.InvalidArgument, "unsupported CLI")
	}
	var assignedSlugs []string
	if proxy := strings.TrimSpace(os.Getenv("HTTP_PROXY")); proxy != "" {
		assignedSlugs = DiscoverAssignedMCPSlugs(stream.Context(), proxy)
	}
	if configPath, mcpErr := InstallBotMCPServers(req.Cli, s.homeDir, assignedSlugs); mcpErr == nil && req.Cli == "claude" && configPath != "" {
		args = append(args, "--mcp-config", configPath)
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	binary := req.Cli
	if configured := s.cliBinaries[req.Cli]; configured != "" {
		binary = configured
	}
	ctx, cancel := context.WithTimeout(stream.Context(), 10*time.Minute)
	defer cancel()
	//nolint:gosec // only fixed CLI names or trusted server options select a binary
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = s.homeDir
	cmd.Stdin = strings.NewReader(string(input))
	cmd.Env = sanitizedSandboxEnv(s.homeDir)
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE", "GIT_SSL_CAINFO"} {
		if value := os.Getenv(key); value != "" {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	if proxy := strings.TrimSpace(os.Getenv("HTTP_PROXY")); proxy != "" {
		// Bypass the egress HTTP forward proxy for direct control-plane proxy endpoints like http://egress:8081/mcp/*.
		noProxy := appendNoProxyHost(os.Getenv("NO_PROXY"), proxy)
		trimmedProxy := strings.TrimRight(proxy, "/")
		cmd.Env = append(cmd.Env,
			"NO_PROXY="+noProxy,
			"no_proxy="+noProxy,
			"http_proxy="+proxy,
			"https_proxy="+proxy,
			"ANTHROPIC_BASE_URL="+trimmedProxy+"/llm/anthropic",
			"OPENAI_BASE_URL="+trimmedProxy+"/llm/openai/v1",
		)
	} else if noProxy := strings.TrimSpace(os.Getenv("NO_PROXY")); noProxy != "" {
		cmd.Env = append(cmd.Env, "NO_PROXY="+noProxy, "no_proxy="+noProxy)
	}
	if req.ClaudeSubscription {
		cmd.Env = append(cmd.Env, "CLAUDE_CODE_OAUTH_TOKEN=botbox-placeholder")
	} else {
		cmd.Env = append(cmd.Env, "ANTHROPIC_API_KEY=botbox-placeholder")
	}
	cmd.Env = append(cmd.Env, "OPENAI_API_KEY=botbox-placeholder")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return status.Error(codes.Internal, "cannot open CLI output")
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return status.Error(codes.Internal, "cannot open CLI stderr")
	}
	if err = cmd.Start(); err != nil {
		return status.Error(codes.FailedPrecondition, "CLI binary unavailable")
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	copyPipe := func(reader io.Reader, isStderr bool) {
		defer wg.Done()
		buffer := make([]byte, 32768)
		total := 0
		for {
			n, readErr := reader.Read(buffer)
			if n > 0 {
				total += n
				if total > 8<<20 {
					cancel()
					return
				}
				mu.Lock()
				sendErr := stream.Send(&agentv1.RunCLIResponse{Data: append([]byte(nil), buffer[:n]...), Stderr: isStderr})
				mu.Unlock()
				if sendErr != nil {
					cancel()
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}
	wg.Add(2)
	go copyPipe(stdout, false)
	go copyPipe(stderr, true)
	wg.Wait()
	runErr := cmd.Wait()
	if ctx.Err() != nil {
		return status.Error(codes.Canceled, "CLI turn interrupted")
	}
	code := int32(0)
	if runErr != nil {
		var exit *exec.ExitError
		if errors.As(runErr, &exit) {
			//nolint:gosec // POSIX process exit codes fit in int32
			code = int32(exit.ExitCode())
		} else {
			return status.Error(codes.Internal, "CLI execution failed")
		}
	}
	return stream.Send(&agentv1.RunCLIResponse{Exited: true, ExitCode: code})
}

func appendNoProxyHost(existing, proxyURL string) string {
	entries := []string{"localhost", "127.0.0.1", "::1"}
	if strings.TrimSpace(existing) != "" {
		entries = entries[:0]
		for _, part := range strings.Split(existing, ",") {
			if p := strings.TrimSpace(part); p != "" {
				entries = append(entries, p)
			}
		}
	}
	if u, err := url.Parse(proxyURL); err == nil {
		if host := strings.TrimSpace(u.Hostname()); host != "" && !slices.Contains(entries, host) {
			entries = append(entries, host)
		}
	}
	return strings.Join(entries, ",")
}
