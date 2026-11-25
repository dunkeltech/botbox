// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"context"
	"errors"

	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/computermcp"
	agentv1 "github.com/dunkeltech/botbox/backend/internal/gen/proto/agent/v1"
	"google.golang.org/grpc"
)

// OpenTerminal starts a tool command on the desktop's authenticated agent.
// Closing the stream cancels the process group inside that sandbox.
func (d *Driver) OpenTerminal(ctx context.Context, id compute.EnvID, cmd computermcp.Command) (computermcp.Terminal, error) {
	ctx, cancel := context.WithCancel(ctx)
	client, rpcCtx, closeConn, err := d.agentClient(ctx, id)
	if err != nil {
		cancel()
		return nil, err
	}
	closeTerminal := func() {
		cancel()
		closeConn()
	}
	stream, err := client.RunTerminal(rpcCtx)
	if err != nil {
		closeTerminal()
		return nil, err
	}
	start := &agentv1.RunTerminalRequest{Frame: &agentv1.RunTerminalRequest_Start{Start: &agentv1.ExecRequest{
		Command: cmd.Program, Args: cmd.Args, Workdir: cmd.Workdir, TimeoutMs: cmd.TimeoutMS,
	}}}
	if err := stream.Send(start); err != nil {
		closeTerminal()
		return nil, err
	}
	return &sandboxTerminal{stream: stream, close: closeTerminal}, nil
}

type sandboxTerminal struct {
	stream      grpc.BidiStreamingClient[agentv1.RunTerminalRequest, agentv1.RunTerminalResponse]
	close       func()
	inputClosed bool
}

func (t *sandboxTerminal) Receive() (computermcp.TerminalFrame, error) {
	frame, err := t.stream.Recv()
	if err != nil {
		return computermcp.TerminalFrame{}, err
	}
	return computermcp.TerminalFrame{
		Data: frame.GetData(), Stderr: frame.GetStderr(), Exited: frame.GetExited(),
		ExitCode: frame.GetExitCode(), Truncated: frame.GetTruncated(),
	}, nil
}

func (t *sandboxTerminal) SendInput(data []byte, closeInput bool) error {
	if t.inputClosed {
		return errors.New("terminal input closed")
	}
	if len(data) > 0 {
		if err := t.stream.Send(&agentv1.RunTerminalRequest{Frame: &agentv1.RunTerminalRequest_Input{Input: data}}); err != nil {
			return err
		}
	}
	if closeInput {
		t.inputClosed = true
		return t.stream.Send(&agentv1.RunTerminalRequest{Frame: &agentv1.RunTerminalRequest_CloseInput{CloseInput: true}})
	}
	return nil
}

func (t *sandboxTerminal) Close() { t.close() }
