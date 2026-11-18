// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/mcpproxy"
	"github.com/google/uuid"
)

type mcpRoundTripFunc func(*http.Request) (*http.Response, error)

func (f mcpRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMCPProxyWorkspaceAndBotPolicies(t *testing.T) {
	const mcpSecret = "lin_live_secret_value_abc123"
	var sawAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": "2025-03-26"}})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": []map[string]any{
				{"name": "list_issues", "description": "List issues", "inputSchema": map[string]any{"type": "object"}},
				{"name": "create_issue", "description": "Create issue", "inputSchema": map[string]any{"type": "object"}},
				{"name": "delete_issue", "description": "Delete issue", "inputSchema": map[string]any{"type": "object"}},
			}}})
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "ok " + req.Params.Name + " " + mcpSecret}},
			}})
		}
	}))
	defer upstream.Close()

	upstreamClient := &mcpproxy.UpstreamClient{
		Timeout: 5 * time.Second,
		HTTPClient: &http.Client{
			Transport: mcpRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				cloned := req.Clone(req.Context())
				cloned.URL.Scheme = "http"
				cloned.URL.Host = strings.TrimPrefix(upstream.URL, "http://")
				return http.DefaultTransport.RoundTrip(cloned)
			}),
		},
	}

	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue, mcpUpstream: upstreamClient})
	owner, _ := h.completeSetup("mcp-owner@example.com", "CorrectHorse123!", "Owner", "MCP Workspace", "mcp-ws")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()

	admin, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "mcp-admin@example.com", "admin", "Admin", "CorrectHorse123!")
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "mcp-member@example.com", "member", "Member", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "mcp-viewer@example.com", "viewer", "Viewer", "CorrectHorse123!")

	otherWSResp := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other WS", "slug": "other-mcp"}, owner.AccessToken, nil, true)
	var otherWS apigen.WorkspaceSummary
	otherWSResp.decode(t, &otherWS)
	eve, _ := h.inviteAndAcceptNewUser(owner.AccessToken, otherWS.Id, "mcp-eve@example.com", "owner", "Eve", "CorrectHorse123!")

	serversPath := base + "/mcp-servers"
	validCreate := map[string]any{
		"name":        "Linear",
		"slug":        "linear",
		"endpointUrl": "https://mcp.linear.app/mcp",
		"authScheme":  "bearer",
		"secretValue": mcpSecret,
	}

	// Unauthenticated -> 401
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		if r := h.request(m, serversPath, validCreate, "", nil, true); r.status != http.StatusUnauthorized {
			t.Fatalf("unauth %s %s = %d", m, serversPath, r.status)
		}
	}

	// Member & Viewer cannot mutate workspace MCP servers -> 403
	for _, tok := range []string{member.AccessToken, viewer.AccessToken} {
		if r := h.request(http.MethodPost, serversPath, validCreate, tok, nil, true); r.status != http.StatusForbidden {
			t.Fatalf("forbidden create = %d", r.status)
		}
	}

	// Cross-workspace -> 404
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		if r := h.request(m, serversPath, validCreate, eve.AccessToken, nil, true); r.status != http.StatusNotFound {
			t.Fatalf("cross-workspace %s = %d", m, r.status)
		}
	}

	// Invalid payloads -> 400
	invalidPayloads := []map[string]any{
		{"name": "", "slug": "linear", "endpointUrl": "https://mcp.linear.app/mcp", "authScheme": "none"},
		{"name": "Linear", "slug": "bad_slug", "endpointUrl": "https://mcp.linear.app/mcp", "authScheme": "none"},
		{"name": "Linear", "slug": "linear", "endpointUrl": "http://mcp.linear.app/mcp", "authScheme": "none"},
		{"name": "Linear", "slug": "linear", "endpointUrl": "https://127.0.0.1/mcp", "authScheme": "none"},
		{"name": "Linear", "slug": "linear", "endpointUrl": "https://mcp.linear.app:8443/mcp", "authScheme": "none"},
		{"name": "Linear", "slug": "linear", "endpointUrl": "https://mcp.linear.app/mcp", "authScheme": "bearer"},
	}
	for _, payload := range invalidPayloads {
		if r := h.request(http.MethodPost, serversPath, payload, owner.AccessToken, nil, true); r.status != http.StatusBadRequest {
			t.Fatalf("invalid create accepted (%v): %d %s", payload, r.status, r.body)
		}
	}

	// Admin creates server; secret never appears in response.
	createResp := h.request(http.MethodPost, serversPath, validCreate, admin.AccessToken, nil, true)
	if createResp.status != http.StatusCreated {
		t.Fatalf("create mcp server = %d %s", createResp.status, createResp.body)
	}
	if strings.Contains(string(createResp.body), mcpSecret) {
		t.Fatalf("secret leaked in create response: %s", createResp.body)
	}
	var created apigen.MCPServer
	createResp.decode(t, &created)

	// Duplicate slug -> 409
	if r := h.request(http.MethodPost, serversPath, validCreate, owner.AccessToken, nil, true); r.status != http.StatusConflict {
		t.Fatalf("duplicate slug = %d", r.status)
	}

	// Sync discovers tools from upstream using injected Bearer secret.
	syncPath := serversPath + "/" + created.Id.String() + "/sync"
	if r := h.request(http.MethodPost, syncPath, nil, member.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("member sync = %d", r.status)
	}
	syncResp := h.request(http.MethodPost, syncPath, nil, owner.AccessToken, nil, true)
	if syncResp.status != http.StatusOK {
		t.Fatalf("sync = %d %s", syncResp.status, syncResp.body)
	}
	if sawAuth != "Bearer "+mcpSecret {
		t.Fatalf("sync auth header = %q", sawAuth)
	}
	var synced apigen.MCPServer
	syncResp.decode(t, &synced)
	if len(synced.Tools) != 3 {
		t.Fatalf("synced tools = %d, want 3", len(synced.Tools))
	}

	// Verify GET list never returns secret to any role, and cross-workspace sync/patch/delete -> 404.
	for _, tok := range []string{owner.AccessToken, admin.AccessToken, member.AccessToken, viewer.AccessToken} {
		listResp := h.request(http.MethodGet, serversPath, nil, tok, nil, false)
		if listResp.status != http.StatusOK || strings.Contains(string(listResp.body), mcpSecret) {
			t.Fatalf("list status=%d leaked=%v", listResp.status, strings.Contains(string(listResp.body), mcpSecret))
		}
	}
	if r := h.request(http.MethodPost, syncPath, nil, eve.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("cross-workspace sync = %d", r.status)
	}

	// Bot policies: member's own bot vs owner's bot.
	atlas := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas-mcp")
	mia := createPiBot(t, h, base, member.AccessToken, "Mia", "mia-mcp")

	atlasMCPPath := base + "/bots/" + atlas.Id.String() + "/mcp"
	atlasServerPath := atlasMCPPath + "/" + created.Id.String()
	miaServerPath := base + "/bots/" + mia.Id.String() + "/mcp/" + created.Id.String()

	policyBody := map[string]any{
		"enabled":          true,
		"defaultPolicy":    "deny",
		"expectedRevision": 0,
		"toolPolicies": map[string]string{
			"list_issues":  "allow",
			"create_issue": "ask",
			"delete_issue": "deny",
		},
	}

	// Viewer and non-owner member cannot edit Atlas; member CAN edit Mia.
	if r := h.request(http.MethodPut, atlasServerPath, policyBody, viewer.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("viewer bot mcp = %d", r.status)
	}
	if r := h.request(http.MethodPut, atlasServerPath, policyBody, member.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("non-owner member bot mcp = %d", r.status)
	}
	if r := h.request(http.MethodPut, miaServerPath, policyBody, member.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("owner member bot mcp = %d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPut, atlasServerPath, policyBody, eve.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("cross-workspace bot mcp = %d", r.status)
	}

	putResp := h.request(http.MethodPut, atlasServerPath, policyBody, owner.AccessToken, nil, true)
	if putResp.status != http.StatusOK {
		t.Fatalf("put atlas mcp = %d %s", putResp.status, putResp.body)
	}
	var updatedPolicy apigen.BotMCPServerPolicy
	putResp.decode(t, &updatedPolicy)
	if updatedPolicy.Revision != 1 || updatedPolicy.ToolPolicies["list_issues"] != apigen.Allow || updatedPolicy.ToolPolicies["create_issue"] != apigen.Ask {
		t.Fatalf("unexpected bot policy: %+v", updatedPolicy)
	}

	// Stale expectedRevision -> 409 conflict
	if r := h.request(http.MethodPut, atlasServerPath, policyBody, owner.AccessToken, nil, true); r.status != http.StatusConflict {
		t.Fatalf("stale revision = %d, want 409", r.status)
	}

	// Execute via mcpproxy.Service against the same Postgres pool and verify logs API.
	svc := mcpproxy.NewService(h.pool, h.keyRing, upstreamClient, nil, nil)
	ctx := context.Background()
	exposed, err := svc.ListBotTools(ctx, ws, atlas.Id, "")
	if err != nil || len(exposed) != 2 {
		t.Fatalf("ListBotTools = %d, err=%v", len(exposed), err)
	}
	res, err := svc.CallBotTool(ctx, ws, atlas.Id, "linear", "list_issues", nil)
	if err != nil || strings.Contains(res.Text, mcpSecret) || !strings.Contains(res.Text, "[REDACTED]") {
		t.Fatalf("allowed call res=%+v err=%v", res, err)
	}
	if _, err := svc.CallBotTool(ctx, ws, atlas.Id, "linear", "delete_issue", nil); err == nil {
		t.Fatal("denied tool unexpectedly succeeded")
	}

	logsResp := h.request(http.MethodGet, atlasMCPPath+"/logs", nil, owner.AccessToken, nil, false)
	if logsResp.status != http.StatusOK {
		t.Fatalf("logs status = %d %s", logsResp.status, logsResp.body)
	}
	if strings.Contains(string(logsResp.body), mcpSecret) {
		t.Fatalf("secret leaked in logs: %s", logsResp.body)
	}
	var logs apigen.MCPCallLogListResponse
	logsResp.decode(t, &logs)
	if len(logs.Logs) != 2 {
		t.Fatalf("logs count = %d, want 2", len(logs.Logs))
	}

	// Initial tool policy with allowed commands auto-enables an unassigned server cleanly.
	luke := createPiBot(t, h, base, owner.AccessToken, "Luke", "luke-mcp")
	lukeServerPath := base + "/bots/" + luke.Id.String() + "/mcp/" + created.Id.String()
	autoEnableResp := h.request(http.MethodPut, lukeServerPath, map[string]any{
		"enabled":          false,
		"defaultPolicy":    "deny",
		"expectedRevision": 0,
		"toolPolicies": map[string]string{
			"list_issues": "allow",
		},
	}, owner.AccessToken, nil, true)
	if autoEnableResp.status != http.StatusOK {
		t.Fatalf("auto-enable put status = %d %s", autoEnableResp.status, autoEnableResp.body)
	}
	var autoEnabled apigen.BotMCPServerPolicy
	autoEnableResp.decode(t, &autoEnabled)
	if !autoEnabled.Enabled {
		t.Fatal("expected initial allowed tool policy to auto-enable server assignment")
	}

	// Delete workspace MCP server -> cascades cleanly; unknown server returns 404.
	delPath := serversPath + "/" + created.Id.String()
	if r := h.request(http.MethodDelete, delPath, nil, member.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("member delete = %d", r.status)
	}
	if r := h.request(http.MethodDelete, delPath, nil, owner.AccessToken, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("owner delete = %d", r.status)
	}
	if r := h.request(http.MethodDelete, serversPath+"/"+uuid.NewString(), nil, owner.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("missing delete = %d", r.status)
	}
}
