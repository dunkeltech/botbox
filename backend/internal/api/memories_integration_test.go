package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/memory"
	"github.com/google/uuid"
)

func memErrCode(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode error payload %s: %v", body, err)
	}
	return payload.Error.Code
}

func TestBotKnowledgeBaseAPIAndMemoryTools(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})

	owner, _ := h.completeSetup("kb-owner@example.com", "CorrectHorse123!", "Owner", "KB Workspace", "kb-ws")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()

	admin, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "kb-admin@example.com", "admin", "Admin", "CorrectHorse123!")
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "kb-member@example.com", "member", "Member", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "kb-viewer@example.com", "viewer", "Viewer", "CorrectHorse123!")

	otherWSResp := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other KB WS", "slug": "other-kb"}, owner.AccessToken, nil, true)
	var otherWS apigen.WorkspaceSummary
	otherWSResp.decode(t, &otherWS)
	eve, _ := h.inviteAndAcceptNewUser(owner.AccessToken, otherWS.Id, "kb-eve@example.com", "owner", "Eve", "CorrectHorse123!")

	atlas := createAPIBot(t, h, base, owner.AccessToken, "Atlas", "atlas-kb")
	mia := createAPIBot(t, h, base, member.AccessToken, "Mia", "mia-kb")

	atlasMemBase := base + "/bots/" + atlas.Id.String() + "/memories"
	miaMemBase := base + "/bots/" + mia.Id.String() + "/memories"
	randomID := uuid.NewString()

	// 1. Unauthenticated -> 401 across every endpoint.
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, atlasMemBase, nil},
		{http.MethodPost, atlasMemBase, map[string]any{"title": "T", "content": "C"}},
		{http.MethodPut, atlasMemBase + "/policy", map[string]any{"autoSave": false}},
		{http.MethodPatch, atlasMemBase + "/" + randomID, map[string]any{"title": "T2"}},
		{http.MethodDelete, atlasMemBase + "/" + randomID, nil},
		{http.MethodDelete, atlasMemBase, nil},
	} {
		if r := h.request(tc.method, tc.path, tc.body, "", nil, tc.method != http.MethodGet); r.status != http.StatusUnauthorized {
			t.Fatalf("unauth %s %s = %d, want 401", tc.method, tc.path, r.status)
		}
	}

	// 2. Cross-workspace -> 404 across every endpoint.
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, atlasMemBase, nil},
		{http.MethodPost, atlasMemBase, map[string]any{"title": "T", "content": "C"}},
		{http.MethodPut, atlasMemBase + "/policy", map[string]any{"autoSave": false}},
		{http.MethodPatch, atlasMemBase + "/" + randomID, map[string]any{"title": "T2"}},
		{http.MethodDelete, atlasMemBase + "/" + randomID, nil},
		{http.MethodDelete, atlasMemBase, nil},
	} {
		if r := h.request(tc.method, tc.path, tc.body, eve.AccessToken, nil, tc.method != http.MethodGet); r.status != http.StatusNotFound {
			t.Fatalf("cross-workspace %s %s = %d, want 404", tc.method, tc.path, r.status)
		}
	}

	// 3. RBAC: viewer & non-owner member can GET, cannot mutate Atlas's knowledge base -> 403.
	for _, tok := range []string{viewer.AccessToken, member.AccessToken} {
		if r := h.request(http.MethodGet, atlasMemBase, nil, tok, nil, false); r.status != http.StatusOK {
			t.Fatalf("read %s = %d, want 200", atlasMemBase, r.status)
		}
		for _, tc := range []struct {
			method, path string
			body         any
		}{
			{http.MethodPost, atlasMemBase, map[string]any{"title": "Rule", "content": "Use Conventional Commits."}},
			{http.MethodPut, atlasMemBase + "/policy", map[string]any{"autoSave": false}},
			{http.MethodPatch, atlasMemBase + "/" + randomID, map[string]any{"title": "Updated"}},
			{http.MethodDelete, atlasMemBase + "/" + randomID, nil},
			{http.MethodDelete, atlasMemBase, nil},
		} {
			if r := h.request(tc.method, tc.path, tc.body, tok, nil, true); r.status != http.StatusForbidden {
				t.Fatalf("forbidden %s %s = %d, want 403", tc.method, tc.path, r.status)
			}
		}
	}

	// Member owns Mia -> can manage Mia's knowledge base.
	if r := h.request(http.MethodPost, miaMemBase, map[string]any{
		"title":   "Member Bot Rule",
		"content": "Owned by member.",
		"mode":    "on_demand",
		"tags":    []string{"team"},
	}, member.AccessToken, nil, true); r.status != http.StatusCreated {
		t.Fatalf("member managing own bot = %d %s", r.status, r.body)
	}

	// 4. Invalid / missing / out-of-range inputs -> 400 validation_error.
	for _, bad := range []any{
		map[string]any{"title": "", "content": "Body"},
		map[string]any{"title": "Multi\nLine", "content": "Body"},
		map[string]any{"title": "Ok", "content": "   "},
		map[string]any{"title": "Ok", "content": "Body", "mode": "invalid"},
		map[string]any{"title": "Ok", "content": "Body", "tags": []string{"bad tag"}},
		map[string]any{"title": strings.Repeat("x", memory.MaxTitleChars+1), "content": "Body"},
	} {
		r := h.request(http.MethodPost, atlasMemBase, bad, owner.AccessToken, nil, true)
		if r.status != http.StatusBadRequest {
			t.Fatalf("invalid create (%v) = %d %s", bad, r.status, r.body)
		}
	}

	// 5. Zero-secret guard: rejects static token patterns AND real encrypted workspace, bot, and MCP secrets.
	const (
		wsSecretVal  = "ws-live-secret-token-987654321"  // #nosec G101 -- synthetic fixture value for zero-secret guard test
		botSecretVal = "bot-live-secret-token-123456789" // #nosec G101 -- synthetic fixture value for zero-secret guard test
		mcpSecretVal = "mcp-live-secret-token-555555555" // #nosec G101 -- synthetic fixture value for zero-secret guard test
	)
	if r := h.request(http.MethodPost, base+"/secrets", map[string]any{
		"name": "WS_Key", "domainMatch": "api.example.com", "authScheme": "bearer", "value": wsSecretVal,
	}, owner.AccessToken, nil, true); r.status != http.StatusCreated {
		t.Fatalf("create ws secret = %d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, base+"/bots/"+atlas.Id.String()+"/secrets", map[string]any{
		"name": "Bot_Key", "domainMatch": "git.example.com", "authScheme": "bearer", "value": botSecretVal,
	}, owner.AccessToken, nil, true); r.status != http.StatusCreated {
		t.Fatalf("create bot secret = %d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, base+"/mcp-servers", map[string]any{
		"name": "Linear", "slug": "linear-mem", "endpointUrl": "https://mcp.linear.app/mcp", "authScheme": "bearer", "secretValue": mcpSecretVal,
	}, owner.AccessToken, nil, true); r.status != http.StatusCreated {
		t.Fatalf("create mcp secret = %d %s", r.status, r.body)
	}

	for _, leaked := range []string{
		"sk-ant-api03-abcdefghijklmnop",
		wsSecretVal,
		botSecretVal,
		mcpSecretVal,
	} {
		r := h.request(http.MethodPost, atlasMemBase, map[string]any{
			"title":   "Secret leak attempt",
			"content": "Never store " + leaked + " here",
			"mode":    "on_demand",
		}, owner.AccessToken, nil, true)
		if r.status != http.StatusBadRequest {
			t.Fatalf("secret %q accepted: status=%d body=%s", leaked, r.status, r.body)
		}
		if memErrCode(t, r.body) != "memory.secret_rejected" {
			t.Fatalf("expected memory.secret_rejected, got %s", r.body)
		}
		if strings.Contains(string(r.body), leaked) {
			t.Fatalf("secret leaked in error response: %s", r.body)
		}
	}

	// 6. CRUD + filters + policy toggle + budget enforcement + limit enforcement.
	createA := h.request(http.MethodPost, atlasMemBase, map[string]any{
		"title":   "Release Checklist",
		"content": "Always run make lint test before tagging releases.",
		"mode":    "always",
		"tags":    []string{"release", "git"},
	}, owner.AccessToken, nil, true)
	if createA.status != http.StatusCreated {
		t.Fatalf("createA = %d %s", createA.status, createA.body)
	}
	var entryA apigen.BotMemory
	createA.decode(t, &entryA)

	createB := h.request(http.MethodPost, atlasMemBase, map[string]any{
		"title":   "Staging Deploy Host",
		"content": "Staging runs on internal host staging-eu-1 via ArgoCD.",
		"mode":    "on_demand",
		"tags":    []string{"infra"},
	}, admin.AccessToken, nil, true)
	if createB.status != http.StatusCreated {
		t.Fatalf("createB = %d %s", createB.status, createB.body)
	}
	var entryB apigen.BotMemory
	createB.decode(t, &entryB)

	// Filtering + pagination.
	listFiltered := h.request(http.MethodGet, atlasMemBase+"?q=ArgoCD&mode=on_demand&source=manual&tag=infra", nil, viewer.AccessToken, nil, false)
	if listFiltered.status != http.StatusOK {
		t.Fatalf("filtered list = %d %s", listFiltered.status, listFiltered.body)
	}
	var filteredResp apigen.BotMemoryListResponse
	listFiltered.decode(t, &filteredResp)
	if len(filteredResp.Entries) != 1 || filteredResp.Entries[0].Id != entryB.Id || filteredResp.FilteredCount != 1 {
		t.Fatalf("unexpected filtered list: %+v", filteredResp)
	}
	if filteredResp.Summary.TotalCount != 2 || filteredResp.Summary.AlwaysCount != 1 || filteredResp.Summary.OnDemandCount != 1 {
		t.Fatalf("unexpected summary: %+v", filteredResp.Summary)
	}

	// Update entry + promote/demote mode.
	patchB := h.request(http.MethodPatch, atlasMemBase+"/"+entryB.Id.String(), map[string]any{
		"mode": "always",
		"tags": []string{"infra", "argocd"},
	}, owner.AccessToken, nil, true)
	if patchB.status != http.StatusOK {
		t.Fatalf("patchB = %d %s", patchB.status, patchB.body)
	}
	var updatedB apigen.BotMemory
	patchB.decode(t, &updatedB)
	if updatedB.Mode != apigen.BotMemoryMode("always") || len(updatedB.Tags) != 2 {
		t.Fatalf("unexpected updatedB: %+v", updatedB)
	}

	// Always-in-context budget enforcement (> 4000 tokens -> 409 memory.always_budget_exceeded).
	for i := range 4 {
		r := h.request(http.MethodPost, atlasMemBase, map[string]any{
			"title":   fmt.Sprintf("Always Budget Filler %d", i),
			"content": strings.Repeat("rule ", 750), // ~3750 chars => ~940 tokens each
			"mode":    "always",
		}, owner.AccessToken, nil, true)
		if r.status != http.StatusCreated {
			t.Fatalf("filler %d = %d %s", i, r.status, r.body)
		}
	}
	overBudget := h.request(http.MethodPost, atlasMemBase, map[string]any{
		"title":   "Overflow Rule",
		"content": strings.Repeat("overflow ", 200),
		"mode":    "always",
	}, owner.AccessToken, nil, true)
	if overBudget.status != http.StatusConflict || memErrCode(t, overBudget.body) != "memory.always_budget_exceeded" {
		t.Fatalf("overBudget = %d %s, want 409 memory.always_budget_exceeded", overBudget.status, overBudget.body)
	}

	// 7. Chat tool execution (memory_save -> memory_search -> memory_update -> memory_delete) + DM card sync.
	ctx := context.Background()
	dmResp := h.request(http.MethodPost, base+"/bots/"+atlas.Id.String()+"/dm", nil, owner.AccessToken, nil, true)
	if dmResp.status != http.StatusOK {
		t.Fatalf("open dm = %d %s", dmResp.status, dmResp.body)
	}
	var conv apigen.ChatConversation
	dmResp.decode(t, &conv)

	msgResp := h.request(http.MethodPost, base+"/conversations/"+conv.Id.String()+"/messages", map[string]any{
		"id":      uuid.New(),
		"content": "Please remember that deployments happen on Tuesdays.",
	}, owner.AccessToken, nil, true)
	if msgResp.status != http.StatusAccepted {
		t.Fatalf("post dm = %d %s", msgResp.status, msgResp.body)
	}
	var turnID uuid.UUID
	if err := h.pool.QueryRow(ctx, `UPDATE bot_turns SET status = 'running' WHERE workspace_id = $1 AND bot_id = $2 RETURNING id`, ws, atlas.Id).Scan(&turnID); err != nil {
		t.Fatalf("activate turn: %v", err)
	}

	memSvc := memory.Service{Pool: h.pool, Ring: h.keyRing}
	scope := memory.ToolScope{WorkspaceID: ws, BotID: atlas.Id, TurnID: turnID}

	// Wrong bot turn scope fails with ErrToolUnavailable.
	if _, err := memSvc.Call(ctx, memory.ToolScope{WorkspaceID: ws, BotID: mia.Id, TurnID: turnID}, memory.ToolSearch, json.RawMessage(`{}`)); !errors.Is(err, memory.ErrToolUnavailable) {
		t.Fatalf("wrong bot turn = %v, want ErrToolUnavailable", err)
	}

	// memory_save succeeds while autoSave=true and writes a kind='memory' chat card.
	saveOut, err := memSvc.Call(ctx, scope, memory.ToolSave, json.RawMessage(`{"title":"Deploy Window","content":"Deployments happen on Tuesdays at 14:00 UTC.","tags":["deploy","ops"]}`))
	if err != nil {
		t.Fatalf("memory_save err: %v", err)
	}
	var savedTool struct {
		Saved    bool      `json:"saved"`
		MemoryID uuid.UUID `json:"memory_id"`
	}
	if err := json.Unmarshal([]byte(saveOut), &savedTool); err != nil || !savedTool.Saved || savedTool.MemoryID == uuid.Nil {
		t.Fatalf("unexpected memory_save output %s %v", saveOut, err)
	}

	// Verify chat history includes kind='memory' card with full payload.
	histResp := h.request(http.MethodGet, base+"/conversations/"+conv.Id.String()+"/messages", nil, owner.AccessToken, nil, false)
	if histResp.status != http.StatusOK {
		t.Fatalf("chat history = %d %s", histResp.status, histResp.body)
	}
	var hist apigen.ChatHistory
	histResp.decode(t, &hist)
	var memMsg *apigen.ChatMessage
	for i := range hist.Messages {
		if hist.Messages[i].Kind == apigen.ChatMessageKindMemory && hist.Messages[i].Memory != nil && hist.Messages[i].Memory.MemoryId == savedTool.MemoryID {
			memMsg = &hist.Messages[i]
			break
		}
	}
	if memMsg == nil || memMsg.Memory.Deleted || memMsg.Memory.Title != "Deploy Window" {
		t.Fatalf("missing kind=memory card in chat history: %+v", hist.Messages)
	}

	// FormatPromptContext injects always-in-context entries and omits secrets.
	promptCtx, err := memSvc.FormatPromptContext(ctx, ws, atlas.Id, turnID, true)
	if err != nil {
		t.Fatalf("FormatPromptContext err: %v", err)
	}
	if !strings.Contains(promptCtx, "Release Checklist") || !strings.Contains(promptCtx, turnID.String()) {
		t.Fatalf("unexpected promptCtx: %s", promptCtx)
	}

	// memory_search finds the on_demand Deploy Window entry.
	searchOut, err := memSvc.Call(ctx, scope, memory.ToolSearch, json.RawMessage(`{"query":"Tuesdays","tag":"deploy"}`))
	if err != nil {
		t.Fatalf("memory_search err: %v", err)
	}
	if !strings.Contains(searchOut, savedTool.MemoryID.String()) {
		t.Fatalf("memory_search did not find saved entry: %s", searchOut)
	}

	// memory_update modifies title/mode and updates chat card snapshot.
	if _, err := memSvc.Call(ctx, scope, memory.ToolUpdate, json.RawMessage(fmt.Sprintf(`{"memory_id":%q,"title":"Deploy Window Updated","mode":"always"}`, savedTool.MemoryID))); err != nil {
		t.Fatalf("memory_update err: %v", err)
	}
	// memory_delete marks both memory entry and chat card deleted.
	var secondSaved struct {
		MemoryID uuid.UUID `json:"memory_id"`
	}
	secondOut, err := memSvc.Call(ctx, scope, memory.ToolSave, json.RawMessage(`{"title":"Temp Note","content":"Temporary rule to delete via tool."}`))
	if err != nil {
		t.Fatalf("second memory_save err: %v", err)
	}
	_ = json.Unmarshal([]byte(secondOut), &secondSaved)
	if _, err := memSvc.Call(ctx, scope, memory.ToolDelete, json.RawMessage(fmt.Sprintf(`{"memory_id":%q}`, secondSaved.MemoryID))); err != nil {
		t.Fatalf("memory_delete err: %v", err)
	}

	// memory_save rejects stored secret too.
	if _, err := memSvc.Call(ctx, scope, memory.ToolSave, json.RawMessage(fmt.Sprintf(`{"title":"Leak","content":"Token %s"}`, botSecretVal))); !errors.Is(err, memory.ErrSecretDetected) {
		t.Fatalf("memory_save secret = %v, want ErrSecretDetected", err)
	}

	// Turn off autoSave policy -> memory_save from chat returns ErrAutoSaveDisabled, manual POST still works.
	policyResp := h.request(http.MethodPut, atlasMemBase+"/policy", map[string]any{"autoSave": false}, owner.AccessToken, nil, true)
	if policyResp.status != http.StatusOK {
		t.Fatalf("policy = %d %s", policyResp.status, policyResp.body)
	}
	if _, err := memSvc.Call(ctx, scope, memory.ToolSave, json.RawMessage(`{"title":"Blocked","content":"Should fail when auto-save is off"}`)); !errors.Is(err, memory.ErrAutoSaveDisabled) {
		t.Fatalf("memory_save when autoSave=false = %v, want ErrAutoSaveDisabled", err)
	}

	// Deleting the memory via REST marks the chat memory card deleted.
	delResp := h.request(http.MethodDelete, atlasMemBase+"/"+savedTool.MemoryID.String(), nil, owner.AccessToken, nil, true)
	if delResp.status != http.StatusNoContent {
		t.Fatalf("delete memory = %d %s", delResp.status, delResp.body)
	}
	histAfter := h.request(http.MethodGet, base+"/conversations/"+conv.Id.String()+"/messages", nil, owner.AccessToken, nil, false)
	var hist2 apigen.ChatHistory
	histAfter.decode(t, &hist2)
	var markedDeleted bool
	for _, m := range hist2.Messages {
		if m.Kind == apigen.ChatMessageKindMemory && m.Memory != nil && m.Memory.MemoryId == savedTool.MemoryID && m.Memory.Deleted {
			markedDeleted = true
		}
	}
	if !markedDeleted {
		t.Fatal("expected deleted memory card snapshot deleted=true in chat history")
	}

	// 8. Max entries limit (200 per bot -> 409 memory.limit_reached).
	clearResp := h.request(http.MethodDelete, atlasMemBase, nil, owner.AccessToken, nil, true)
	if clearResp.status != http.StatusNoContent {
		t.Fatalf("clear = %d %s", clearResp.status, clearResp.body)
	}
	// Seed 200 entries directly in SQL so the integration test finishes quickly, then verify 201st returns 409.
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO bot_memories (workspace_id, bot_id, title, content, mode, tags, token_estimate, source_kind, source_label)
		SELECT $1, $2, 'Entry ' || g, 'Rule #' || g, 'on_demand', '{}', 4, 'manual', 'Manual'
		FROM generate_series(1, $3) AS g
	`, ws, atlas.Id, memory.MaxEntriesPerBot); err != nil {
		t.Fatalf("seed max entries: %v", err)
	}
	overLimit := h.request(http.MethodPost, atlasMemBase, map[string]any{
		"title":   "Entry 201",
		"content": "Exceeds 200 limit",
		"mode":    "on_demand",
	}, owner.AccessToken, nil, true)
	if overLimit.status != http.StatusConflict || memErrCode(t, overLimit.body) != "memory.limit_reached" {
		t.Fatalf("overLimit = %d %s, want 409 memory.limit_reached", overLimit.status, overLimit.body)
	}

	// Ensure zero secret values ever appeared in API logs.
	logText := h.logs.String()
	for _, s := range []string{wsSecretVal, botSecretVal, mcpSecretVal} {
		if strings.Contains(logText, s) {
			t.Fatalf("secret %q leaked into logs", s)
		}
	}
}
