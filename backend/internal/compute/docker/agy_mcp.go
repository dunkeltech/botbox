// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"bufio"
	"context"
	"errors"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/agent"
	"github.com/dunkeltech/botbox/backend/internal/computermcp"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	"github.com/google/uuid"
)

// One endpoint belongs to Docker exec and the other to agy's configured MCP
// subprocess. Both use the same local socket and neither can reach another
// container through the network.
const acpMCPRelay = `
import os
import select
import socket
import sys
import threading

def relay(conn):
    def send_input():
        try:
            while data := os.read(0, 65536):
                conn.sendall(data)
        except OSError:
            pass
        finally:
            try:
                conn.shutdown(socket.SHUT_WR)
            except OSError:
                pass

    threading.Thread(target=send_input, daemon=True).start()
    while data := conn.recv(65536):
        sys.stdout.buffer.write(data)
        sys.stdout.buffer.flush()

mode, path = sys.argv[1:]
if mode == "connect":
    with socket.socket(socket.AF_UNIX) as peer:
        peer.connect(path)
        relay(peer)
else:
    os.umask(0o077)
    with socket.socket(socket.AF_UNIX) as listener:
        listener.bind(path)
        try:
            listener.listen(1)
            print("ready", flush=True)
            readable, _, _ = select.select([listener, 0], [], [])
            if listener in readable:
                connection, _ = listener.accept()
                with connection:
                    relay(connection)
        finally:
            os.unlink(path)
`

func (d *Driver) startComputerMCP(ctx context.Context, id string, handler *agent.MCPHandler) (engine.ACPMCPServer, func(), error) {
	socket := "/tmp/botbox-mcp-" + uuid.NewString()
	command := "/usr/bin/python3"
	args := []string{"-I", "-c", acpMCPRelay}
	conn, err := d.dialACPExec(ctx, id, append([]string{command}, append(args, "listen", socket)...))
	if err != nil {
		return engine.ACPMCPServer{}, nil, err
	}
	mcpCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(mcpCtx, func() { _ = conn.Close() })
	reader := bufio.NewReaderSize(conn, 32)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := reader.ReadSlice('\n')
	if err != nil || string(line) != "ready\n" {
		cancel()
		stop()
		_ = conn.Close()
		return engine.ACPMCPServer{}, nil, errors.New("computer tool bridge unavailable")
	}
	_ = conn.SetReadDeadline(time.Time{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_ = handler.ServeStream(mcpCtx, reader, conn)
	}()
	finish := func() {
		cancel()
		_ = conn.Close()
		<-finished
		stop()
	}
	return engine.ACPMCPServer{
		Name: computermcp.ServerName, Command: command, Args: append(args, "connect", socket),
	}, finish, nil
}
