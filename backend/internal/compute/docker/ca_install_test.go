// SPDX-License-Identifier: AGPL-3.0-only

package docker_test

import (
	"crypto/x509"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
)

func TestInstallBotCAWithoutPrivileges(t *testing.T) {
	home := filepath.Join(t.TempDir(), "bot home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	fake := `#!/bin/sh
printf '%s\n' "$*" >> "$HOME/certutil.calls"
`
	for name, body := range map[string]string{"certutil": fake, "id": "#!/bin/sh\nprintf 1000\n", "sudo": "#!/bin/sh\nexit 99\n", "update-ca-certificates": "#!/bin/sh\nexit 99\n"} {
		//nolint:gosec // executable test doubles require owner execute permission
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	ca, err := mitmproxy.NewBotCA("fixture")
	if err != nil {
		t.Fatal(err)
	}
	cert := filepath.Join(home, "public.crt")
	if err = os.WriteFile(cert, ca.CertPEM(), 0600); err != nil {
		t.Fatal(err)
	}
	script := "../../../../images/sandbox/install-bot-ca.sh"
	//nolint:gosec // fixed repository script and test-owned certificate path
	cmd := exec.Command("bash", script, cert)
	cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+bin+":"+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("unprivileged CA installation: %v %s", err, output)
	}
	envFile := filepath.Join(home, ".botbox-ca-env")
	// The entrypoint sources these assignments before spawning supervised tools.
	//nolint:gosec // fixed shell program; the test-owned path is a positional argument
	cmd = exec.Command("bash", "-c", `. "$1"; printf '%s\n' "$SSL_CERT_FILE" "$NODE_EXTRA_CA_CERTS" "$REQUESTS_CA_BUNDLE" "$GIT_SSL_CAINFO"`, "fixture", envFile)
	cmd.Env = append(os.Environ(), "HOME="+home)
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("load CA env: %v %s", err, output)
	}
	for _, path := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if !strings.HasPrefix(path, home+string(os.PathSeparator)) {
			t.Fatalf("CA path outside bot home: %s", path)
		}
		data, err := os.ReadFile(path) //nolint:gosec // generated env file is owned by this test
		if err != nil {
			t.Fatal(err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			t.Fatal("invalid CA bundle")
		}
		if !strings.Contains(string(data), string(ca.CertPEM())) {
			t.Fatal("bot certificate missing")
		}
	}
	//nolint:gosec // test-owned call log under the temporary bot home
	if out, err := os.ReadFile(filepath.Join(home, "certutil.calls")); err != nil || !strings.Contains(string(out), "botbox-bot-ca") {
		t.Fatalf("NSS trust not configured: %s %v", out, err)
	}
	//nolint:gosec // fixed repository script and test-owned missing file path
	missing := exec.Command("bash", script, filepath.Join(home, "missing.crt"))
	missing.Env = cmd.Env
	if err := missing.Run(); err == nil {
		t.Fatal("missing certificate accepted")
	}
}
