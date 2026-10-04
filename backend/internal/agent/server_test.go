package agent_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

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

func TestServerDesktopRPCs(t *testing.T) {
	t.Parallel()

	pngBytes := []byte("\x89PNG\r\n\x1a\ngrpc")

	t.Run("happy path Screenshot Click Type Key", func(t *testing.T) {
		t.Parallel()

		runner := &fakeCmdRunner{out: pngBytes}
		srv := agent.NewServerWithDesktop("desk-1", agent.NewDesktopService(runner))

		shot, err := srv.Screenshot(context.Background(), &agentv1.ScreenshotRequest{})
		if err != nil {
			t.Fatalf("Screenshot failed: %v", err)
		}
		if !slices.Equal(shot.ImagePng, pngBytes) || shot.Width != agent.DesktopWidth || shot.Height != agent.DesktopHeight {
			t.Fatalf("unexpected screenshot response: width=%d height=%d len=%d", shot.Width, shot.Height, len(shot.ImagePng))
		}
		if _, err := srv.Click(context.Background(), &agentv1.ClickRequest{X: 100, Y: 200, Button: "left"}); err != nil {
			t.Fatalf("Click failed: %v", err)
		}
		if _, err := srv.Type(context.Background(), &agentv1.TypeRequest{Text: "hello"}); err != nil {
			t.Fatalf("Type failed: %v", err)
		}
		if _, err := srv.Key(context.Background(), &agentv1.KeyRequest{Key: "Return"}); err != nil {
			t.Fatalf("Key failed: %v", err)
		}
	})

	t.Run("negative cases map to expected gRPC status codes", func(t *testing.T) {
		t.Parallel()

		srv := agent.NewServerWithDesktop("desk-1", agent.NewDesktopService(&fakeCmdRunner{out: pngBytes}))
		failingSrv := agent.NewServerWithDesktop("desk-err", agent.NewDesktopService(&fakeCmdRunner{err: errors.New("X11 socket gone")}))

		if _, err := srv.Screenshot(context.Background(), nil); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("nil Screenshot code = %v, want InvalidArgument", status.Code(err))
		}
		if _, err := srv.Click(context.Background(), &agentv1.ClickRequest{X: 2000, Y: 10}); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("oob Click code = %v, want InvalidArgument", status.Code(err))
		}
		if _, err := srv.Type(context.Background(), &agentv1.TypeRequest{Text: ""}); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("empty Type code = %v, want InvalidArgument", status.Code(err))
		}
		if _, err := srv.Key(context.Background(), &agentv1.KeyRequest{Key: "bad key"}); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid Key code = %v, want InvalidArgument", status.Code(err))
		}
		if _, err := failingSrv.Click(context.Background(), &agentv1.ClickRequest{X: 1, Y: 1}); status.Code(err) != codes.Internal {
			t.Fatalf("runner failure code = %v, want Internal", status.Code(err))
		}

		expiredCtx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
		defer cancel()
		if _, err := srv.Type(expiredCtx, &agentv1.TypeRequest{Text: "hi"}); status.Code(err) != codes.DeadlineExceeded {
			t.Fatalf("expired context code = %v, want DeadlineExceeded", status.Code(err))
		}
		if _, err := srv.Click(context.Background(), nil); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("nil Click code = %v, want InvalidArgument", status.Code(err))
		}
		if _, err := srv.Type(context.Background(), nil); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("nil Type code = %v, want InvalidArgument", status.Code(err))
		}
		if _, err := srv.Key(context.Background(), nil); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("nil Key code = %v, want InvalidArgument", status.Code(err))
		}
	})
}
