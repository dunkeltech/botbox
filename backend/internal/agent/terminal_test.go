// SPDX-License-Identifier: AGPL-3.0-only

package agent_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/agent"
	agentv1 "github.com/dunkeltech/botbox/backend/internal/gen/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const terminalTestToken = "fixture-agent-terminal" //nolint:gosec // synthetic authentication fixture

func TestRunTerminalStreamsInputAndCancelsOnHumanTakeover(t *testing.T) {
	desktop := agent.NewDesktopService(nil)
	desktop.SetControl(agent.NewControlGate(""))
	client := terminalTestClient(t, desktop)
	stream := startTestTerminal(t, client, "printf 'ready\\n'; read -r value; printf '%s' \"$value\"")
	receiveText(t, stream, "ready\n")
	if err := stream.Send(&agentv1.RunTerminalRequest{Frame: &agentv1.RunTerminalRequest_Input{Input: []byte("fixture input\n")}}); err != nil {
		t.Fatal(err)
	}
	receiveText(t, stream, "fixture input")
	frame, err := stream.Recv()
	if err != nil || !frame.GetExited() || frame.GetExitCode() != 0 {
		t.Fatalf("terminal completion = %v, %v", frame, err)
	}

	waiting := startTestTerminal(t, client, "printf 'ready\\n'; read -r value")
	receiveText(t, waiting, "ready\n")
	desktop.SetHuman(true)
	if _, err := waiting.Recv(); status.Code(err) != codes.Canceled {
		t.Fatalf("takeover did not cancel terminal: %v", err)
	}
	locked := startTestTerminal(t, client, "printf forbidden")
	if _, err := locked.Recv(); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("terminal started under human control: %v", err)
	}
}

func TestRunTerminalRejectsMissingStartAndEscapingWorkdir(t *testing.T) {
	client := terminalTestClient(t, agent.NewDesktopService(nil))
	for _, tc := range []struct {
		name  string
		frame *agentv1.RunTerminalRequest
		code  codes.Code
	}{
		{"missing start", &agentv1.RunTerminalRequest{Frame: &agentv1.RunTerminalRequest_Input{Input: []byte("invalid")}}, codes.InvalidArgument},
		{"unsafe workdir", terminalStart("exit", "../../etc"), codes.PermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream, err := client.RunTerminal(terminalContext(t))
			if err != nil {
				t.Fatal(err)
			}
			if err := stream.Send(tc.frame); err != nil && !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			if _, err := stream.Recv(); status.Code(err) != tc.code {
				t.Fatalf("terminal returned %v, want %s", err, tc.code)
			}
		})
	}
}

func TestRunTerminalRejectsMissingOrForeignAgentToken(t *testing.T) {
	client := terminalTestClient(t, agent.NewDesktopService(nil))
	for _, token := range []string{"", "other-agent"} {
		ctx := metadata.AppendToOutgoingContext(t.Context(), agent.AgentTokenMetadataKey, token)
		stream, err := client.RunTerminal(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Recv(); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("unauthenticated terminal RPC returned %v", err)
		}
	}
}

func terminalContext(t *testing.T) context.Context {
	t.Helper()
	return metadata.AppendToOutgoingContext(t.Context(), agent.AgentTokenMetadataKey, terminalTestToken)
}

func terminalTestClient(t *testing.T, desktop *agent.DesktopService) agentv1.AgentServiceClient {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer(grpc.StreamInterceptor(agent.TokenStreamInterceptor(terminalTestToken)))
	agentv1.RegisterAgentServiceServer(grpcServer, agent.NewServerWithOptions(agent.ServerOptions{Desktop: desktop, HomeDir: t.TempDir()}))
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)
	conn, err := grpc.NewClient("passthrough:///terminal", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return agentv1.NewAgentServiceClient(conn)
}

func terminalStart(script, workdir string) *agentv1.RunTerminalRequest {
	return &agentv1.RunTerminalRequest{Frame: &agentv1.RunTerminalRequest_Start{Start: &agentv1.ExecRequest{
		Command: "/bin/sh", Args: []string{"-c", script}, Workdir: workdir,
	}}}
}

func startTestTerminal(t *testing.T, client agentv1.AgentServiceClient, script string) agentv1.AgentService_RunTerminalClient {
	t.Helper()
	stream, err := client.RunTerminal(terminalContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = stream.Send(terminalStart(script, ".")); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	return stream
}

func receiveText(t *testing.T, stream agentv1.AgentService_RunTerminalClient, expected string) {
	t.Helper()
	var output strings.Builder
	for output.Len() < len(expected) {
		frame, err := stream.Recv()
		if err != nil || frame.GetExited() {
			t.Fatalf("terminal closed early: %v, %v", frame, err)
		}
		output.Write(frame.GetData())
	}
	if output.String() != expected {
		t.Fatalf("terminal output %q, want %q", output.String(), expected)
	}
}
