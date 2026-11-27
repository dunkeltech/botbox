// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestPhase6GroupConversationAPI(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	atlas := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas-seed")
	nova := createPiBot(t, h, base, owner.AccessToken, "Nova", "nova-seed")
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member@example.com", "member", "Member", "CorrectHorse123!")
	admin, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "admin@example.com", "admin", "Admin", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "viewer@example.com", "viewer", "Viewer", "CorrectHorse123!")
	other := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other", "slug": "other"}, owner.AccessToken, nil, true)
	var otherWS apigen.WorkspaceSummary
	other.decode(t, &otherWS)
	otherBase := "/api/workspaces/" + otherWS.Id.String()
	outsider := createPiBot(t, h, otherBase, owner.AccessToken, "Outsider", "outsider-seed")
	eve, _ := h.inviteAndAcceptNewUser(owner.AccessToken, otherWS.Id, "eve@other.com", "owner", "Eve", "CorrectHorse123!")

	if r := h.request(http.MethodGet, base+"/groups", nil, "", nil, false); r.status != http.StatusUnauthorized {
		t.Fatalf("unauth list=%d", r.status)
	}
	groupID := uuid.New()
	body := newGroupBody(groupID, "Launch", atlas.Id, []any{
		groupUser(member.User.Id, "writer"),
		groupUser(viewer.User.Id, "reader"),
		groupBot(atlas.Id, "lead"),
		groupBot(nova.Id, "reviewer"),
	})
	if r := h.request(http.MethodPost, base+"/groups", body, viewer.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("viewer create=%d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, base+"/groups", body, owner.AccessToken, nil, false); r.status != http.StatusForbidden || r.errorCode(t) != "auth.csrf_required" {
		t.Fatalf("csrf=%d %s", r.status, r.body)
	}
	created := h.request(http.MethodPost, base+"/groups", body, owner.AccessToken, nil, true)
	if created.status != http.StatusCreated {
		t.Fatalf("create=%d %s", created.status, created.body)
	}
	var group apigen.Group
	created.decode(t, &group)
	if group.Id != groupID || group.Name != "Launch" || group.LeadBotId == nil || *group.LeadBotId != atlas.Id || group.Unread != 0 || len(group.Members) != 5 {
		t.Fatalf("created=%s", created.body)
	}
	if findGroupMember(t, group.Members, owner.User.Id).RoleText != "" {
		t.Fatal("caller role was not kept empty when omitted")
	}
	replay := h.request(http.MethodPost, base+"/groups", body, owner.AccessToken, nil, true)
	if replay.status != http.StatusOK {
		t.Fatalf("replay=%d %s", replay.status, replay.body)
	}
	changed := newGroupBody(groupID, "Renamed", atlas.Id, body["members"].([]any))
	if r := h.request(http.MethodPost, base+"/groups", changed, owner.AccessToken, nil, true); r.status != http.StatusConflict {
		t.Fatalf("conflict=%d %s", r.status, r.body)
	}
	// Same client id in another workspace collides on the primary key. The body
	// has to be valid there, or membership validation answers before the insert.
	otherBody := newGroupBody(groupID, "Launch", outsider.Id, []any{groupBot(outsider.Id, "lead")})
	if r := h.request(http.MethodPost, otherBase+"/groups", otherBody, owner.AccessToken, nil, true); r.status != http.StatusConflict {
		t.Fatalf("cross workspace id=%d %s", r.status, r.body)
	}

	path := base + "/groups/" + groupID.String()
	for _, tc := range []struct {
		token  string
		status int
	}{
		{"", http.StatusUnauthorized},
		{admin.AccessToken, http.StatusNotFound},
		{eve.AccessToken, http.StatusNotFound},
		{viewer.AccessToken, http.StatusOK},
		{member.AccessToken, http.StatusOK},
		{owner.AccessToken, http.StatusOK},
	} {
		r := h.request(http.MethodGet, path, nil, tc.token, nil, false)
		if r.status != tc.status {
			t.Fatalf("get token status=%d want=%d body=%s", r.status, tc.status, r.body)
		}
	}
	if r := h.request(http.MethodGet, base+"/groups/not-a-uuid", nil, owner.AccessToken, nil, false); r.status != http.StatusBadRequest {
		t.Fatalf("bad group id=%d", r.status)
	}
	if r := h.request(http.MethodGet, otherBase+"/groups/"+groupID.String(), nil, owner.AccessToken, nil, false); r.status != http.StatusNotFound {
		t.Fatalf("cross workspace get=%d", r.status)
	}
	listed := h.request(http.MethodGet, base+"/groups", nil, member.AccessToken, nil, false)
	var groups []apigen.Group
	listed.decode(t, &groups)
	if len(groups) != 1 || groups[0].Id != groupID {
		t.Fatalf("member list=%s", listed.body)
	}
	if r := h.request(http.MethodGet, base+"/groups", nil, admin.AccessToken, nil, false); r.status != http.StatusOK {
		t.Fatalf("admin list=%d", r.status)
	} else {
		var none []apigen.Group
		r.decode(t, &none)
		if len(none) != 0 {
			t.Fatalf("admin sees groups=%s", r.body)
		}
	}

	for _, bad := range []map[string]any{
		newGroupBody(uuid.Nil, "Ok", atlas.Id, body["members"].([]any)),
		newGroupBody(uuid.New(), " ", atlas.Id, body["members"].([]any)),
		newGroupBody(uuid.New(), "Ok", atlas.Id, []any{groupUser(member.User.Id, "writer")}),
		newGroupBody(uuid.New(), "Ok", nova.Id, []any{groupBot(atlas.Id, "lead")}),
		newGroupBody(uuid.New(), "Ok", atlas.Id, []any{groupBot(outsider.Id, "lead")}),
		newGroupBody(uuid.New(), "Ok", atlas.Id, []any{groupBot(atlas.Id, "lead"), groupUser(uuid.New(), "ghost")}),
		newGroupBody(uuid.New(), "Ok", atlas.Id, []any{groupBot(atlas.Id, strings.Repeat("r", 101))}),
		newGroupBody(uuid.New(), "Ok", atlas.Id, []any{groupBot(atlas.Id, "lead"), groupBot(atlas.Id, "again")}),
	} {
		if bad["id"] == uuid.Nil {
			delete(bad, "id")
		}
		r := h.request(http.MethodPost, base+"/groups", bad, owner.AccessToken, nil, true)
		if r.status != http.StatusBadRequest {
			t.Fatalf("invalid create=%d %s", r.status, r.body)
		}
	}
	limit := newGroupBody(uuid.New(), "Ok", atlas.Id, []any{groupBot(atlas.Id, "lead")})
	limit["turnLimit"] = 0
	policy := newGroupBody(uuid.New(), "Ok", atlas.Id, []any{groupBot(atlas.Id, "lead")})
	policy["answerPolicy"] = "everyone"
	goal := newGroupBody(uuid.New(), "Ok", atlas.Id, []any{groupBot(atlas.Id, "lead")})
	goal["goal"] = strings.Repeat("a", 8193)
	custom := newGroupBody(uuid.New(), "Ok", atlas.Id, []any{groupBot(atlas.Id, "lead")})
	custom["askBefore"] = []any{map[string]any{"kind": "custom"}}
	budget := newGroupBody(uuid.New(), "Ok", atlas.Id, []any{groupBot(atlas.Id, "lead")})
	budget["budgetLimitMicros"] = 0
	shape := newGroupBody(uuid.New(), "Ok", atlas.Id, []any{groupBot(atlas.Id, "lead")})
	shape["askBefore"] = map[string]any{"kind": "new_host"}
	for _, bad := range []map[string]any{limit, policy, goal, custom, budget, shape} {
		if r := h.request(http.MethodPost, base+"/groups", bad, owner.AccessToken, nil, true); r.status != http.StatusBadRequest {
			t.Fatalf("invalid settings=%d %s", r.status, r.body)
		}
	}

	messages := path + "/messages"
	for _, tc := range []struct {
		token  string
		status int
	}{
		{"", http.StatusUnauthorized},
		{admin.AccessToken, http.StatusNotFound},
		{viewer.AccessToken, http.StatusForbidden},
		{member.AccessToken, http.StatusAccepted},
	} {
		r := h.request(http.MethodPost, messages, map[string]any{"id": uuid.New(), "content": "hello"}, tc.token, nil, true)
		if r.status != tc.status {
			t.Fatalf("post=%d want=%d body=%s", r.status, tc.status, r.body)
		}
	}
	if len(queue.jobs) != 1 || queue.jobs[0].BotID != atlas.Id {
		t.Fatalf("group post scheduled %#v", queue.jobs)
	}
	input := map[string]any{"id": uuid.New(), "content": "Ship the notes"}
	for range 2 {
		r := h.request(http.MethodPost, messages, input, member.AccessToken, nil, true)
		if r.status != http.StatusAccepted {
			t.Fatalf("replay post=%d %s", r.status, r.body)
		}
	}
	if len(queue.jobs) != 2 || queue.jobs[1].BotID != atlas.Id {
		t.Fatalf("replay scheduled %#v", queue.jobs)
	}
	input["content"] = "Changed"
	if r := h.request(http.MethodPost, messages, input, member.AccessToken, nil, true); r.status != http.StatusConflict {
		t.Fatalf("message conflict=%d", r.status)
	}
	for _, bad := range []any{map[string]any{}, map[string]any{"id": uuid.New(), "content": " "}, map[string]any{"id": uuid.New(), "content": strings.Repeat("a", 8193)}} {
		if r := h.request(http.MethodPost, messages, bad, member.AccessToken, nil, true); r.status != http.StatusBadRequest {
			t.Fatalf("bad message=%d %s", r.status, r.body)
		}
	}
	history := h.request(http.MethodGet, messages, nil, viewer.AccessToken, nil, false)
	var chat apigen.ChatHistory
	history.decode(t, &chat)
	if len(chat.Messages) != 2 || len(chat.Turns) != 2 || chat.Messages[1].Content != "Ship the notes" || chat.Turns[0].Status != "queued" || chat.Turns[1].Status != "queued" {
		t.Fatalf("history=%s", history.body)
	}
	if r := h.request(http.MethodGet, messages+"?before=0", nil, viewer.AccessToken, nil, false); r.status != http.StatusBadRequest {
		t.Fatalf("cursor=%d", r.status)
	}
	if r := h.request(http.MethodGet, messages, nil, admin.AccessToken, nil, false); r.status != http.StatusNotFound {
		t.Fatalf("admin history=%d", r.status)
	}
	got := h.request(http.MethodGet, path, nil, member.AccessToken, nil, false)
	got.decode(t, &group)
	if group.Unread != 2 {
		t.Fatalf("unread=%d body=%s", group.Unread, got.body)
	}
	if r := h.request(http.MethodPost, path+"/read", nil, "", nil, true); r.status != http.StatusUnauthorized {
		t.Fatalf("unauth read=%d", r.status)
	}
	if r := h.request(http.MethodPost, path+"/read", nil, admin.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("admin read=%d", r.status)
	}
	if r := h.request(http.MethodPost, path+"/read", nil, member.AccessToken, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("mark read=%d %s", r.status, r.body)
	}
	got = h.request(http.MethodGet, path, nil, member.AccessToken, nil, false)
	got.decode(t, &group)
	if group.Unread != 0 {
		t.Fatalf("unread after read=%d", group.Unread)
	}
	ownerView := h.request(http.MethodGet, path, nil, owner.AccessToken, nil, false)
	ownerView.decode(t, &group)
	if group.Unread != 2 {
		t.Fatalf("owner unread=%d", group.Unread)
	}

	if r := h.request(http.MethodPatch, path, changed, viewer.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("viewer patch=%d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPatch, path, changed, admin.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("admin patch=%d %s", r.status, r.body)
	}
	changed["id"] = groupID
	delete(changed, "id")
	changed["botTalk"] = true
	changed["turnLimit"] = 4
	changed["answerPolicy"] = "round_robin"
	changed["goal"] = "Ship it"
	changed["askBefore"] = []any{map[string]any{"kind": "custom", "label": "Push main"}}
	changed["budgetLimitMicros"] = 5000
	changed["members"] = []any{groupUser(owner.User.Id, "owner"), groupUser(member.User.Id, "writer"), groupBot(nova.Id, "lead")}
	changed["leadBotId"] = nil
	if r := h.request(http.MethodPatch, path, changed, owner.AccessToken, nil, true); r.status != http.StatusBadRequest {
		t.Fatalf("clear an existing lead: %d %s", r.status, r.body)
	}
	changed["leadBotId"] = nova.Id
	patched := h.request(http.MethodPatch, path, changed, owner.AccessToken, nil, true)
	if patched.status != http.StatusOK {
		t.Fatalf("patch=%d %s", patched.status, patched.body)
	}
	patched.decode(t, &group)
	if !group.BotTalk || group.TurnLimit != 4 || group.AnswerPolicy != "round_robin" || group.Goal != "Ship it" || group.LeadBotId == nil || *group.LeadBotId != nova.Id || group.BudgetLimitMicros == nil || *group.BudgetLimitMicros != 5000 {
		t.Fatalf("patched=%s", patched.body)
	}
	if len(group.AskBefore) != 1 || group.AskBefore[0].Kind != "custom" || group.AskBefore[0].Label == nil || *group.AskBefore[0].Label != "Push main" {
		t.Fatalf("rules=%s", patched.body)
	}
	if len(group.Members) != 3 || findGroupMember(t, group.Members, member.User.Id).RoleText != "writer" {
		t.Fatalf("members=%s", patched.body)
	}
	if r := h.request(http.MethodGet, path, nil, viewer.AccessToken, nil, false); r.status != http.StatusNotFound {
		t.Fatalf("removed viewer=%d", r.status)
	}
	still := h.request(http.MethodGet, path, nil, member.AccessToken, nil, false)
	still.decode(t, &group)
	if group.Unread != 0 {
		t.Fatalf("settings save cleared the read cursor, unread=%d", group.Unread)
	}

	removed := h.request(http.MethodDelete, base+"/members/"+member.User.Id.String(), nil, owner.AccessToken, nil, true)
	if removed.status != http.StatusNoContent {
		t.Fatalf("remove group author=%d %s", removed.status, removed.body)
	}
	if r := h.request(http.MethodGet, messages, nil, member.AccessToken, nil, false); r.status != http.StatusNotFound {
		t.Fatalf("removed group author can read history: %d", r.status)
	}
	history = h.request(http.MethodGet, messages, nil, owner.AccessToken, nil, false)
	history.decode(t, &chat)
	if len(chat.Messages) != 2 || chat.Messages[1].SenderUserId == nil || *chat.Messages[1].SenderUserId != member.User.Id {
		t.Fatalf("historical authorship lost: %s", history.body)
	}
	ownerView = h.request(http.MethodGet, path, nil, owner.AccessToken, nil, false)
	ownerView.decode(t, &group)
	if len(group.Members) != 2 {
		t.Fatalf("membership removal left a group grant: %s", ownerView.body)
	}
}

func TestPhase6GroupMessageRateLimit(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue, chatLimit: 1})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	base := "/api/workspaces/" + owner.ActiveWorkspace.Id.String()
	atlas := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas-seed")
	body := newGroupBody(uuid.New(), "Launch", atlas.Id, []any{groupBot(atlas.Id, "lead")})
	created := h.request(http.MethodPost, base+"/groups", body, owner.AccessToken, nil, true)
	if created.status != http.StatusCreated {
		t.Fatalf("create=%d %s", created.status, created.body)
	}
	var group apigen.Group
	created.decode(t, &group)
	messages := base + "/groups/" + group.Id.String() + "/messages"
	first := map[string]any{"id": uuid.New(), "content": "once"}
	if r := h.request(http.MethodPost, messages, first, owner.AccessToken, nil, true); r.status != http.StatusAccepted {
		t.Fatalf("first=%d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, messages, map[string]any{"id": uuid.New(), "content": "twice"}, owner.AccessToken, nil, true); r.status != http.StatusTooManyRequests {
		t.Fatalf("limit=%d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, messages, first, owner.AccessToken, nil, true); r.status != http.StatusAccepted {
		t.Fatalf("replay under limit=%d %s", r.status, r.body)
	}
	if len(queue.jobs) != 1 {
		t.Fatalf("rate limit scheduled %#v", queue.jobs)
	}
}

func TestPhase6GroupHistoryPaginationToBeginning(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	atlas := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas-seed")
	outsider, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "outsider@example.com", "member", "Outsider", "CorrectHorse123!")

	other := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other", "slug": "other"}, owner.AccessToken, nil, true)
	var otherWS apigen.WorkspaceSummary
	other.decode(t, &otherWS)

	groupID := uuid.New()
	body := newGroupBody(groupID, "Launch", atlas.Id, []any{groupBot(atlas.Id, "lead")})
	created := h.request(http.MethodPost, base+"/groups", body, owner.AccessToken, nil, true)
	if created.status != http.StatusCreated {
		t.Fatalf("create=%d %s", created.status, created.body)
	}

	wsPG := pgtype.UUID{Bytes: ws, Valid: true}
	convPG := pgtype.UUID{Bytes: groupID, Valid: true}
	userPG := pgtype.UUID{Bytes: owner.User.Id, Valid: true}
	botPG := pgtype.UUID{Bytes: atlas.Id, Valid: true}
	for i := 1; i <= 105; i++ {
		params := db.CreateChatMessageParams{
			ID:             pgtype.UUID{Bytes: uuid.New(), Valid: true},
			WorkspaceID:    wsPG,
			ConversationID: convPG,
			Kind:           "text",
			Content:        fmt.Sprintf("group-msg-%03d", i),
			Metadata:       []byte("{}"),
		}
		if i%2 == 1 {
			params.SenderType = "user"
			params.SenderUserID = userPG
		} else {
			params.SenderType = "bot"
			params.SenderBotID = botPG
			if i%4 == 0 {
				params.Kind = "tool"
				params.Content = "tool output"
				params.Metadata = []byte(`{"Kind":"tool_result"}`)
			}
		}
		if _, err := h.queries.CreateChatMessage(context.Background(), params); err != nil {
			t.Fatal(err)
		}
	}

	messagesPath := base + "/groups/" + groupID.String() + "/messages"
	if r := h.request(http.MethodGet, messagesPath+"?before=0", nil, owner.AccessToken, nil, false); r.status != http.StatusBadRequest {
		t.Fatalf("invalid before=%d", r.status)
	}
	if r := h.request(http.MethodGet, messagesPath, nil, outsider.AccessToken, nil, false); r.status != http.StatusNotFound {
		t.Fatalf("non-member=%d", r.status)
	}
	if r := h.request(http.MethodGet, "/api/workspaces/"+otherWS.Id.String()+"/groups/"+groupID.String()+"/messages", nil, owner.AccessToken, nil, false); r.status != http.StatusNotFound {
		t.Fatalf("cross-workspace=%d", r.status)
	}

	var collected []apigen.ChatMessage
	cursor := ""
	for pageIdx := range 3 {
		url := messagesPath
		if cursor != "" {
			url += "?before=" + cursor
		}
		res := h.request(http.MethodGet, url, nil, owner.AccessToken, nil, false)
		if res.status != http.StatusOK {
			t.Fatalf("page %d status=%d %s", pageIdx, res.status, res.body)
		}
		var page apigen.ChatHistory
		res.decode(t, &page)
		wantMore := pageIdx < 2
		if page.HasMore == nil || *page.HasMore != wantMore {
			t.Fatalf("page %d hasMore=%v want=%v", pageIdx, page.HasMore, wantMore)
		}
		collected = append(page.Messages, collected...)
		cursor = strconv.FormatInt(page.Messages[0].Position, 10)
	}
	if len(collected) != 105 {
		t.Fatalf("collected=%d want 105", len(collected))
	}
	if collected[0].Content != "group-msg-001" || collected[104].Content != "group-msg-105" {
		t.Fatalf("bounds first=%q last=%q", collected[0].Content, collected[104].Content)
	}
	for i := 1; i < len(collected); i++ {
		if collected[i].Position <= collected[i-1].Position {
			t.Fatalf("out-of-order positions at %d: %d <= %d", i, collected[i].Position, collected[i-1].Position)
		}
	}
}

