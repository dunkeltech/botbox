// SPDX-License-Identifier: AGPL-3.0-only

package docker_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeLauncherSelectsMatchingPlaceholder(t *testing.T) {
	script := filepath.Join("..", "..", "..", "..", "images", "sandbox", "claude-launch.sh")
	//nolint:gosec // synthetic credential used to verify redaction
	secret := "sk-ant-api03-SUPERSECRETVALUE"
	cases := []struct {
		name    string
		mode    string
		preset  string
		wantKey string
		wantTok string
		fail    bool
	}{
		{name: "oauth", mode: "oauth", wantTok: "botbox-placeholder"},
		{name: "api key", mode: "api_key", wantKey: "botbox-placeholder"},
		{name: "keeps engine selection", mode: "api_key", preset: "oauth", wantTok: "botbox-placeholder"},
		{name: "unavailable", mode: "unavailable", fail: true},
		{name: "none", mode: "none", fail: true},
		{name: "rejects credential body", mode: secret, fail: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"userID":"kept","theme":"light"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			preset := ""
			if tc.preset == "oauth" {
				preset = "export CLAUDE_CODE_OAUTH_TOKEN=botbox-placeholder\n"
			}
			command := exec.Command("bash", "-c", `
set -euo pipefail
source "$SCRIPT"
botbox_fetch_auth_mode() { printf '%s' "$MODE"; }
`+preset+`
if botbox_prepare_claude; then printf 'ok\n'; else printf 'fail\n'; fi
printf 'token=%s\n' "${CLAUDE_CODE_OAUTH_TOKEN:-}"
printf 'key=%s\n' "${ANTHROPIC_API_KEY:-}"
printf 'base=%s\n' "${ANTHROPIC_BASE_URL:-}"
printf 'noproxy=%s\n' "${NO_PROXY:-}"
printf 'telemetry=%s\n' "${DISABLE_TELEMETRY:-}"
`)
			command.Env = []string{
				"PATH=" + os.Getenv("PATH"),
				"HOME=" + home,
				"SCRIPT=" + script,
				"MODE=" + tc.mode,
				"HTTP_PROXY=http://egress:8081",
				"NO_PROXY=localhost,127.0.0.1,::1",
				"TMPDIR=" + os.TempDir(),
			}
			out, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("launcher: %v %s", err, out)
			}
			text := string(out)
			if strings.Contains(text, secret) {
				t.Fatalf("launcher printed credential material: %s", text)
			}
			if tc.fail {
				if !strings.Contains(text, "fail") || strings.Contains(text, "token=botbox-placeholder") && tc.preset == "" || strings.Contains(text, "key=botbox-placeholder") {
					t.Fatalf("failure path exported a placeholder: %s", text)
				}
				return
			}
			if !strings.Contains(text, "ok") || !strings.Contains(text, "token="+tc.wantTok) || !strings.Contains(text, "key="+tc.wantKey) {
				t.Fatalf("selection: %s", text)
			}
			if tc.preset == "" {
				if !strings.Contains(text, "base=http://egress:8081/llm/anthropic") || !strings.Contains(text, "noproxy=localhost,127.0.0.1,::1,egress") || !strings.Contains(text, "telemetry=1") {
					t.Fatalf("proxy environment: %s", text)
				}
				//nolint:gosec // reads only the test-owned temporary home
				raw, readErr := os.ReadFile(filepath.Join(home, ".claude.json"))
				if readErr != nil {
					t.Fatal(readErr)
				}
				var saved map[string]any
				if json.Unmarshal(raw, &saved) != nil || saved["userID"] != "kept" || saved["hasCompletedOnboarding"] != true || saved["theme"] != "light" {
					t.Fatalf("onboarding file: %s", raw)
				}
				projects, _ := saved["projects"].(map[string]any)
				trusted := false
				for _, rawProject := range projects {
					project, _ := rawProject.(map[string]any)
					if project["hasTrustDialogAccepted"] == true {
						trusted = true
					}
				}
				if !trusted {
					t.Fatalf("working directory was not trusted: %s", raw)
				}
				approved, _ := saved["customApiKeyResponses"].(map[string]any)["approved"].([]any)
				found := false
				for _, item := range approved {
					if item == "botbox-placeholder" {
						found = true
					}
					if item == secret {
						t.Fatal("onboarding file stored a credential")
					}
				}
				if !found {
					t.Fatalf("placeholder was not approved: %s", raw)
				}
			}
		})
	}
}

