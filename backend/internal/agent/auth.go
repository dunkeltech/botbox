package agent

import (
	"context"
	"crypto/subtle"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// AgentTokenMetadataKey is the gRPC metadata header carrying the per-sandbox token.
const AgentTokenMetadataKey = "x-agent-token"

// TokenAuthInterceptor enforces a constant-time per-sandbox token check on unary RPCs
// when expectedToken is non-empty.
func TokenAuthInterceptor(expectedToken string) grpc.UnaryServerInterceptor {
	expected := strings.TrimSpace(expectedToken)
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if err := verifyMetadataToken(ctx, expected); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// TokenStreamInterceptor enforces a constant-time per-sandbox token check on streaming RPCs
// when expectedToken is non-empty.
func TokenStreamInterceptor(expectedToken string) grpc.StreamServerInterceptor {
	expected := strings.TrimSpace(expectedToken)
	return func(
		srv any,
		ss grpc.ServerStream,
		_ *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		if err := verifyMetadataToken(ss.Context(), expected); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}

func verifyMetadataToken(ctx context.Context, expected string) error {
	if expected == "" {
		return nil
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing agent authentication metadata")
	}
	vals := md.Get(AgentTokenMetadataKey)
	if len(vals) == 0 || vals[0] == "" {
		return status.Error(codes.Unauthenticated, "missing agent token")
	}
	if subtle.ConstantTimeCompare([]byte(vals[0]), []byte(expected)) != 1 {
		return status.Error(codes.Unauthenticated, "invalid agent token")
	}
	return nil
}
