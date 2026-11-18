// SPDX-License-Identifier: AGPL-3.0-only

package mcpproxy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/approvals"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/mcpproxy"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/dunkeltech/botbox/backend/internal/testutil/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestValidateEndpointURL(t *testing.T) {
	valid := []struct {
		in   string
		want string
		host string
	}{
		{"https://mcp.linear.app/mcp", "https://mcp.linear.app:443/mcp", "mcp.linear.app"},
		{"https://MCP.Example.com:443", "https://mcp.example.com:443/mcp", "mcp.example.com"},
		{"https://mcp.sentry.io/v1/sse", "https://mcp.sentry.io:443/v1/sse", "mcp.sentry.io"},
	}
	for _, tc := range valid {
		gotURL, gotHost, err := mcpproxy.ValidateEndpointURL(tc.in)
		if err != nil {
			t.Fatalf("valid URL %q rejected: %v", tc.in, err)
		}
		if gotURL != tc.want || gotHost != tc.host {
			t.Fatalf("ValidateEndpointURL(%q) = (%q, %q), want (%q, %q)", tc.in, gotURL, gotHost, tc.want, tc.host)
		}
	}

	invalid := []string{
		"",
		"http://mcp.linear.app/mcp",
		"https://mcp.linear.app:8443/mcp",
		"https://127.0.0.1/mcp",
		"https://169.254.169.254/latest",
		"https://localhost/mcp",
		"https://user:pass@mcp.linear.app/mcp",
		"https://mcp.linear.app/mcp?token=abc",
		"https://mcp.linear.app/mcp#frag",
		"https://*.linear.app/mcp",
		"https://mcp.linear.app/../secret",
	}
	for _, raw := range invalid {
		if _, _, err := mcpproxy.ValidateEndpointURL(raw); err == nil {
			t.Fatalf("invalid URL %q unexpectedly accepted", raw)
		}
	}
}