func createPiBot(t *testing.T, h *testHarness, base, token, name, seed string) apigen.Bot {
	t.Helper()
	r := h.request(http.MethodPost, base+"/bots", map[string]any{"name": name, "avatarSeed": seed, "llmMode": "cli", "llmProvider": "openrouter", "llmModel": "openrouter/fixture"}, token, nil, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create bot=%d %s", r.status, r.body)
	}
	var bot apigen.Bot
	r.decode(t, &bot)
	return bot
}

func newGroupBody(id uuid.UUID, name string, lead uuid.UUID, members []any) map[string]any {
	return map[string]any{
		"id": id, "name": name, "leadBotId": lead, "botTalk": false, "turnLimit": 20,
		"answerPolicy": "mentioned_else_lead", "askBefore": []any{}, "members": members,
	}
}

func groupUser(id uuid.UUID, role string) map[string]any {
	return map[string]any{"type": "user", "id": id, "roleText": role}
}

func groupBot(id uuid.UUID, role string) map[string]any {
	return map[string]any{"type": "bot", "id": id, "roleText": role}
}

func findGroupMember(t *testing.T, members []apigen.GroupMember, id uuid.UUID) apigen.GroupMember {
	t.Helper()
	for _, member := range members {
		if member.Id == id {
			return member
		}
	}
	t.Fatalf("member %s not in group", id)
	return apigen.GroupMember{}
}
