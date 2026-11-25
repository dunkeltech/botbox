// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/google/uuid"
)

func TestBotAskBeforeConfig(t *testing.T) {
	h := newTestHarness(t, nil, harnessOpts{})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member@example.com", "member", "Member", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "viewer@example.com", "viewer", "Viewer", "CorrectHorse123!")

	otherResp := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other", "slug": "other"}, owner.AccessToken, nil, true)
	var otherWS apigen.WorkspaceSummary
	otherResp.decode(t, &otherWS)
	eve, _ := h.inviteAndAcceptNewUser(owner.AccessToken, otherWS.Id, "eve@other.com", "owner", "Eve", "CorrectHorse123!")

	createBot := func(token string, extra map[string]any) apiResponse {
		body := map[string]any{
			"name":        "Atlas",
			"avatarSeed":  "atlas-seed",
			"llmMode":     "cli",
			"llmProvider": "openrouter",
			"llmModel":    "openrouter/fixture",
		}
		for k, v := range extra {
			body[k] = v
		}
		return h.request(http.MethodPost, base+"/bots", body, token, nil, true)
	}

	r := createBot(owner.AccessToken, nil)
	if r.status != http.StatusCreated {
		t.Fatalf("default create=%d %s", r.status, r.body)
	}
	var defaultBot apigen.Bot
	r.decode(t, &defaultBot)
	if defaultBot.AskBefore == nil || len(*defaultBot.AskBefore) != 0 {
		t.Fatalf("default askBefore=%v", defaultBot.AskBefore)
	}

	validRules := []map[string]any{
		{"kind": "new_host"},
		{"kind": "push_protected", "label": "release"},
		{"kind": "custom", "label": "delete the database"},
	}
	r = createBot(owner.AccessToken, map[string]any{"askBefore": validRules})
	if r.status != http.StatusCreated {
		t.Fatalf("create with rules=%d %s", r.status, r.body)
	}
	var bot apigen.Bot
	r.decode(t, &bot)
	if bot.AskBefore == nil || len(*bot.AskBefore) != 3 {
		t.Fatalf("created askBefore=%v", bot.AskBefore)
	}
	if (*bot.AskBefore)[0].Kind != apigen.AskBeforeRuleKindNewHost ||
		(*bot.AskBefore)[1].Kind != apigen.AskBeforeRuleKindPushProtected || *(*bot.AskBefore)[1].Label != "release" ||
		(*bot.AskBefore)[2].Kind != apigen.AskBeforeRuleKindCustom || *(*bot.AskBefore)[2].Label != "delete the database" {
		t.Fatalf("unexpected askBefore=%+v", *bot.AskBefore)
	}

	botPath := base + "/bots/" + bot.Id.String()

	// Partial PATCH without askBefore preserves existing rules.
	r = h.request(http.MethodPatch, botPath, map[string]any{"name": "Atlas Two"}, owner.AccessToken, nil, true)
	if r.status != http.StatusOK {
		t.Fatalf("rename patch=%d %s", r.status, r.body)
	}
	r.decode(t, &bot)
	if bot.AskBefore == nil || len(*bot.AskBefore) != 3 {
		t.Fatalf("rename dropped askBefore=%v", bot.AskBefore)
	}

	// Updating askBefore replaces the list.
	r = h.request(http.MethodPatch, botPath, map[string]any{"askBefore": []map[string]any{{"kind": "new_host"}}}, owner.AccessToken, nil, true)
	if r.status != http.StatusOK {
		t.Fatalf("patch askBefore=%d %s", r.status, r.body)
	}
	r.decode(t, &bot)
	if bot.AskBefore == nil || len(*bot.AskBefore) != 1 || (*bot.AskBefore)[0].Kind != apigen.AskBeforeRuleKindNewHost {
		t.Fatalf("patched askBefore=%+v", bot.AskBefore)
	}

	// Negative validation cases.
	tooMany := make([]map[string]any, 21)
	for i := range tooMany {
		tooMany[i] = map[string]any{"kind": "new_host"}
	}
	for _, tc := range []struct {
		name  string
		rules any
	}{
		{"raise_budget disallowed on bot", []map[string]any{{"kind": "raise_budget"}}},
		{"custom empty label", []map[string]any{{"kind": "custom", "label": "   "}}},
		{"unknown kind", []map[string]any{{"kind": "unknown_kind"}}},
		{"mcp_tool disallowed", []map[string]any{{"kind": "mcp_tool"}}},
		{"too many rules", tooMany},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if res := createBot(owner.AccessToken, map[string]any{"askBefore": tc.rules}); res.status != http.StatusBadRequest {
				t.Fatalf("invalid create=%d %s", res.status, res.body)
			}
			if res := h.request(http.MethodPatch, botPath, map[string]any{"askBefore": tc.rules}, owner.AccessToken, nil, true); res.status != http.StatusBadRequest {
				t.Fatalf("invalid patch=%d %s", res.status, res.body)
			}
		})
	}

	// Auth and RBAC checks.
	if res := h.request(http.MethodPatch, botPath, map[string]any{"askBefore": []any{}}, "", nil, true); res.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated=%d %s", res.status, res.body)
	}
	if res := h.request(http.MethodPatch, botPath, map[string]any{"askBefore": []any{}}, member.AccessToken, nil, true); res.status != http.StatusForbidden {
		t.Fatalf("non-owner member=%d %s", res.status, res.body)
	}
	if res := h.request(http.MethodPatch, botPath, map[string]any{"askBefore": []any{}}, viewer.AccessToken, nil, true); res.status != http.StatusForbidden {
		t.Fatalf("viewer=%d %s", res.status, res.body)
	}
	if res := h.request(http.MethodPatch, botPath, map[string]any{"askBefore": []any{}}, eve.AccessToken, nil, true); res.status != http.StatusNotFound {
		t.Fatalf("cross-workspace=%d %s", res.status, res.body)
	}
}

