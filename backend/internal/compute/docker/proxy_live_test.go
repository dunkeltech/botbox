package docker_test

import (
	"bufio"
	"context"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/compute/docker"
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
	driver, err := docker.NewDriver(docker.Options{Proxy: &docker.ProxyOptions{Image: "botbox/egress-proxy:dev", DatabaseURL: dsn.String(), KeySpec: strings.Repeat("01", 32)}})
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
	waitContainerLog(t, ctx, "botbox-egress-"+bot, "egress proxy listening")
	if err = driver.StartEnvironment(ctx, id); err != nil {
		t.Fatal(err)
	}
	waitContainerLog(t, ctx, string(id), "agent entered RUNNING state")
	agentCtx, agentCancel := context.WithTimeout(ctx, 8*time.Second)
	data, readErr := driver.ReadFile(agentCtx, id, "/home/bot/.botbox-ca-env")
	agentCancel()
	if readErr != nil || !strings.Contains(string(data), "SSL_CERT_FILE") {
		t.Fatalf("authenticated agent transport unavailable: %v", readErr)
	}
	restarted, err := docker.NewDriver(docker.Options{Proxy: &docker.ProxyOptions{Image: "botbox/egress-proxy:dev", DatabaseURL: dsn.String(), KeySpec: strings.Repeat("01", 32)}})
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
