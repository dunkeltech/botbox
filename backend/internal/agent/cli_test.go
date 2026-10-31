package agent_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/agent"
	agentv1 "github.com/dunkeltech/botbox/backend/internal/gen/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type fakeCLIStream struct {
	ctx    context.Context
	frames []*agentv1.RunCLIResponse
	onSend func(*agentv1.RunCLIResponse)
}

func (s *fakeCLIStream) Context() context.Context     { return s.ctx }
func (s *fakeCLIStream) SetHeader(metadata.MD) error  { return nil }
func (s *fakeCLIStream) SendHeader(metadata.MD) error { return nil }
func (s *fakeCLIStream) SetTrailer(metadata.MD)       {}
func (s *fakeCLIStream) SendMsg(any) error            { return nil }
func (s *fakeCLIStream) RecvMsg(any) error            { return nil }
func (s *fakeCLIStream) Send(frame *agentv1.RunCLIResponse) error {
	s.frames = append(s.frames, frame)
	if s.onSend != nil {
		s.onSend(frame)
	}
	return nil
}

func TestRunCLIFixtureAndValidation(t *testing.T) {
	home := t.TempDir()
	binary := filepath.Join(home, "fake-cli")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '{\"type\":\"turn.started\"}'\nprintf '%s\\n' 'permission denied' >&2\nprintenv BOTBOX_AGENT_TOKEN || true\nexit 7\n"
	//nolint:gosec // only the test owner can execute this synthetic fixture
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOTBOX_AGENT_TOKEN", "agent-credential-must-not-leak")
	server := agent.NewServerWithOptions(agent.ServerOptions{HomeDir: home, CLIBinaries: map[string]string{"codex": binary}})
	stream := &fakeCLIStream{ctx: context.Background()}
	if err := server.RunCLI(&agentv1.RunCLIRequest{Cli: "codex", Prompt: "fixture"}, stream); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	exited := false
	for _, frame := range stream.frames {
		if frame.Stderr {
			stderr.Write(frame.Data)
		} else {
			stdout.Write(frame.Data)
		}
		if frame.Exited {
			exited = frame.ExitCode == 7
		}
	}
	if !exited || !strings.Contains(stdout.String(), "turn.started") || !strings.Contains(stderr.String(), "permission denied") || strings.Contains(stdout.String(), "agent-credential") {
		t.Fatalf("unexpected output stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	for _, req := range []*agentv1.RunCLIRequest{nil, {Cli: "sh", Prompt: "fixture"}, {Cli: "codex"}, {Cli: "codex", Prompt: strings.Repeat("x", 65537)}} {
		if err := server.RunCLI(req, &fakeCLIStream{ctx: context.Background()}); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("validation=%v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := server.RunCLI(&agentv1.RunCLIRequest{Cli: "codex", Prompt: "fixture"}, &fakeCLIStream{ctx: ctx}); err == nil {
		t.Fatal("canceled process started")
	}
}

func TestRunCLICancellationKillsToolProcessGroup(t *testing.T) {
	home := t.TempDir()
	fifo := filepath.Join(home, "hold")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(home, "fake-blocking-cli")
	//nolint:gosec // executable fixture owned only by the current test user
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ncat \"$HOME/hold\" &\nprintf ready\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	server := agent.NewServerWithOptions(agent.ServerOptions{HomeDir: home, CLIBinaries: map[string]string{"codex": binary}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &fakeCLIStream{ctx: ctx, onSend: func(frame *agentv1.RunCLIResponse) {
		if strings.Contains(string(frame.Data), "ready") {
			cancel()
		}
	}}
	if err := server.RunCLI(&agentv1.RunCLIRequest{Cli: "codex", Prompt: "fixture"}, stream); status.Code(err) != codes.Canceled {
		t.Fatalf("cancel=%v", err)
	}
}

func TestClaudeSubscriptionUsesOnlyPlaceholderAndNoAPIKey(t *testing.T) {
	home := t.TempDir()
	binary := filepath.Join(home, "fake-claude")
	script := `#!/bin/sh
cat >/dev/null
if [ "$CLAUDE_CODE_OAUTH_TOKEN" != botbox-placeholder ]; then exit 2; fi
if [ -n "${ANTHROPIC_API_KEY:-}" ] || [ -n "${ANTHROPIC_AUTH_TOKEN:-}" ] || [ -n "${BOTBOX_AGENT_TOKEN:-}" ]; then exit 3; fi
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"subscription fixture"}]}}'
`
	//nolint:gosec // executable fixture owned by this test
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "host-token-must-not-enter-sandbox")
	t.Setenv("ANTHROPIC_API_KEY", "host-key-must-not-enter-sandbox")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "host-auth-must-not-enter-sandbox")
	t.Setenv("BOTBOX_AGENT_TOKEN", "agent-token-must-not-enter-cli")
	server := agent.NewServerWithOptions(agent.ServerOptions{HomeDir: home, CLIBinaries: map[string]string{"claude": binary}})
	stream := &fakeCLIStream{ctx: context.Background()}
	if err := server.RunCLI(&agentv1.RunCLIRequest{Cli: "claude", Prompt: "fixture", Model: "sonnet", ClaudeSubscription: true}, stream); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	for _, frame := range stream.frames {
		output.Write(frame.Data)
		if frame.Exited && frame.ExitCode != 0 {
			t.Fatalf("subscription environment check failed: %d", frame.ExitCode)
		}
	}
	if !strings.Contains(output.String(), "subscription fixture") || strings.Contains(output.String(), "must-not") {
		t.Fatal("unsafe or missing subscription output")
	}
	if err := server.RunCLI(&agentv1.RunCLIRequest{Cli: "codex", Prompt: "fixture", ClaudeSubscription: true}, &fakeCLIStream{ctx: context.Background()}); status.Code(err) != codes.InvalidArgument {
		t.Fatal("subscription auth accepted for another CLI")
	}
}

func TestRunCLIDiscoversMCPSlugsAndBypassesProxyHost(t *testing.T) {
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/mcp" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"mcp__gitlab__get_job","description":"Fetch GitLab job","inputSchema":{"type":"object"}}]}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer proxySrv.Close()

	home := t.TempDir()
	binary := filepath.Join(home, "fake-claude")
	script := `#!/bin/sh
cat >/dev/null
printf '%s|%s\n' "$NO_PROXY" "$no_proxy"
`
	//nolint:gosec // executable fixture owned only by this test
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HTTP_PROXY", proxySrv.URL)
	t.Setenv("NO_PROXY", "localhost,127.0.0.1,::1")

	server := agent.NewServerWithOptions(agent.ServerOptions{HomeDir: home, CLIBinaries: map[string]string{"claude": binary}})
	stream := &fakeCLIStream{ctx: context.Background()}
	if err := server.RunCLI(&agentv1.RunCLIRequest{Cli: "claude", Prompt: "list tools"}, stream); err != nil {
		t.Fatal(err)
	}

	var stdout strings.Builder
	for _, frame := range stream.frames {
		if !frame.Stderr {
			stdout.Write(frame.Data)
		}
	}
	if !strings.Contains(stdout.String(), "127.0.0.1") {
		t.Fatalf("expected NO_PROXY to preserve loopback hosts, got %q", stdout.String())
	}
	cfgRaw, err := os.ReadFile(filepath.Join(home, ".botbox", "desktop-mcp.json")) // #nosec G304 -- config written inside t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfgRaw), `"mcp-gitlab"`) || !strings.Contains(string(cfgRaw), "http://egress:8081/mcp/gitlab") {
		t.Fatalf("expected desktop-mcp.json to register mcp-gitlab, got %s", cfgRaw)
	}
}