func TestClaudeLauncherInstallKeepsOfficialCLI(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "lib", "node_modules", "@anthropic-ai", "claude-code", "bin")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(realDir, "claude.exe")
	const official = "\x7fELFofficial-native"
	//nolint:gosec // the test-owned CLI fixture needs owner execute permission
	if err := os.WriteFile(real, []byte(official), 0o700); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(binDir, "claude")
	if err := os.Symlink(real, dest); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join("..", "..", "..", "..", "images", "sandbox", "claude-launch.sh")
	installer := filepath.Join("..", "..", "..", "..", "images", "sandbox", "install-claude-launcher.sh")
	//nolint:gosec // fixed repository installer and test-owned destination paths
	command := exec.Command("bash", installer, dest, launcher)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("install launcher: %v %s", err, out)
	}
	//nolint:gosec // reads only the test-owned native CLI fixture
	kept, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != official {
		t.Fatalf("official CLI was replaced: %s", kept)
	}
	info, err := os.Lstat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("launcher is still the npm symlink")
	}
	//nolint:gosec // reads only the test-owned launcher destination
	installed, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(installed), "botbox_prepare_claude") || strings.Contains(string(kept), "botbox_prepare_claude") {
		t.Fatal("launcher and official CLI were not separated")
	}
	dockerfile, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "images", "sandbox", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(dockerfile), "COPY images/sandbox/claude-launch.sh /usr/local/bin/claude") {
		t.Fatal("Dockerfile copies the launcher onto the npm symlink")
	}
	if !strings.Contains(string(dockerfile), "install-claude-launcher.sh") {
		t.Fatal("Dockerfile does not use the symlink-safe launcher install")
	}
	if !strings.Contains(string(dockerfile), "claude native binary missing") {
		t.Fatal("Dockerfile does not reject the Claude install stub")
	}
	//nolint:gosec // reads the fixed repository launcher source
	launcherText, err := os.ReadFile(launcher)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(launcherText), "bin/claude.exe") || strings.Contains(string(launcherText), "cli.js") {
		t.Fatal("launcher does not exec the native Claude binary")
	}
}

func TestClaudeLauncherRejectsStubBinary(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "claude.exe")
	//nolint:gosec // the test-owned stub needs owner execute permission
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho stub\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join("..", "..", "..", "..", "images", "sandbox", "claude-launch.sh")
	command := exec.Command("bash", "-c", `
set -euo pipefail
source "$SCRIPT"
claude_real="$STUB"
botbox_exec_claude --version
`)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "SCRIPT=" + script, "STUB=" + stub}
	out, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "Claude native binary is not installed.") {
		t.Fatalf("stub binary was accepted: %v %s", err, out)
	}
}

func TestClaudeLauncherWithoutProxyKeepsOfficialCLI(t *testing.T) {
	script := filepath.Join("..", "..", "..", "..", "images", "sandbox", "claude-launch.sh")
	command := exec.Command("bash", "-c", `
set -euo pipefail
source "$SCRIPT"
if [[ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" && -z "${ANTHROPIC_API_KEY:-}" && -z "${ANTHROPIC_AUTH_TOKEN:-}" && -z "${HTTP_PROXY:-${http_proxy:-}}" ]]; then
  printf 'passthrough\n'
else
  printf 'wrapped\n'
fi
`)
	command.Env = []string{"SCRIPT=" + script, "PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	out, err := command.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "passthrough" {
		t.Fatalf("login helper would be wrapped: %v %s", err, out)
	}
}
