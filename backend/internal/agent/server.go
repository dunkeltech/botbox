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
	desktop  *DesktopService
}

// NewServer creates an AgentService gRPC server using the provided hostname.
// If hostname is empty, os.Hostname() is used with a "sandbox" fallback.
func NewServer(hostname string) *Server {
	return NewServerWithDesktop(hostname, NewDesktopService(nil))
}

// NewServerWithDesktop creates an AgentService gRPC server backed by desktop.
func NewServerWithDesktop(hostname string, desktop *DesktopService) *Server {
	if hostname == "" {
		if resolved, err := os.Hostname(); err == nil && resolved != "" {
			hostname = resolved
		} else {
			hostname = "sandbox"
		}
	}
	if desktop == nil {
		desktop = NewDesktopService(nil)
	}

	return &Server{
		hostname: hostname,
		desktop:  desktop,
	}
}

// Ping verifies gRPC connectivity and validates request bounds and cancellation.
func (s *Server) Ping(ctx context.Context, req *agentv1.PingRequest) (*agentv1.PingResponse, error) {
	if err := checkRPCContext(ctx, "ping"); err != nil {
		return nil, err
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

// Screenshot captures the current :0 X11 display as PNG bytes and dimensions.
func (s *Server) Screenshot(ctx context.Context, req *agentv1.ScreenshotRequest) (*agentv1.ScreenshotResponse, error) {
	if err := checkRPCContext(ctx, "screenshot"); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "screenshot request is required")
	}

	pngBytes, width, height, err := s.desktop.Screenshot(ctx)
	if err != nil {
		return nil, mapDesktopGRPCError(err)
	}

	return &agentv1.ScreenshotResponse{
		ImagePng: pngBytes,
		Width:    width,
		Height:   height,
	}, nil
}

// Click moves the X11 pointer within 1280x800 and triggers the requested mouse button.
func (s *Server) Click(ctx context.Context, req *agentv1.ClickRequest) (*agentv1.ClickResponse, error) {
	if err := checkRPCContext(ctx, "click"); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "click request is required")
	}

	if err := s.desktop.Click(ctx, req.X, req.Y, req.Button); err != nil {
		return nil, mapDesktopGRPCError(err)
	}

	return &agentv1.ClickResponse{}, nil
}

// Type types text into the focused X11 window.
func (s *Server) Type(ctx context.Context, req *agentv1.TypeRequest) (*agentv1.TypeResponse, error) {
	if err := checkRPCContext(ctx, "type"); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "type request is required")
	}

	if err := s.desktop.Type(ctx, req.Text); err != nil {
		return nil, mapDesktopGRPCError(err)
	}

	return &agentv1.TypeResponse{}, nil
}

// Key sends a key or key chord to the focused X11 window.
func (s *Server) Key(ctx context.Context, req *agentv1.KeyRequest) (*agentv1.KeyResponse, error) {
	if err := checkRPCContext(ctx, "key"); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "key request is required")
	}

	if err := s.desktop.Key(ctx, req.Key); err != nil {
		return nil, mapDesktopGRPCError(err)
	}

	return &agentv1.KeyResponse{}, nil
}

func checkRPCContext(ctx context.Context, op string) error {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return status.Errorf(codes.DeadlineExceeded, "%s request deadline exceeded", op)
		}
		return status.Errorf(codes.Canceled, "%s request canceled", op)
	}
	return nil
}

func mapDesktopGRPCError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, ErrInvalidDesktopInput):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
