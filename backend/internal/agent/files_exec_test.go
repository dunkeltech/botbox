package agent_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/dunkeltech/botbox/backend/internal/agent"
	agentv1 "github.com/dunkeltech/botbox/backend/internal/gen/proto/agent/v1"
)

func TestServerGetInfoAndExec(t *testing.T) {
	homeDir := t.TempDir()
	baseTime := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	srv := agent.NewServerWithOptions(agent.ServerOptions{
		Hostname:  "sandbox-exec",
		HomeDir:   homeDir,
		StartedAt: baseTime,
		Now:       func() time.Time { return baseTime.Add(42 * time.Second) },
	})

	info, err := srv.GetInfo(context.Background(), &agentv1.GetInfoRequest{})
	if err != nil {
		t.Fatalf("GetInfo failed: %v", err)
	}
	if info.DisplayWidth != agent.DesktopWidth || info.DisplayHeight != agent.DesktopHeight || info.UptimeSeconds != 42 || info.HomeDir != agent.DefaultHomeDir {
		t.Fatalf("unexpected GetInfo response: %+v", info)
	}

	t.Run("exec echo succeeds inside homeDir and strips agent token", func(t *testing.T) {
		t.Setenv("BOTBOX_AGENT_TOKEN", "top-secret-token")

		resp, err := srv.Exec(context.Background(), &agentv1.ExecRequest{
			Command: "sh",
			Args:    []string{"-c", "echo hello-world && printenv BOTBOX_AGENT_TOKEN || true"},
			Workdir: "/home/bot",
		})
		if err != nil {
			t.Fatalf("Exec failed: %v", err)
		}
		if resp.ExitCode != 0 {
			t.Fatalf("exit_code = %d, want 0 (stderr=%q)", resp.ExitCode, resp.Stderr)
		}
		if strings.TrimSpace(resp.Stdout) != "hello-world" {
			t.Fatalf("stdout = %q, want %q", resp.Stdout, "hello-world")
		}
		if strings.Contains(resp.Stdout, "top-secret-token") {
			t.Fatal("agent token leaked into child command environment")
		}
	})

	t.Run("exec preserves non-zero exit codes", func(t *testing.T) {
		resp, err := srv.Exec(context.Background(), &agentv1.ExecRequest{
			Command: "sh",
			Args:    []string{"-c", "echo oops >&2; exit 7"},
		})
		if err != nil {
			t.Fatalf("Exec unexpected error: %v", err)
		}
		if resp.ExitCode != 7 || !strings.Contains(resp.Stderr, "oops") {
			t.Fatalf("unexpected non-zero response: %+v", resp)
		}
	})

	t.Run("exec enforces timeout and workdir containment", func(t *testing.T) {
		if _, err := srv.Exec(context.Background(), &agentv1.ExecRequest{
			Command:   "sleep",
			Args:      []string{"2"},
			TimeoutMs: 50,
		}); status.Code(err) != codes.DeadlineExceeded {
			t.Fatalf("timeout code = %v, want DeadlineExceeded", status.Code(err))
		}

		if _, err := srv.Exec(context.Background(), &agentv1.ExecRequest{
			Command: "pwd",
			Workdir: "../etc",
		}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("workdir escape code = %v, want PermissionDenied", status.Code(err))
		}
	})
}

func TestServerFilesAndPathContainment(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	outsideFile := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("outside-secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(homeDir, "escape-link")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	srv := agent.NewServerWithOptions(agent.ServerOptions{HomeDir: homeDir})

	writeResp, err := srv.WriteFile(context.Background(), &agentv1.WriteFileRequest{
		Path:    "/home/bot/notes/todo.txt",
		Content: []byte("check sandbox logs"),
	})
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if writeResp.Path != "/home/bot/notes/todo.txt" || writeResp.SizeBytes != 18 {
		t.Fatalf("unexpected WriteFile response: %+v", writeResp)
	}

	listResp, err := srv.ListFiles(context.Background(), &agentv1.ListFilesRequest{Path: "/home/bot/notes"})
	if err != nil {
		t.Fatalf("ListFiles failed: %v", err)
	}
	if listResp.Path != "/home/bot/notes" || len(listResp.Entries) != 1 || listResp.Entries[0].Name != "todo.txt" {
		t.Fatalf("unexpected ListFiles response: %+v", listResp)
	}

	readResp, err := srv.ReadFile(context.Background(), &agentv1.ReadFileRequest{Path: "notes/todo.txt"})
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(readResp.Content) != "check sandbox logs" {
		t.Fatalf("ReadFile content = %q, want %q", string(readResp.Content), "check sandbox logs")
	}

	negativePaths := []string{
		"../etc/passwd",
		"/home/bot/../../etc/passwd",
		"/etc/passwd",
		outsideFile,
		"/home/bot/escape-link",
		"escape-link",
	}
	for _, p := range negativePaths {
		if _, err := srv.ReadFile(context.Background(), &agentv1.ReadFileRequest{Path: p}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("ReadFile(%q) code = %v, want PermissionDenied", p, status.Code(err))
		}
		if _, err := srv.WriteFile(context.Background(), &agentv1.WriteFileRequest{Path: p, Content: []byte("x")}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("WriteFile(%q) code = %v, want PermissionDenied", p, status.Code(err))
		}
		if _, err := srv.ListFiles(context.Background(), &agentv1.ListFilesRequest{Path: p}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("ListFiles(%q) code = %v, want PermissionDenied", p, status.Code(err))
		}
	}
}

func TestTokenAuthInterceptor(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	const secretToken = "sandbox-secret-token-123"

	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = lis.Close() })

	grpcSrv := grpc.NewServer(
		grpc.UnaryInterceptor(agent.TokenAuthInterceptor(secretToken)),
		grpc.StreamInterceptor(agent.TokenStreamInterceptor(secretToken)),
	)
	agentv1.RegisterAgentServiceServer(grpcSrv, agent.NewServerWithOptions(agent.ServerOptions{
		Hostname: "auth-box",
		HomeDir:  homeDir,
	}))
	go func() { _ = grpcSrv.Serve(lis) }()
	t.Cleanup(grpcSrv.GracefulStop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := agentv1.NewAgentServiceClient(conn)

	// Missing token -> Unauthenticated
	if _, err := client.Ping(context.Background(), &agentv1.PingRequest{Caller: "test"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token code = %v, want Unauthenticated", status.Code(err))
	}

	// Wrong token -> Unauthenticated
	wrongCtx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(agent.AgentTokenMetadataKey, "wrong-token"))
	if _, err := client.Ping(wrongCtx, &agentv1.PingRequest{Caller: "test"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("wrong token code = %v, want Unauthenticated", status.Code(err))
	}

	// Valid token -> OK
	validCtx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(agent.AgentTokenMetadataKey, secretToken))
	resp, err := client.Ping(validCtx, &agentv1.PingRequest{Caller: "test"})
	if err != nil || resp.Hostname != "auth-box" {
		t.Fatalf("valid token Ping failed: resp=%+v err=%v", resp, err)
	}
}
