// Command agent runs inside each sandbox container to expose desktop and
// container diagnostics to the control plane over gRPC.
package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"

	"github.com/dunkeltech/botbox/backend/internal/agent"
	agentv1 "github.com/dunkeltech/botbox/backend/internal/gen/proto/agent/v1"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	addr := envOrDefault("AGENT_GRPC_ADDR", "127.0.0.1:9090")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		logger.Error("failed to bind agent listener", slog.String("addr", addr), slog.Any("error", err))
		os.Exit(1)
	}

	grpcServer := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(grpcServer, agent.NewServer(""))

	errCh := make(chan error, 1)
	go func() {
		logger.Info("agent gRPC server listening", slog.String("addr", addr))
		errCh <- grpcServer.Serve(lis)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			logger.Error("agent gRPC server failed", slog.Any("error", err))
			os.Exit(1)
		}
	case <-ctx.Done():
		logger.Info("shutting down agent gRPC server")
		grpcServer.GracefulStop()
	}
}

func envOrDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
