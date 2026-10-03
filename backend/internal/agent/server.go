// Package agent implements the gRPC service that runs inside each bot sandbox.
package agent

import (
	"context"
	"errors"
	"os"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/dunkeltech/botbox/backend/internal/gen/proto/agent/v1"
)

const maxCallerLength = 128

// Server implements agentv1.AgentServiceServer for sandbox health and desktop control.
type Server struct {
	agentv1.UnimplementedAgentServiceServer
	hostname string
}

// NewServer creates an AgentService gRPC server using the provided hostname.
// If hostname is empty, os.Hostname() is used with a "sandbox" fallback.
func NewServer(hostname string) *Server {
	if hostname == "" {
		if resolved, err := os.Hostname(); err == nil && resolved != "" {
			hostname = resolved
		} else {
			hostname = "sandbox"
		}
	}

	return &Server{hostname: hostname}
}

// Ping verifies gRPC connectivity and validates request bounds and cancellation.
func (s *Server) Ping(ctx context.Context, req *agentv1.PingRequest) (*agentv1.PingResponse, error) {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, status.Error(codes.DeadlineExceeded, "ping request deadline exceeded")
		}
		return nil, status.Error(codes.Canceled, "ping request canceled")
	}

	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "ping request is required")
	}

	if len(req.Caller) > maxCallerLength {
		return nil, status.Error(codes.InvalidArgument, "caller exceeds maximum length")
	}

	return &agentv1.PingResponse{
		Status:   "ok",
		Hostname: s.hostname,
	}, nil
}
