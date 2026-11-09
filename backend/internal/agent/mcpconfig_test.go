package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/agent"
	"github.com/dunkeltech/botbox/backend/internal/desk"
)

func mustType(t *testing.T) desk.Action {
	t.Helper()
	act, err := desk.Parse("type", []byte(`{"text":"Ada","delay_ms":40}`))
	if err != nil {
		t.Fatal(err)
	}
	return act
}

func TestInstallDesktopMCPMergesWithoutDroppingExistingConfig(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codex, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codex, "config.toml"), []byte("model = \"fixture\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agy := filepath.Join(home, ".gemini", "config")
	if err := os.MkdirAll(agy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agy, "mcp_config.json"), []byte("{\"mcpServers\":{\"other\":{\"url\":\"http://127.0.0.1:1\"}}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := agent.InstallDesktopMCP("claude", home)
	if err != nil || !strings.Contains(path, "desktop-mcp.json") {
		t.Fatal(err)
	}
	claude, err := os.ReadFile(path) // #nosec G304 -- path is the config InstallDesktopMCP wrote inside t.TempDir
	if err != nil || !strings.Contains(string(claude), agent.DesktopMCPURL) || !strings.Contains(string(claude), agent.TeamMCPURL) || !strings.Contains(string(claude), agent.MemoryMCPURL) {
		t.Fatalf("claude config %s %v", claude, err)
	}
	if _, err = agent.InstallDesktopMCP("codex", home); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(codex, "config.toml")) // #nosec G304 -- config written inside t.TempDir
	if err != nil || !strings.Contains(string(raw), "model = \"fixture\"") || !strings.Contains(string(raw), agent.DesktopMCPURL) || !strings.Contains(string(raw), agent.TeamMCPURL) || !strings.Contains(string(raw), agent.MemoryMCPURL) {
		t.Fatalf("codex %s", raw)
	}
	if _, err = agent.InstallDesktopMCP("agy", home); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(agy, "mcp_config.json")) // #nosec G304 -- config written inside t.TempDir
	if err != nil || !strings.Contains(string(raw), "other") || !strings.Contains(string(raw), "botbox-desktop") || !strings.Contains(string(raw), agent.TeamMCPURL) || !strings.Contains(string(raw), agent.MemoryMCPURL) {
		t.Fatalf("agy %s", raw)
	}
	if _, err = agent.InstallDesktopMCP("claude", ""); err == nil {
		t.Fatal("empty home was accepted")
	}
}

func TestInstallBotMCPServersRegistersAssignedSlugs(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	path, err := agent.InstallBotMCPServers("claude", home, []string{"linear", "sentry"})
	if err != nil {
		t.Fatal(err)
	}
	rawClaude, err := os.ReadFile(path) // #nosec G304 -- config written inside t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawClaude), "http://egress:8081/mcp/linear") || !strings.Contains(string(rawClaude), "http://egress:8081/mcp/sentry") {
		t.Fatalf("claude MCP config missing slugs: %s", rawClaude)
	}
	if _, err = agent.InstallBotMCPServers("codex", home, []string{"linear"}); err != nil {
		t.Fatal(err)
	}
	rawCodex, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml")) // #nosec G304 -- config written inside t.TempDir
	if err != nil || !strings.Contains(string(rawCodex), "[mcp_servers.mcp_linear]") {
		t.Fatalf("codex MCP config missing slug: %s %v", rawCodex, err)
	}
	if _, err = agent.InstallBotMCPServers("agy", home, []string{"linear"}); err != nil {
		t.Fatal(err)
	}
	rawAgy, err := os.ReadFile(filepath.Join(home, ".gemini", "config", "mcp_config.json")) // #nosec G304 -- config written inside t.TempDir
	if err != nil || !strings.Contains(string(rawAgy), "mcp-linear") {
		t.Fatalf("agy MCP config missing slug: %s %v", rawAgy, err)
	}
}
