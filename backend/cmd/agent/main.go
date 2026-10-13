// Command agent runs inside each sandbox container to expose desktop,
// command execution, and /home/bot file access to the control plane over gRPC.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/dunkeltech/botbox/backend/internal/agent"
	agentv1 "github.com/dunkeltech/botbox/backend/internal/gen/proto/agent/v1"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if len(os.Args) > 1 && os.Args[1] == "--mcp-stdio" {
		if err := serveMCPStdio(context.Background()); err != nil {
			logger.Error("desktop mcp stdio failed", slog.Any("error", err))
			os.Exit(1)
		}
		return
	}
	addr := envOrDefault("AGENT_GRPC_ADDR", "127.0.0.1:9090")
	homeDir := envOrDefault("BOTBOX_HOME_DIR", agent.DefaultHomeDir)
	agentToken := os.Getenv("BOTBOX_AGENT_TOKEN")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	desktop := newSandboxDesktop()

	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		logger.Error("failed to bind agent listener", slog.String("addr", addr), slog.Any("error", err))
		os.Exit(1)
	}

	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(agent.TokenAuthInterceptor(agentToken)),
		grpc.StreamInterceptor(agent.TokenStreamInterceptor(agentToken)),
	)
	agentv1.RegisterAgentServiceServer(grpcServer, agent.NewServerWithOptions(agent.ServerOptions{
		HomeDir: homeDir,
		Desktop: desktop,
	}))

	// MCP stays on loopback in this process so takeover cancels the live xdotool.
	mux := http.NewServeMux()
	mux.Handle("POST /mcp", agent.NewMCPHandler(desktop))
	mcpServer := &http.Server{
		Addr:              "127.0.0.1:9091",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 2)
	go func() {
		logger.Info("agent gRPC server listening", slog.String("addr", addr), slog.Bool("token_auth", agentToken != ""))
		errCh <- grpcServer.Serve(lis)
	}()
	go func() {
		logger.Info("desktop mcp listening", slog.String("addr", mcpServer.Addr))
		if err := mcpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		if err != nil {
			logger.Error("agent server failed", slog.Any("error", err))
			os.Exit(1)
		}
	case <-ctx.Done():
		logger.Info("shutting down agent")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = mcpServer.Shutdown(shutdownCtx)
		grpcServer.GracefulStop()
	}
}

func serveMCPStdio(ctx context.Context) error {
	desktop := newSandboxDesktop()
	return agent.NewMCPHandler(desktop).ServeStream(ctx, os.Stdin, os.Stdout)
}

func newSandboxDesktop() *agent.DesktopService {
	desktop := agent.NewDesktopService(nil)
	desktop.SetControl(agent.NewControlGate(agent.ControlLockFile))
	desktop.SetJournal(agent.NewJournal(newSessionID()))
	desktop.SetBrowser(agent.NewCDPBrowser(""))
	return desktop
}

func newSessionID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "session"
	}
	return hex.EncodeToString(buf)
}

func envOrDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