func TestDirectMessageAskBeforeApprovals(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue, chatLimit: 100})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member@example.com", "member", "Member", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "viewer@example.com", "viewer", "Viewer", "CorrectHorse123!")

	otherResp := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other", "slug": "other"}, owner.AccessToken, nil, true)
	var otherWS apigen.WorkspaceSummary
	otherResp.decode(t, &otherWS)
	eve, _ := h.inviteAndAcceptNewUser(owner.AccessToken, otherWS.Id, "eve@other.com", "owner", "Eve", "CorrectHorse123!")

	atlas := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas-seed")
	if r := h.request(http.MethodPatch, base+"/bots/"+atlas.Id.String()+"/egress", map[string]any{
		"policy":           "allowlist",
		"allowedHosts":     []string{"docs.example.com"},
		"expectedRevision": 0,
	}, owner.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("egress=%d %s", r.status, r.body)
	}

	// Open a 1:1 DM as member.
	dmResp := h.request(http.MethodPost, base+"/bots/"+atlas.Id.String()+"/dm", nil, member.AccessToken, nil, true)
	if dmResp.status != http.StatusOK {
		t.Fatalf("open dm=%d %s", dmResp.status, dmResp.body)
	}
	var dm apigen.ChatConversation
	dmResp.decode(t, &dm)
	postDM := func(text string) {
		t.Helper()
		r := h.request(http.MethodPost, base+"/conversations/"+dm.Id.String()+"/messages", map[string]any{
			"id":      uuid.New(),
			"content": text,
		}, member.AccessToken, nil, true)
		if r.status != http.StatusAccepted {
			t.Fatalf("post dm=%d %s", r.status, r.body)
		}
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var replyText string
	var toolInput string
	worker := &engine.Worker{
		Pool:    h.pool,
		Now:     h.clock.Now,
		Enqueue: queue.Enqueue,
		Execute: func(_ context.Context, s engine.Session, emit func(engine.Event) error) error {
			if toolInput != "" {
				if err := emit(engine.Event{Kind: engine.EventKindToolCall, ToolName: "git", ToolInput: []byte(toolInput)}); err != nil {
					return err
				}
			}
			if started != nil {
				close(started)
				<-release
			}
			if replyText == "" {
				return nil
			}
			if !strings.Contains(s.AskBeforePrompt, "new_host") && len(s.AskBeforePrompt) > 0 {
				return nil
			}
			return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: replyText})
		},
	}

	// 1) Opt-in check: with default empty askBefore, blocked host on 443 does not open a card.
	replyText = "done"
	postDM("check blocked without ask_before")
	errCh := make(chan error, 1)
	job := queue.jobs[0]
	queue.jobs = queue.jobs[1:]
	go func() { errCh <- worker.Process(context.Background(), job) }()
	<-started
	client, upstream := botProxyClient(t, h, ws, atlas.Id)
	defer upstream.Close()
	mustFailGet(t, client, "https://optin.example.com/test")
	close(release)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if list := listApprovals(t, h, base, member.AccessToken, "pending"); len(list.Approvals) != 0 {
		t.Fatalf("expected opt-in empty approvals, got %+v", list.Approvals)
	}

	// 2) Enable askBefore on the bot for new_host, push_protected, custom.
	if r := h.request(http.MethodPatch, base+"/bots/"+atlas.Id.String(), map[string]any{
		"askBefore": []map[string]any{
			{"kind": "new_host"},
			{"kind": "push_protected", "label": "main"},
			{"kind": "custom", "label": "delete the database"},
		},
	}, owner.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("patch bot askBefore=%d %s", r.status, r.body)
	}

	// Verify new_host card in DM, port != 443 ignored, deduplicated, and permissions enforced.
	started = make(chan struct{})
	release = make(chan struct{})
	postDM("fetch new host")
	job = queue.jobs[0]
	queue.jobs = queue.jobs[1:]
	go func() { errCh <- worker.Process(context.Background(), job) }()
	<-started
	if resp, err := client.Get("https://docs.example.com/ok"); err != nil {
		t.Fatalf("allowed host failed: %v", err)
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	mustFailGet(t, client, "https://api.example.net/one")
	mustFailGet(t, client, "https://api.example.net/two") // duplicate should not stack
	mustFailGet(t, client, "https://port.example.net:8443/no")
	close(release)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}

	pending := listApprovals(t, h, base, member.AccessToken, "pending")
	if len(pending.Approvals) != 1 {
		t.Fatalf("expected 1 pending DM new_host approval, got %+v", pending.Approvals)
	}
	card := findApproval(t, pending, apigen.ApprovalKindNewHost, "api.example.net")
	if card.ConversationId == nil || *card.ConversationId != dm.Id || card.MessageId == nil {
		t.Fatalf("unexpected DM card fields: %+v", card)
	}

	// Another member in the workspace who is not the DM human cannot see or decide it.
	if ownerList := listApprovals(t, h, base, owner.AccessToken, "pending"); approvalIDs(ownerList)[card.Id] {
		t.Fatal("other user saw private DM approval")
	}
	if res := h.request(http.MethodPost, base+"/approvals/"+card.Id.String()+"/decision", map[string]any{"decision": "approved"}, owner.AccessToken, nil, true); res.status != http.StatusNotFound {
		t.Fatalf("other user decide=%d %s", res.status, res.body)
	}
	if res := h.request(http.MethodPost, base+"/approvals/"+card.Id.String()+"/decision", map[string]any{"decision": "approved"}, viewer.AccessToken, nil, true); res.status != http.StatusNotFound {
		t.Fatalf("viewer outside DM decide=%d %s", res.status, res.body)
	}
	if res := h.request(http.MethodPost, base+"/approvals/"+card.Id.String()+"/decision", map[string]any{"decision": "approved"}, eve.AccessToken, nil, true); res.status != http.StatusNotFound {
		t.Fatalf("cross-workspace decide=%d %s", res.status, res.body)
	}

	// Member approves the DM new_host card -> host is added and DM turn resumes.
	beforeJobs := len(queue.jobs)
	if res := h.request(http.MethodPost, base+"/approvals/"+card.Id.String()+"/decision", map[string]any{"decision": "approved"}, member.AccessToken, nil, true); res.status != http.StatusNoContent {
		t.Fatalf("approve DM new_host=%d %s", res.status, res.body)
	}
	if len(queue.jobs) != beforeJobs+1 {
		t.Fatalf("expected resumed DM turn to be queued, jobs=%d", len(queue.jobs))
	}
	if hosts := egressHosts(t, h, atlas.Id); !containsAll(hosts, "docs.example.com", "api.example.net") {
		t.Fatalf("hosts=%v", hosts)
	}
	// Drain resumed turn.
	started = nil
	if err := worker.Process(context.Background(), queue.jobs[0]); err != nil {
		t.Fatal(err)
	}
	queue.jobs = queue.jobs[1:]

	// 3) Verify push_protected and custom rules in DM + deny notice.
	toolInput = `{"command":"git push origin main"}`
	replyText = "I am ready to delete the database now."
	postDM("push and delete")
	job = queue.jobs[0]
	queue.jobs = queue.jobs[1:]
	if err := worker.Process(context.Background(), job); err != nil {
		t.Fatal(err)
	}

	pending = listApprovals(t, h, base, member.AccessToken, "pending")
	pushCard := findApproval(t, pending, apigen.ApprovalKindPushProtected, "")
	customCard := findApproval(t, pending, apigen.ApprovalKindCustom, "")
	if pushCard.Branch == nil || *pushCard.Branch != "main" {
		t.Fatalf("pushCard=%+v", pushCard)
	}
	if customCard.Label == nil || *customCard.Label != "delete the database" {
		t.Fatalf("customCard=%+v", customCard)
	}

	// Deny customCard -> posts system notice and does not queue a turn.
	beforeJobs = len(queue.jobs)
	if res := h.request(http.MethodPost, base+"/approvals/"+customCard.Id.String()+"/decision", map[string]any{"decision": "denied"}, member.AccessToken, nil, true); res.status != http.StatusNoContent {
		t.Fatalf("deny custom=%d %s", res.status, res.body)
	}
	if len(queue.jobs) != beforeJobs {
		t.Fatalf("denied DM approval queued a turn: %d", len(queue.jobs))
	}
	histResp := h.request(http.MethodGet, base+"/conversations/"+dm.Id.String()+"/messages", nil, member.AccessToken, nil, false)
	if histResp.status != http.StatusOK {
		t.Fatalf("history=%d %s", histResp.status, histResp.body)
	}
	var hist apigen.ChatHistory
	histResp.decode(t, &hist)
	var sawApproval, sawDeniedNotice bool
	for _, m := range hist.Messages {
		if m.Kind == apigen.ChatMessageKindApproval && m.Id == *card.MessageId {
			sawApproval = true
		}
		if m.Kind == apigen.ChatMessageKindSystem && strings.Contains(m.Content, "Denied") {
			sawDeniedNotice = true
		}
	}
	if !sawApproval || !sawDeniedNotice {
		t.Fatalf("expected approval message and denied notice in DM history: sawApproval=%v sawDeniedNotice=%v", sawApproval, sawDeniedNotice)
	}
}
