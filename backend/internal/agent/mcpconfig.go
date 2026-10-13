package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// DesktopMCPURL is the loopback MCP endpoint served by this agent process.
// It stays on 127.0.0.1 so a takeover can cancel the same process that runs xdotool.
const DesktopMCPURL = "http://127.0.0.1:9091/mcp"

// InstallDesktopMCP registers the desktop MCP server for one CLI.
// A failed write must not fail the turn: the CLI can still answer without the desktop.
// claude receives the config path for --mcp-config. The other CLIs read their home config.
func InstallDesktopMCP(cli, home string) (string, error) {
	if home == "" {
		return "", os.ErrInvalid
	}
	switch cli {
	case "claude":
		dir := filepath.Join(home, ".botbox")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		path := filepath.Join(dir, "desktop-mcp.json")
		body, err := json.MarshalIndent(map[string]any{
			"mcpServers": map[string]any{
				"botbox-desktop": map[string]string{"type": "http", "url": DesktopMCPURL},
			},
		}, "", "  ")
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil { // #nosec G703 -- path is the Claude MCP config under the sandbox home
			return "", err
		}
		return path, nil
	case "codex":
		dir := filepath.Join(home, ".codex")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		path := filepath.Join(dir, "config.toml")
		return "", writeCodexDesktop(path)
	case "agy":
		dir := filepath.Join(home, ".gemini", "config")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		return "", writeAgyDesktop(filepath.Join(dir, "mcp_config.json"))
	default:
		return "", nil
	}
}

func writeCodexDesktop(path string) error {
	existing, err := os.ReadFile(path) // #nosec G304 -- path is the Codex config under the sandbox home, not a request path
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	const header = "[mcp_servers.botbox_desktop]"
	block := header + "\nurl = \"" + DesktopMCPURL + "\"\n"
	text := string(existing)
	if strings.Contains(text, header) {
		text = replaceTOMLSection(text, header, block)
	} else {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += block
	}
	return os.WriteFile(path, []byte(text), 0o600) // #nosec G703 -- same sandbox-home Codex config path
}

func replaceTOMLSection(text, header, block string) string {
	start := strings.Index(text, header)
	if start < 0 {
		return text + block
	}
	rest := text[start+len(header):]
	next := strings.Index(rest, "\n[")
	if next < 0 {
		return text[:start] + block
	}
	return text[:start] + block + rest[next+1:]
}

func writeAgyDesktop(path string) error {
	doc := map[string]any{}
	existing, err := os.ReadFile(path) // #nosec G304 -- path is the agy MCP config under the sandbox home, not a request path
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &doc); err != nil {
			return err
		}
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers["botbox-desktop"] = map[string]string{"url": DesktopMCPURL}
	doc["mcpServers"] = servers
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o600) // #nosec G703 -- path is the agy MCP config under the sandbox home
}
