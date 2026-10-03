package agent_test

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/dunkeltech/botbox/backend/internal/agent"
	agentv1 "github.com/dunkeltech/botbox/backend/internal/gen/proto/agent/v1"
)

func TestServerPing(t *testing.T) {
	t.Parallel()

	srv := agent.NewServer("atlas-sandbox")

	tests := []struct {
		name      string
		cancelCtx bool
		req       *agentv1.PingRequest
		wantCode  codes.Code
		wantHost  string
	}{
		{
			name:     "valid request returns ok and hostname",
			req:      &agentv1.PingRequest{Caller: "api"},
			wantCode: codes.OK,
			wantHost: "atlas-sandbox",
		},
		{
			name:     "nil request returns InvalidArgument",
			req:      nil,
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "oversized caller returns InvalidArgument",
			req:      &agentv1.PingRequest{Caller: strings.Repeat("a", 129)},
			wantCode: codes.InvalidArgument,
		},
		{
			name:      "canceled context returns Canceled",
			cancelCtx: true,
			req:       &agentv1.PingRequest{Caller: "api"},
			wantCode:  codes.Canceled,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			if tc.cancelCtx {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			resp, err := srv.Ping(ctx, tc.req)
			if gotCode := status.Code(err); gotCode != tc.wantCode {
				t.Fatalf("status code = %v, want %v (err=%v)", gotCode, tc.wantCode, err)
			}

			if tc.wantCode == codes.OK {
				if resp == nil || resp.Status != "ok" || resp.Hostname != tc.wantHost {
					t.Fatalf("unexpected response: %+v", resp)
				}
			}
		})
	}
}
