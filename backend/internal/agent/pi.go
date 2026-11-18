// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var piSessionID = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9_.-]{0,253}[a-zA-Z0-9])?$`)

func writePiConfig(dir, proxy string, slugs []string) error {
	models := map[string]any{"providers": map[string]any{
		"openrouter": map[string]string{"baseUrl": strings.TrimRight(proxy, "/") + "/llm/openrouter/v1"},
	}}
	servers := map[string]map[string]string{}
	add := func(name, url string) {
		servers[name] = map[string]string{"url": url, "exposure": "direct"}
	}
	add("botbox-desktop", DesktopMCPURL)
	add("botbox-team", TeamMCPURL)
	add("botbox-memory", MemoryMCPURL)
	for _, slug := range slugs {
		add("mcp-"+slug, EgressMCPBaseURL+"/"+slug)
	}
	for name, value := range map[string]any{
		"models.json": models,
		"mcp.json":    map[string]any{"mcpServers": servers},
	} {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil { // #nosec G703 -- filenames and temporary directory are controlled by the agent
			return err
		}
	}
	return nil
}
