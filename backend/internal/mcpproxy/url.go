// SPDX-License-Identifier: AGPL-3.0-only

// Package mcpproxy implements control-plane MCP server discovery, credential
// injection, per-bot command policy enforcement (allow/ask/deny), and audit logging.
package mcpproxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/dunkeltech/botbox/backend/internal/egresspolicy"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
)

const (
	// MaxToolsPerServer bounds cached tool catalogs per registered MCP server.
	MaxToolsPerServer = 200
	// MaxToolSchemaBytes bounds each tool's serialized JSON inputSchema.
	MaxToolSchemaBytes = 64 << 10
	// MaxToolDescriptionBytes bounds each tool description stored in Postgres.
	MaxToolDescriptionBytes = 4096
)

var (
	slugRegex     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$`)
	toolNameRegex = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// ToolDef represents a validated MCP tool entry discovered from or registered for a server.
type ToolDef struct {
	ToolName    string          `json:"toolName"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ValidateSlug checks and normalizes a workspace-unique MCP server slug.
func ValidateSlug(raw string) (string, bool) {
	slug := strings.ToLower(strings.TrimSpace(raw))
	if !slugRegex.MatchString(slug) {
		return "", false
	}
	return slug, true
}

// ValidateToolName checks a single MCP tool identifier.
func ValidateToolName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if !toolNameRegex.MatchString(name) {
		return "", false
	}
	return name, true
}

// FormatPrefixedToolName builds the unambiguous aggregated tool identifier for a server and tool.
// Because server slugs allow only [a-z0-9-], "__" cannot appear inside a slug.
func FormatPrefixedToolName(slug, toolName string) string {
	return "mcp__" + slug + "__" + toolName
}

// ParsePrefixedToolName splits an aggregated tool identifier into server slug and tool name.
func ParsePrefixedToolName(name string) (slug, toolName string, ok bool) {
	rest, hasPrefix := strings.CutPrefix(name, "mcp__")
	if !hasPrefix {
		return "", "", false
	}
	rawSlug, rawTool, found := strings.Cut(rest, "__")
	if !found {
		return "", "", false
	}
	validSlug, okSlug := ValidateSlug(rawSlug)
	validTool, okTool := ValidateToolName(rawTool)
	if !okSlug || !okTool {
		return "", "", false
	}
	return validSlug, validTool, true
}

// ValidateEndpointURL enforces canonical HTTPS port-443 public endpoints with no credentials or queries.
func ValidateEndpointURL(raw string) (canonicalURL string, host string, err error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > 512 || strings.ContainsAny(trimmed, " \t\r\n\x00#?") {
		return "", "", errors.New("endpointUrl must be a valid HTTPS URL (max 512 chars)")
	}
	origin, hostPart, _, origErr := mitmproxy.CanonicalHTTPSOrigin(trimmed)
	if origErr != nil {
		return "", "", origErr
	}
	normHost, ok := egresspolicy.NormalizeAndValidateHost(hostPart)
	if !ok || strings.HasPrefix(normHost, "*.") {
		return "", "", errors.New("endpointUrl host must be a valid public DNS hostname")
	}
	parsed, parseErr := url.Parse(trimmed)
	if parseErr != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", "", errors.New("endpointUrl must not include query parameters or fragments")
	}
	cleanPath := parsed.EscapedPath()
	if cleanPath == "" {
		cleanPath = "/mcp"
	}
	if !strings.HasPrefix(cleanPath, "/") || strings.Contains(cleanPath, "..") {
		return "", "", errors.New("endpointUrl path is invalid")
	}
	canonical := origin + cleanPath
	if len(canonical) > 512 {
		return "", "", errors.New("canonical endpointUrl exceeds 512 chars")
	}
	return canonical, normHost, nil
}

// NormalizeToolDefinitions validates, deduplicates, and bounds a list of MCP tool schemas.
func NormalizeToolDefinitions(defs []ToolDef) ([]ToolDef, error) {
	if len(defs) > MaxToolsPerServer {
		return nil, fmt.Errorf("at most %d tools may be registered per MCP server", MaxToolsPerServer)
	}
	seen := make(map[string]bool, len(defs))
	out := make([]ToolDef, 0, len(defs))
	for _, item := range defs {
		name, ok := ValidateToolName(item.ToolName)
		if !ok {
			return nil, fmt.Errorf("invalid MCP tool name %q", item.ToolName)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate MCP tool %q", name)
		}
		seen[name] = true

		desc := strings.TrimSpace(item.Description)
		if len(desc) > MaxToolDescriptionBytes {
			desc = desc[:MaxToolDescriptionBytes]
		}
		schema := item.InputSchema
		if len(schema) == 0 || string(schema) == "null" {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		if len(schema) > MaxToolSchemaBytes || !json.Valid(schema) {
			return nil, fmt.Errorf("invalid or oversized inputSchema for tool %q", name)
		}
		var obj map[string]any
		if err := json.Unmarshal(schema, &obj); err != nil {
			return nil, fmt.Errorf("inputSchema for tool %q must be a JSON object", name)
		}
		out = append(out, ToolDef{
			ToolName:    name,
			Description: desc,
			InputSchema: schema,
		})
	}
	return out, nil
}
