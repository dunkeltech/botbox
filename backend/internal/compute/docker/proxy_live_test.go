package docker_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/compute/docker"
	"github.com/dunkeltech/botbox/backend/internal/compute/vncrelay"
	"github.com/dunkeltech/botbox/backend/internal/testutil/pgtest"
	"github.com/google/uuid"
)

func TestProxyRealDockerIsolation(t *testing.T) {
	if os.Getenv("BOTBOX_DOCKER_SMOKE") != "1" {
		t.Skip("set BOTBOX_DOCKER_SMOKE=1 after building sandbox-image and proxy-image")
	}
	pool := pgtest.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dsn, err := url.Parse(pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	dsn.Host = "host.docker.internal:" + dsn.Port()
	workspace, bot := uuid.NewString(), uuid.NewString()
	proxyImage := os.Getenv("BOTBOX_DOCKER_SMOKE_PROXY_IMAGE")
	if proxyImage == "" {
		proxyImage = "botbox/egress-proxy:dev"
	}
	driver, err := docker.NewDriver(docker.Options{Proxy: &docker.ProxyOptions{Image: proxyImage, DatabaseURL: dsn.String(), KeySpec: strings.Repeat("01", 32)}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := driver.CreateEnvironment(ctx, compute.BotConfig{WorkspaceID: workspace, BotID: bot})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = driver.DestroyEnvironment(cleanup, id)
		// Only resources with this test's random identity are removed.
		for _, args := range [][]string{{"network", "rm", "botbox-net-" + bot}, {"volume", "rm", "botbox-home-" + bot}} {
			_ = exec.CommandContext(cleanup, "docker", args...).Run() //nolint:gosec // fixed commands and random test-owned resource names
		}
	})
	proxyName := "botbox-egress-" + bot
	//nolint:gosec // the inspected container belongs to this test.
	networkJSON, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .NetworkSettings.Networks}}", proxyName).Output()
	if err != nil {
		t.Fatalf("inspect test proxy networks: %v", err)
	}
	var networkAddresses map[string]struct{ IPAddress string }
	if err := json.Unmarshal(networkJSON, &networkAddresses); err != nil {
		t.Fatal(err)
	}
	internalIP := networkAddresses["botbox-net-"+bot].IPAddress
	if internalIP == "" || internalIP == networkAddresses["bridge"].IPAddress {
		t.Fatal("missing dedicated proxy interface")
	}
	waitContainerLog(t, ctx, proxyName, `"addr":"`+internalIP+`:8081"`)
	if err = driver.StartEnvironment(ctx, id); err != nil {
		t.Fatal(err)
	}
	waitContainerLog(t, ctx, string(id), "agent entered RUNNING state")
	waitContainerLog(t, ctx, string(id), "websockify entered RUNNING state")
	checkDockerDesktopViewer(t, ctx, driver, id, workspace, bot)
	agentCtx, agentCancel := context.WithTimeout(ctx, 8*time.Second)
	data, readErr := driver.ReadFile(agentCtx, id, "/home/bot/.botbox-ca-env")
	agentCancel()
	if readErr != nil || !strings.Contains(string(data), "SSL_CERT_FILE") {
		t.Fatalf("authenticated agent transport unavailable: %v", readErr)
	}
	restarted, err := docker.NewDriver(docker.Options{Proxy: &docker.ProxyOptions{Image: proxyImage, DatabaseURL: dsn.String(), KeySpec: strings.Repeat("01", 32)}})
	if err != nil {
		t.Fatal(err)
	}
	data, readErr = restarted.ReadFile(ctx, id, "/home/bot/.botbox-ca-env")
	if readErr != nil || len(data) == 0 {
		t.Fatalf("agent token recovery after API restart: %v", readErr)
	}
	if err = driver.CLIProxyReady(ctx, id); err != nil {
		t.Fatal(err)
	}
	script := `set -euo pipefail
 test -z "${DATABASE_URL:-}" && test -z "${BOTBOX_SECRETS_KEK:-}"
 source /home/bot/.botbox-ca-env
 test -f "$SSL_CERT_FILE" && test -f "$NODE_EXTRA_CA_CERTS"
 curl --noproxy '*' --fail --silent http://egress:8081/healthz >/dev/null
 certutil -L -n botbox-bot-ca -d sql:/home/bot/.pki/nssdb >/dev/null
 status=$(curl --silent --proxy http://egress:8081 --connect-timeout 5 -o /dev/null -w '%{http_connect}' https://api.openai.com/ || true)
 test "$status" = 403
 printf 'proxy isolation and CA startup verified\n'
 `
	//nolint:gosec // fixed verification program inside the test-owned sandbox
	command := exec.CommandContext(ctx, "docker", "exec", string(id), "bash", "-c", script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("sandbox proxy check: %v %s", err, output)
	}
}

func checkDockerDesktopViewer(t *testing.T, ctx context.Context, driver *docker.Driver, id compute.EnvID, workspace, bot string) {
	t.Helper()
	signer, err := vncrelay.NewSigner([]byte("synthetic-key-for-live-tests-only"))
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := signer.Sign(vncrelay.TicketClaims{WorkspaceID: workspace, BotID: bot, UserID: "viewer", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	relay := httptest.NewServer(vncrelay.NewRelayHandler(signer, func(ctx context.Context, _, _ string) (string, error) {
		return driver.GetVNCStreamURL(ctx, id)
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), &vncrelay.RelayOptions{
		DialUpstream: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return driver.DialVNC(ctx, id)
		},
	}))
	defer relay.Close()
	socketURL := "ws" + strings.TrimPrefix(relay.URL, "http") + "?workspace_id=" + workspace + "&bot_id=" + bot + "&ticket=" + ticket
	conn, resp, err := websocket.Dial(ctx, socketURL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	stream := websocket.NetConn(ctx, conn, websocket.MessageBinary)
	for _, step := range []struct {
		server string
		client string
	}{
		{"RFB 003.008\n", "RFB 003.008\n"},
		{"\x01\x01", "\x01"},
		{"\x00\x00\x00\x00", "\x01"},
	} {
		got := make([]byte, len(step.server))
		if _, err := io.ReadFull(stream, got); err != nil || string(got) != step.server {
			t.Fatalf("desktop handshake got %q, want %q: %v", got, step.server, err)
		}
		if _, err := io.WriteString(stream, step.client); err != nil {
			t.Fatal(err)
		}
	}
	var dimensions [4]byte
	if _, err := io.ReadFull(stream, dimensions[:]); err != nil || dimensions == [4]byte{} {
		t.Fatalf("viewer did not receive desktop dimensions: %v", err)
	}
}

func waitContainerLog(t *testing.T, ctx context.Context, id, ready string) {
	t.Helper()
	// Read a readiness event instead of sleeping or polling wall-clock time.
	command := exec.CommandContext(ctx, "docker", "logs", "--follow", id) //nolint:gosec // test-owned container identity
	reader, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), ready) {
			return
		}
	}
	t.Fatalf("container exited before %s", ready)
}
