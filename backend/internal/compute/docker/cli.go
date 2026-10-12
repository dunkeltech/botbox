package docker

import (
	"context"
	"errors"
	"io"

	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	agentv1 "github.com/dunkeltech/botbox/backend/internal/gen/proto/agent/v1"
)

// RunCLI streams a sandbox CLI turn through the authenticated agent connection.
func (d *Driver) RunCLI(ctx context.Context, id compute.EnvID, cli, prompt, session, model string, emit func(engine.Event) error) error {
	return d.RunCLIWithSubscription(ctx, id, cli, prompt, session, model, false, emit)
}

// RunCLIWithSubscription selects a placeholder-only subscription environment.
// The real token is resolved by the external proxy, never passed over this RPC.
func (d *Driver) RunCLIWithSubscription(ctx context.Context, id compute.EnvID, cli, prompt, session, model string, subscription bool, emit func(engine.Event) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	client, rpcCtx, closeConn, err := d.agentClient(ctx, id)
	if err != nil {
		return err
	}
	defer closeConn()
	stream, err := client.RunCLI(rpcCtx, &agentv1.RunCLIRequest{Cli: cli, Prompt: prompt, SessionId: session, Model: model, ClaudeSubscription: subscription})
	if err != nil {
		return err
	}
	stdout, outWriter := io.Pipe()
	stderr, errWriter := io.Pipe()
	finished := make(chan error, 1)
	go func() {
		var finalErr error
		defer func() {
			_ = outWriter.CloseWithError(finalErr)
			_ = errWriter.CloseWithError(finalErr)
			finished <- finalErr
		}()
		for {
			frame, recvErr := stream.Recv()
			if recvErr != nil {
				if !errors.Is(recvErr, io.EOF) {
					finalErr = recvErr
				}
				return
			}
			if frame.Exited {
				if frame.ExitCode != 0 {
					finalErr = errors.New("CLI returned nonzero exit code")
				}
				return
			}
			writer := outWriter
			if frame.Stderr {
				writer = errWriter
			}
			if _, e := writer.Write(frame.Data); e != nil {
				finalErr = e
				return
			}
		}
	}()
	parseErr := engine.Stream(ctx, cli, stdout, stderr, emit)
	cancel()
	recvErr := <-finished
	if parseErr != nil {
		return parseErr
	}
	return recvErr
}
