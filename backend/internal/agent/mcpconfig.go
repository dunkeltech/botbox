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

// EgressMCPBaseURL is the control-plane egress proxy MCP base URL reachable from sandboxes.
const EgressMCPBaseURL = "http://egress:8081/mcp"

// InstallDesktopMCP registers the desktop MCP server for one CLI.
// A failed write must not fail the turn: the CLI can still answer without the desktop.
// claude receives the config path for --mcp-config. The other CLIs read their home config.
func InstallDesktopMCP(cli, home string) (string, error) {
	return InstallBotMCPServers(cli, home, nil)
}

// InstallBotMCPServers registers the desktop MCP server plus any assigned proxy MCP server
// slugs (pointing to http://egress:8081/mcp/<slug>) for one CLI without placing secrets in the sandbox.
func InstallBotMCPServers(cli, home string, slugs []string) (string, error) {
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
		servers := map[string]any{
			"botbox-desktop": map[string]string{"type": "http", "url": DesktopMCPURL},
		}
		for _, slug := range slugs {
			if s := strings.TrimSpace(slug); s != "" {
				servers["mcp-"+s] = map[string]string{"type": "http", "url": EgressMCPBaseURL + "/" + s}
			}
		}
		body, err := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
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
		return "", writeCodexMCP(path, slugs)
	case "agy":
		dir := filepath.Join(home, ".gemini", "config")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		return "", writeAgyMCP(filepath.Join(dir, "mcp_config.json"), slugs)
	default:
		return "", nil
	}
}

func writeCodexMCP(path string, slugs []string) error {
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
	for _, slug := range slugs {
		s := strings.TrimSpace(slug)
		if s == "" {
			continue
		}
		secHeader := "[mcp_servers.mcp_" + strings.ReplaceAll(s, "-", "_") + "]"
		secBlock := secHeader + "\nurl = \"" + EgressMCPBaseURL + "/" + s + "\"\n"
		if strings.Contains(text, secHeader) {
			text = replaceTOMLSection(text, secHeader, secBlock)
		} else {
			if text != "" && !strings.HasSuffix(text, "\n") {
				text += "\n"
			}
			text += secBlock
		}
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

func writeAgyMCP(path string, slugs []string) error {
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
	for k := range servers {
		if strings.HasPrefix(k, "mcp-") {
			delete(servers, k)
		}
	}
	servers["botbox-desktop"] = map[string]string{"url": DesktopMCPURL}
	for _, slug := range slugs {
		if s := strings.TrimSpace(slug); s != "" {
			servers["mcp-"+s] = map[string]string{"url": EgressMCPBaseURL + "/" + s}
		}
	}
	doc["mcpServers"] = servers
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o600) // #nosec G703 -- path is the agy MCP config under the sandbox home
}