func TestValidateSlugAndPrefixedTool(t *testing.T) {
	if got, ok := mcpproxy.ValidateSlug(" Linear-Prod "); !ok || got != "linear-prod" {
		t.Fatalf("ValidateSlug failed: %q %v", got, ok)
	}
	for _, bad := range []string{"", "a", "-bad", "bad-", "has_underscore", "BAD!"} {
		if _, ok := mcpproxy.ValidateSlug(bad); ok {
			t.Fatalf("ValidateSlug(%q) unexpectedly accepted", bad)
		}
	}
	full := mcpproxy.FormatPrefixedToolName("pg-readonly", "run_query")
	if full != "mcp__pg-readonly__run_query" {
		t.Fatalf("unexpected prefixed name %q", full)
	}
	slug, tool, ok := mcpproxy.ParsePrefixedToolName(full)
	if !ok || slug != "pg-readonly" || tool != "run_query" {
		t.Fatalf("ParsePrefixedToolName(%q) = (%q, %q, %v)", full, slug, tool, ok)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProxyEnforcementSecretInjectionAndApprovalGate(t *testing.T) {
	pool := pgtest.Open(t)
	ctx := context.Background()

	rawKEK := bytes.Repeat([]byte{0x42}, secrets.AES256KeySize)
	ring, err := secrets.NewKeyRing("v1", map[string][]byte{"v1": rawKEK})
	if err != nil {
		t.Fatal(err)
	}

	var userID, wsID, botID, convID, msgID uuid.UUID
	if err = pool.QueryRow(ctx, `INSERT INTO users(email,password_hash,display_name) VALUES('mcp@example.com','hash','Owner') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO workspaces(name,slug) VALUES('MCP WS','mcp-ws') RETURNING id`).Scan(&wsID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES($1,$2,'owner')`, wsID, userID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO bots(workspace_id,owner_user_id,name,avatar_seed,llm_mode,llm_provider,llm_model) VALUES($1,$2,'Atlas','atlas-01','api','openrouter','fixture') RETURNING id`, wsID, userID).Scan(&botID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO conversations(workspace_id,user_id,bot_id) VALUES($1,$2,$3) RETURNING id`, wsID, userID, botID).Scan(&convID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO messages(workspace_id,conversation_id,sender_type,sender_user_id,content) VALUES($1,$2,'user',$3,'create issue') RETURNING id`, wsID, convID, userID).Scan(&msgID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO bot_turns(workspace_id,bot_id,conversation_id,message_id,status) VALUES($1,$2,$3,$4,'running')`, wsID, botID, convID, msgID); err != nil {
		t.Fatal(err)
	}

	const secretValue = "lin_secret_token_999"
	var sawAuthHeader string
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuthHeader = r.Header.Get("Authorization")
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if decErr := json.NewDecoder(r.Body).Decode(&req); decErr != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": "2025-03-26"}})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": []map[string]any{
				{"name": "list_issues", "description": "List", "inputSchema": map[string]any{"type": "object"}},
				{"name": "create_issue", "description": "Create", "inputSchema": map[string]any{"type": "object"}},
				{"name": "delete_issue", "description": "Delete", "inputSchema": map[string]any{"type": "object"}},
			}}})
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "executed " + req.Params.Name + " leaked=" + secretValue}},
			}})
		}
	}))
	defer upstreamSrv.Close()

	// Route https://mcp.linear.app:443/mcp to our local httptest upstream without bypassing URL validation.
	upstreamClient := &mcpproxy.UpstreamClient{
		Timeout: 5 * time.Second,
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				cloned := req.Clone(req.Context())
				cloned.URL.Scheme = "http"
				cloned.URL.Host = strings.TrimPrefix(upstreamSrv.URL, "http://")
				return http.DefaultTransport.RoundTrip(cloned)
			}),
		},
	}

	svc := mcpproxy.NewService(pool, ring, upstreamClient, nil, nil)
	q := db.New(pool)

	srvID := uuid.New()
	env, err := ring.Encrypt([]byte(secretValue), secrets.SecretAAD(wsID, "mcp_server", srvID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.CreateMCPServer(ctx, db.CreateMCPServerParams{
		ID:             pgtype.UUID{Bytes: srvID, Valid: true},
		WorkspaceID:    pgtype.UUID{Bytes: wsID, Valid: true},
		Slug:           "linear",
		Name:           "Linear",
		EndpointUrl:    "https://mcp.linear.app:443/mcp",
		AuthScheme:     "bearer",
		AuthHeaderName: "Authorization",
		KekID:          env.KeyID,
		WrappedDek:     env.WrappedDEK,
		EncryptedValue: env.Ciphertext,
		MaskedPreview:  secrets.MaskedPreview(secretValue),
	}); err != nil {
		t.Fatal(err)
	}

	discovered, err := upstreamClient.DiscoverTools(ctx, "https://mcp.linear.app:443/mcp", "Authorization", "Bearer "+secretValue, secretValue)
	if err != nil || len(discovered) != 3 {
		t.Fatalf("DiscoverTools = %d, err=%v", len(discovered), err)
	}
	for _, tool := range discovered {
		if _, err = q.UpsertMCPServerTool(ctx, db.UpsertMCPServerToolParams{
			ServerID:    pgtype.UUID{Bytes: srvID, Valid: true},
			WorkspaceID: pgtype.UUID{Bytes: wsID, Valid: true},
			ToolName:    tool.ToolName,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err = q.UpsertBotMCPAssignment(ctx, db.UpsertBotMCPAssignmentParams{
		WorkspaceID:   pgtype.UUID{Bytes: wsID, Valid: true},
		BotID:         pgtype.UUID{Bytes: botID, Valid: true},
		ServerID:      pgtype.UUID{Bytes: srvID, Valid: true},
		Enabled:       true,
		DefaultPolicy: "deny",
		Revision:      1,
	}); err != nil {
		t.Fatal(err)
	}
	for name, policy := range map[string]string{"list_issues": "allow", "create_issue": "ask", "delete_issue": "deny"} {
		if _, err = q.UpsertBotMCPToolPolicy(ctx, db.UpsertBotMCPToolPolicyParams{
			WorkspaceID: pgtype.UUID{Bytes: wsID, Valid: true},
			BotID:       pgtype.UUID{Bytes: botID, Valid: true},
			ServerID:    pgtype.UUID{Bytes: srvID, Valid: true},
			ToolName:    name,
			Policy:      policy,
		}); err != nil {
			t.Fatal(err)
		}
	}

	handler := svc.Handler(wsID, botID)

	// 1. tools/list hides deny tool (delete_issue) and includes allow + ask.
	listReq := httptest.NewRequest(http.MethodPost, "/mcp/linear", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	listRec := httptest.NewRecorder()
	handler.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK || strings.Contains(listRec.Body.String(), "delete_issue") {
		t.Fatalf("tools/list leaked denied tool: code=%d body=%s", listRec.Code, listRec.Body.String())
	}
	if !strings.Contains(listRec.Body.String(), "list_issues") || !strings.Contains(listRec.Body.String(), "create_issue") {
		t.Fatalf("tools/list missing allowed/ask tools: %s", listRec.Body.String())
	}

	// 2. Sampling method is rejected with -32601.
	sampReq := httptest.NewRequest(http.MethodPost, "/mcp/linear", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"sampling/createMessage"}`))
	sampRec := httptest.NewRecorder()
	handler.ServeHTTP(sampRec, sampReq)
	if !strings.Contains(sampRec.Body.String(), "-32601") {
		t.Fatalf("sampling should be rejected: %s", sampRec.Body.String())
	}

	// 3. Direct call to denied tool fails with -32003 and logs blocked.
	denyReq := httptest.NewRequest(http.MethodPost, "/mcp/linear", strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"delete_issue","arguments":{}}}`))
	denyRec := httptest.NewRecorder()
	handler.ServeHTTP(denyRec, denyReq)
	if !strings.Contains(denyRec.Body.String(), "-32003") {
		t.Fatalf("denied tool should return -32003: %s", denyRec.Body.String())
	}

	// 4. Allowed tool injects Bearer token and scrubs reflected secret from response.
	allowReq := httptest.NewRequest(http.MethodPost, "/mcp/linear", strings.NewReader(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list_issues","arguments":{}}}`))
	allowRec := httptest.NewRecorder()
	handler.ServeHTTP(allowRec, allowReq)
	if sawAuthHeader != "Bearer "+secretValue {
		t.Fatalf("upstream did not receive injected secret header: %q", sawAuthHeader)
	}
	if strings.Contains(allowRec.Body.String(), secretValue) || !strings.Contains(allowRec.Body.String(), "[REDACTED]") {
		t.Fatalf("secret reflection not scrubbed: %s", allowRec.Body.String())
	}

	// 5. Ask-gated tool opens approval card and blocks until human approves.
	askReq := httptest.NewRequest(http.MethodPost, "/mcp/linear", strings.NewReader(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"create_issue","arguments":{"title":"Fix bug"}}}`))
	askRec := httptest.NewRecorder()
	handler.ServeHTTP(askRec, askReq)
	if !strings.Contains(askRec.Body.String(), "mcp.approval_required") {
		t.Fatalf("expected mcp.approval_required: %s", askRec.Body.String())
	}

	visible, err := q.ListVisibleApprovals(ctx, db.ListVisibleApprovalsParams{
		WorkspaceID: pgtype.UUID{Bytes: wsID, Valid: true},
		MemberID:    pgtype.UUID{Bytes: userID, Valid: true},
	})
	if err != nil || len(visible) != 1 || visible[0].Kind != "mcp_tool" {
		t.Fatalf("expected 1 mcp_tool approval, got %d (%v)", len(visible), err)
	}
	if _, err = approvals.Apply(ctx, q, visible[0], "approved", pgtype.UUID{Bytes: userID, Valid: true}, time.Now()); err != nil {
		t.Fatalf("approve mcp_tool failed: %v", err)
	}

	// 6. Retrying with exact same arguments succeeds and consumes the one-time grant.
	retryReq := httptest.NewRequest(http.MethodPost, "/mcp/linear", strings.NewReader(`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"create_issue","arguments":{"title":"Fix bug"}}}`))
	retryRec := httptest.NewRecorder()
	handler.ServeHTTP(retryRec, retryReq)
	if !strings.Contains(retryRec.Body.String(), "executed create_issue") {
		t.Fatalf("expected approved tool execution, got %s", retryRec.Body.String())
	}

	// 7. Second call requires a fresh approval because the grant was consumed.
	secondAskReq := httptest.NewRequest(http.MethodPost, "/mcp/linear", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"create_issue","arguments":{"title":"Fix bug"}}}`))
	secondAskRec := httptest.NewRecorder()
	handler.ServeHTTP(secondAskRec, secondAskReq)
	if !strings.Contains(secondAskRec.Body.String(), "mcp.approval_required") {
		t.Fatalf("expected consumed grant to require fresh approval: %s", secondAskRec.Body.String())
	}
}
