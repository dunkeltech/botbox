package docker

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/dunkeltech/botbox/backend/internal/compute"
)

const dockerMaxFrame = 1 << 20

// agentRelayScript is the fixed in-sandbox command that splices the exec
// stdio onto the agent. The image has Python and no socat. The path is
// absolute so a file in the writable home directory cannot shadow it.
// The script contains no secret and no caller input.
const agentRelayScript = `
import os, socket, threading
s = socket.create_connection(("127.0.0.1", 9090))
s.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
def upload():
    try:
        while True:
            chunk = os.read(0, 65536)
            if not chunk:
                break
            s.sendall(chunk)
    finally:
        try:
            s.shutdown(socket.SHUT_WR)
        except OSError:
            pass
threading.Thread(target=upload, daemon=True).start()
try:
    while True:
        chunk = s.recv(65536)
        if not chunk:
            break
        view = memoryview(chunk)
        while len(view):
            wrote = os.write(1, view)
            if wrote <= 0:
                raise SystemExit(1)
            view = view[wrote:]
finally:
    s.close()
`

// publishedLoopback reports whether addr is a host loopback port Docker
// actually allocated. Bot networks are internal, so a requested binding often
// has no host port and this returns false.
func publishedLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (d *Driver) dialAgentGRPC(ctx context.Context, id compute.EnvID, addr string) (*grpc.ClientConn, error) {
	if d.agentDialer != nil {
		return d.agentDialer(ctx, addr)
	}
	// passthrough skips DNS so the dialer below is the only connect path.
	target := addr
	if !strings.Contains(addr, "://") {
		target = "passthrough:///" + addr
	}
	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		if publishedLoopback(addr) {
			var nd net.Dialer
			return nd.DialContext(ctx, "tcp", addr)
		}
		// An internal network never publishes 9090, and a local Go binary
		// cannot use Docker Desktop's bridge route (EHOSTUNREACH). The Docker
		// socket is the remaining path. The sandbox gains no new route.
		return d.dialAgentExec(ctx, string(id))
	}
	return grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(dialer))
}

func (d *Driver) dialAgentExec(ctx context.Context, id string) (net.Conn, error) {
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, "/\\") {
		return nil, errors.New("invalid sandbox id")
	}
	payload := map[string]any{
		"AttachStdin":  true,
		"AttachStdout": true,
		"AttachStderr": true,
		"Tty":          false,
		"Cmd":          []string{"/usr/bin/python3", "-c", agentRelayScript},
	}
	var created struct {
		ID string `json:"Id"`
	}
	if _, err := d.doJSON(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/exec", payload, &created); err != nil {
		return nil, fmt.Errorf("open sandbox agent tunnel: %w", err)
	}
	if strings.TrimSpace(created.ID) == "" {
		return nil, errors.New("open sandbox agent tunnel: docker returned no exec id")
	}
	start := []byte(`{"Detach":false,"Tty":false}`)
	conn, err := d.hijack(ctx, "/exec/"+url.PathEscape(created.ID)+"/start", start)
	if err != nil {
		return nil, fmt.Errorf("start sandbox agent tunnel: %w", err)
	}
	return conn, nil
}

func (d *Driver) hijack(ctx context.Context, path string, payload []byte) (net.Conn, error) {
	if d.daemonDial == nil {
		return nil, errors.New("docker dial is not configured")
	}
	raw, err := d.daemonDial(ctx)
	if err != nil {
		return nil, fmt.Errorf("dial docker engine: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(deadline)
	}
	success := false
	defer func() {
		if !success {
			_ = raw.Close()
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+path, strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	if err := req.Write(raw); err != nil {
		return nil, fmt.Errorf("write docker exec start: %w", err)
	}
	reader := bufio.NewReader(raw)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, fmt.Errorf("read docker exec start: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("docker exec start HTTP %d: %s", resp.StatusCode, msg)
	}
	var prefix []byte
	if n := reader.Buffered(); n > 0 {
		prefix = make([]byte, n)
		if _, err := io.ReadFull(reader, prefix); err != nil {
			return nil, err
		}
	}
	// The dial deadline must not carry into the chat stream.
	_ = raw.SetDeadline(time.Time{})
	success = true
	conn := raw
	if len(prefix) > 0 {
		conn = &prefixConn{Conn: raw, rest: prefix}
	}
	return &dockerStream{c: conn}, nil
}

type prefixConn struct {
	net.Conn
	rest []byte
}

func (p *prefixConn) Read(b []byte) (int, error) {
	if len(p.rest) > 0 {
		n := copy(b, p.rest)
		p.rest = p.rest[n:]
		return n, nil
	}
	return p.Conn.Read(b)
}

// dockerStream unwraps the Docker exec multiplex so gRPC sees a plain byte stream.
// Stderr frames are dropped. They are relay diagnostics, not agent bytes.
type dockerStream struct {
	c       net.Conn
	mu      sync.Mutex
	header  [8]byte
	have    int
	rest    int
	skip    bool
	empties int
	pending []byte
}

func (s *dockerStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.pending) == 0 {
		if err := s.pull(); err != nil {
			return 0, err
		}
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

func (s *dockerStream) pull() error {
	if s.rest == 0 {
		for s.have < len(s.header) {
			n, err := s.c.Read(s.header[s.have:])
			s.have += n
			if err != nil {
				if errors.Is(err, io.EOF) && s.have == 0 {
					return io.EOF
				}
				if n == 0 || s.have < len(s.header) {
					if errors.Is(err, io.EOF) {
						return io.ErrUnexpectedEOF
					}
					return err
				}
			}
		}
		s.have = 0
		size := int(binary.BigEndian.Uint32(s.header[4:8]))
		if size > dockerMaxFrame {
			return errors.New("docker exec frame is too large")
		}
		if size == 0 {
			s.empties++
			if s.empties > 32 {
				return errors.New("docker exec sent empty frames")
			}
			return nil
		}
		s.empties = 0
		s.skip = s.header[0] != 1
		s.rest = size
	}
	buf := make([]byte, s.rest)
	n, err := s.c.Read(buf)
	if n > 0 {
		s.rest -= n
		if !s.skip {
			s.pending = append(s.pending, buf[:n]...)
		}
	}
	if err != nil {
		if s.rest > 0 && errors.Is(err, io.EOF) {
			if len(s.pending) > 0 {
				return nil
			}
			return io.ErrUnexpectedEOF
		}
		if len(s.pending) > 0 {
			return nil
		}
		return err
	}
	return nil
}

func (s *dockerStream) Write(p []byte) (int, error)       { return s.c.Write(p) }
func (s *dockerStream) Close() error                      { return s.c.Close() }
func (s *dockerStream) LocalAddr() net.Addr               { return s.c.LocalAddr() }
func (s *dockerStream) RemoteAddr() net.Addr              { return s.c.RemoteAddr() }
func (s *dockerStream) SetDeadline(t time.Time) error     { return s.c.SetDeadline(t) }
func (s *dockerStream) SetReadDeadline(t time.Time) error { return s.c.SetReadDeadline(t) }
func (s *dockerStream) SetWriteDeadline(t time.Time) error {
	return s.c.SetWriteDeadline(t)
}
