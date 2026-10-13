package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/dunkeltech/botbox/backend/internal/compute"
	agentv1 "github.com/dunkeltech/botbox/backend/internal/gen/proto/agent/v1"
)

func TestPublishedLoopback(t *testing.T) {
	t.Parallel()
	cases := []struct {
		addr string
		want bool
	}{
		{addr: "127.0.0.1:44000", want: true},
		{addr: "localhost:9090", want: true},
		{addr: "[::1]:9090", want: true},
		{addr: "10.1.2.3:9090", want: false},
		{addr: "172.20.0.5:9090", want: false},
		{addr: "0.0.0.0:9090", want: false},
		{addr: "", want: false},
		{addr: "not-a-port", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			t.Parallel()
			if got := publishedLoopback(tc.addr); got != tc.want {
				t.Fatalf("publishedLoopback(%q) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}

func TestDockerStreamDemux(t *testing.T) {
	t.Parallel()
	stderr := muxFrame(2, []byte("nope"))
	empty := muxFrame(1, nil)
	binaryPayload := []byte{0, 1, 255}

	t.Run("stdout skips stderr and empty frames", func(t *testing.T) {
		t.Parallel()
		var raw []byte
		raw = append(raw, empty...)
		raw = append(raw, stderr...)
		raw = append(raw, muxFrame(1, []byte("yes"))...)
		got := readStream(t, splitBytes(raw))
		if got != "yes" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("preserves binary stdout", func(t *testing.T) {
		t.Parallel()
		got := readStream(t, splitBytes(muxFrame(1, binaryPayload)))
		if got != string(binaryPayload) {
			t.Fatalf("got %v", []byte(got))
		}
	})

	t.Run("truncated header", func(t *testing.T) {
		t.Parallel()
		_, err := io.ReadAll(&dockerStream{c: &chunkConn{chunks: [][]byte{{1, 0, 0}}}})
		if err == nil || !strings.Contains(err.Error(), "unexpected EOF") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("truncated payload surfaces on the next read", func(t *testing.T) {
		t.Parallel()
		frame := muxFrame(1, []byte("abcd"))
		stream := &dockerStream{c: &chunkConn{chunks: [][]byte{frame[:9]}}}
		buf := make([]byte, 8)
		n, err := stream.Read(buf)
		if err != nil || n != 1 || buf[0] != 'a' {
			t.Fatalf("first read n=%d err=%v %q", n, err, buf[:n])
		}
		_, err = stream.Read(buf)
		if err == nil || !strings.Contains(err.Error(), "unexpected EOF") {
			t.Fatalf("second err = %v", err)
		}
	})

	t.Run("rejects an oversized frame", func(t *testing.T) {
		t.Parallel()
		header := make([]byte, 8)
		header[0] = 1
		binary.BigEndian.PutUint32(header[4:], dockerMaxFrame+1)
		_, err := io.ReadAll(&dockerStream{c: &chunkConn{chunks: [][]byte{header}}})
		if err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("rejects an empty-frame flood", func(t *testing.T) {
		t.Parallel()
		var raw []byte
		for i := 0; i < 33; i++ {
			raw = append(raw, empty...)
		}
		_, err := io.ReadAll(&dockerStream{c: &chunkConn{chunks: [][]byte{raw}}})
		if err == nil || !strings.Contains(err.Error(), "empty frames") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestDockerStreamCarriesWebSocket is the desktop path: websockify bytes
// arrive as Docker exec frames, and the handshake must still complete.
func TestDockerStreamCarriesWebSocket(t *testing.T) {
	t.Parallel()
	banner := []byte("RFB 003.008\n")
	got := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		if err := conn.Write(r.Context(), websocket.MessageBinary, banner); err != nil {
			return
		}
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		got <- append([]byte(nil), data...)
	}))
	t.Cleanup(upstream.Close)

	raw, err := net.Dial("tcp", upstream.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	used := false
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			if used {
				return nil, io.ErrClosedPipe
			}
			used = true
			return &dockerStream{c: &framingConn{Conn: raw}}, nil
		},
		DisableKeepAlives: true,
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws://127.0.0.1:6080/", &websocket.DialOptions{
		Subprotocols: []string{"binary"},
		HTTPClient:   client,
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial through docker stream: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()
	_, data, err := conn.Read(ctx)
	if err != nil || string(data) != string(banner) {
		t.Fatalf("banner %q err=%v", data, err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-got:
		if string(msg) != "ping" {
			t.Fatalf("upstream got %q", msg)
		}
	case <-ctx.Done():
		t.Fatal("upstream read timed out")
	}
}

// framingConn presents each TCP read as one Docker stdout frame.
type framingConn struct {
	net.Conn
	mu      sync.Mutex
	pending []byte
}

func (f *framingConn) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pending) == 0 {
		buf := make([]byte, 4096)
		n, err := f.Conn.Read(buf)
		if n > 0 {
			f.pending = muxFrame(1, buf[:n])
		}
		if len(f.pending) == 0 {
			if err == nil {
				err = io.EOF
			}
			return 0, err
		}
	}
	n := copy(p, f.pending)
	f.pending = f.pending[n:]
	return n, nil
}

func TestDialAgentExecRejectsBadID(t *testing.T) {
	t.Parallel()
	drv, err := NewDriver(Options{BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "a/b", `a\b`} {
		if _, err := drv.dialAgentExec(context.Background(), id); err == nil {
			t.Fatalf("id %q: expected error", id)
		}
	}
}

func TestNewDriverRejectsBaseURLWithoutHost(t *testing.T) {
	t.Parallel()
	if _, err := NewDriver(Options{BaseURL: "http:///v1.45"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestDialAgentExecTunnelsAndRejectsDockerErrors(t *testing.T) {
	t.Parallel()

	t.Run("relays bytes and pins the command", func(t *testing.T) {
		t.Parallel()
		gotBody := make(chan []byte, 1)
		drv := newExecDriver(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/cid/exec"):
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read body: %v", err)
				}
				gotBody <- body
				_, _ = w.Write([]byte(`{"Id":"exec-1"}`))
			case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec/exec-1/start"):
				// Docker reads the start JSON before upgrading. Leave it unread
				// and the relay would treat that body as agent stdin.
				_, _ = io.Copy(io.Discard, r.Body)
				hj, ok := w.(http.Hijacker)
				if !ok {
					t.Errorf("no hijack")
					return
				}
				conn, rw, err := hj.Hijack()
				if err != nil {
					t.Errorf("hijack: %v", err)
					return
				}
				defer func() { _ = conn.Close() }()
				var header bytes.Buffer
				header.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
				header.Write(muxFrame(1, []byte("pre")))
				if _, err := rw.Write(header.Bytes()); err != nil {
					t.Errorf("write upgrade: %v", err)
					return
				}
				if err := rw.Flush(); err != nil {
					t.Errorf("flush: %v", err)
					return
				}
				buf := make([]byte, 2)
				if _, err := io.ReadFull(rw, buf); err != nil {
					return
				}
				if _, err := rw.Write(muxFrame(1, append([]byte(nil), buf...))); err != nil {
					return
				}
				_ = rw.Flush()
			default:
				http.NotFound(w, r)
			}
		})
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		conn, err := drv.dialAgentExec(ctx, "cid")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		got := make([]byte, 3)
		if _, err := io.ReadFull(conn, got); err != nil || string(got) != "pre" {
			t.Fatalf("prefix = %q err=%v", got, err)
		}
		if _, err := conn.Write([]byte("xy")); err != nil {
			t.Fatal(err)
		}
		echo := make([]byte, 2)
		if _, err := io.ReadFull(conn, echo); err != nil || string(echo) != "xy" {
			t.Fatalf("echo = %q err=%v", echo, err)
		}
		var body []byte
		select {
		case body = <-gotBody:
		case <-ctx.Done():
			t.Fatal("exec create body was not captured")
		}
		var created struct {
			Cmd []string `json:"Cmd"`
			Tty bool     `json:"Tty"`
		}
		if err := json.Unmarshal(body, &created); err != nil {
			t.Fatal(err)
		}
		if created.Tty || len(created.Cmd) != 3 || created.Cmd[0] != "/usr/bin/python3" || created.Cmd[1] != "-c" {
			t.Fatalf("cmd = %#v", created.Cmd)
		}
		if strings.Contains(created.Cmd[2], "os.environ") || strings.Contains(created.Cmd[2], "BOTBOX") || !strings.Contains(created.Cmd[2], "127.0.0.1") {
			t.Fatal("relay script is not the fixed loopback splice")
		}
	})

	t.Run("create failure", func(t *testing.T) {
		t.Parallel()
		drv := newExecDriver(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"engine down"}`))
		})
		_, err := drv.dialAgentExec(context.Background(), "cid")
		if err == nil || !strings.Contains(err.Error(), "engine down") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("start rejected", func(t *testing.T) {
		t.Parallel()
		drv := newExecDriver(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/start") {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"message":"bad start"}`))
				return
			}
			_, _ = w.Write([]byte(`{"Id":"exec-1"}`))
		})
		_, err := drv.dialAgentExec(context.Background(), "cid")
		if err == nil || !strings.Contains(err.Error(), "HTTP 400") || !strings.Contains(err.Error(), "bad start") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("missing exec id", func(t *testing.T) {
		t.Parallel()
		drv := newExecDriver(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{}`))
		})
		_, err := drv.dialAgentExec(context.Background(), "cid")
		if err == nil || !strings.Contains(err.Error(), "no exec id") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestDialVNCTargetsWebsockify(t *testing.T) {
	t.Parallel()
	var body []byte
	drv := newExecDriver(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/exec") && !strings.HasSuffix(r.URL.Path, "/start") {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read exec body: %v", err)
			}
			body = raw
			_, _ = w.Write([]byte(`{"Id":"exec-vnc"}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"stop"}`))
	})
	_, err := drv.DialVNC(context.Background(), compute.EnvID("cid"))
	if err == nil || !strings.Contains(err.Error(), "stop") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(string(body), "6080") || strings.Contains(string(body), "9090") || strings.Contains(string(body), "os.environ") {
		t.Fatalf("script = %s", body)
	}
	if _, err = drv.DialVNC(context.Background(), compute.EnvID(`../cid`)); err == nil {
		t.Fatal("path-like sandbox id was accepted")
	}
}

func TestDialAgentGRPCChoosesLoopbackOrTunnel(t *testing.T) {
	t.Parallel()

	t.Run("loopback does not exec", func(t *testing.T) {
		t.Parallel()
		var hits int
		drv := newExecDriver(t, func(w http.ResponseWriter, r *http.Request) {
			hits++
			http.NotFound(w, r)
		})
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = ln.Close() }()
		accepted := make(chan struct{}, 1)
		go func() {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			time.Sleep(100 * time.Millisecond)
			_ = conn.Close()
		}()
		client, err := drv.dialAgentGRPC(context.Background(), compute.EnvID("cid"), ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = client.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = agentv1.NewAgentServiceClient(client).Ping(ctx, &agentv1.PingRequest{})
		select {
		case <-accepted:
		default:
			t.Fatal("loopback port was not dialed")
		}
		if hits != 0 {
			t.Fatalf("exec requests = %d", hits)
		}
	})

	t.Run("bridge address opens an exec", func(t *testing.T) {
		t.Parallel()
		var mu sync.Mutex
		var path string
		drv := newExecDriver(t, func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			path = r.URL.Path
			mu.Unlock()
			if strings.HasSuffix(r.URL.Path, "/start") {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"message":"stop"}`))
				return
			}
			_, _ = w.Write([]byte(`{"Id":"exec-1"}`))
		})
		client, err := drv.dialAgentGRPC(context.Background(), compute.EnvID("cid"), "10.255.255.2:9090")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = client.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err = agentv1.NewAgentServiceClient(client).Ping(ctx, &agentv1.PingRequest{Caller: "x"})
		if err == nil {
			t.Fatal("expected tunnel failure")
		}
		mu.Lock()
		defer mu.Unlock()
		if !strings.Contains(path, "/exec/exec-1/start") {
			t.Fatalf("last path = %q, ping err = %v", path, err)
		}
	})
}

func TestLiveExecPing(t *testing.T) {
	id := os.Getenv("BOTBOX_LIVE_DOCKER_AGENT")
	if id == "" {
		t.Skip()
	}
	// A proxy config makes the driver read the sandbox token. The values are
	// not used; this test only checks the tunnel and the token check.
	drv, err := NewDriver(Options{Proxy: &ProxyOptions{Image: "unused", DatabaseURL: "unused", KeySpec: "unused"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, rpcCtx, closeFn, err := drv.agentClient(ctx, compute.EnvID(id))
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	resp, err := client.Ping(rpcCtx, &agentv1.PingRequest{Caller: "dial-check"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetStatus() != "ok" || resp.GetHostname() == "" {
		t.Fatalf("status=%q hostname=%q", resp.GetStatus(), resp.GetHostname())
	}
}

func newExecDriver(t *testing.T, h http.HandlerFunc) *Driver {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	drv, err := NewDriver(Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return drv
}

func readStream(t *testing.T, chunks [][]byte) string {
	t.Helper()
	got, err := io.ReadAll(&dockerStream{c: &chunkConn{chunks: chunks}})
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

func muxFrame(stream byte, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	out[0] = stream
	//nolint:gosec // fixtures in this file are bounded to a few bytes
	binary.BigEndian.PutUint32(out[4:], uint32(len(payload)))
	copy(out[8:], payload)
	return out
}

func splitBytes(b []byte) [][]byte {
	out := make([][]byte, len(b))
	for i := range b {
		out[i] = []byte{b[i]}
	}
	return out
}

type chunkConn struct {
	chunks [][]byte
	idx    int
	off    int
}

func (c *chunkConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if c.idx >= len(c.chunks) {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[c.idx][c.off:])
	c.off += n
	if c.off >= len(c.chunks[c.idx]) {
		c.idx++
		c.off = 0
	}
	return n, nil
}

func (c *chunkConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *chunkConn) Close() error                     { return nil }
func (c *chunkConn) LocalAddr() net.Addr              { return chunkAddr("local") }
func (c *chunkConn) RemoteAddr() net.Addr             { return chunkAddr("remote") }
func (c *chunkConn) SetDeadline(time.Time) error      { return nil }
func (c *chunkConn) SetReadDeadline(time.Time) error  { return nil }
func (c *chunkConn) SetWriteDeadline(time.Time) error { return nil }

type chunkAddr string

func (a chunkAddr) Network() string { return "tcp" }
func (a chunkAddr) String() string  { return string(a) }
