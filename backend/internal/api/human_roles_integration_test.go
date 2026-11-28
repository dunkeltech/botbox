// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/notify"
	"github.com/google/uuid"
)

func TestHumanMemberRoleDescriptionsRBACAndValidation(t *testing.T) {
	h := newTestHarness(t, nil, harnessOpts{turnQueue: &fakeTurnQueue{}})
	owner, _ := h.completeSetup("owner@dunkel.tech", "CorrectHorse123!", "Owner", "Alpha", "alpha")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()

	admin1, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "admin1@dunkel.tech", "admin", "Admin One", "CorrectHorse123!")
	admin2, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "admin2@dunkel.tech", "admin", "Admin Two", "CorrectHorse123!")
	member1, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member1@dunkel.tech", "member", "Maya", "CorrectHorse123!")
	member2, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member2@dunkel.tech", "member", "Liam", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "viewer@dunkel.tech", "viewer", "Sara", "CorrectHorse123!")

	otherWSResp := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Beta", "slug": "beta"}, owner.AccessToken, nil, true)
	if otherWSResp.status != http.StatusCreated {
		t.Fatalf("create beta=%d %s", otherWSResp.status, otherWSResp.body)
	}
	var otherWS apigen.WorkspaceSummary
	otherWSResp.decode(t, &otherWS)

	outsider, _ := h.inviteAndAcceptNewUser(owner.AccessToken, otherWS.Id, "outsider@dunkel.tech", "member", "Outsider", "CorrectHorse123!")

	patchMember := func(token string, wsID, userID uuid.UUID, body map[string]any) apiResponse {
		return h.request(http.MethodPatch, "/api/workspaces/"+wsID.String()+"/members/"+userID.String(), body, token, nil, true)
	}

	// Negative validation, auth, cross-workspace, and RBAC boundary checks.
	for _, tc := range []struct {
		name       string
		token      string
		wsID       uuid.UUID
		targetID   uuid.UUID
		body       map[string]any
		wantStatus int
	}{
		{name: "unauthenticated", token: "", wsID: ws, targetID: member1.User.Id, body: map[string]any{"roleDescription": "Reviewer"}, wantStatus: http.StatusUnauthorized},
		{name: "empty body", token: owner.AccessToken, wsID: ws, targetID: member1.User.Id, body: map[string]any{}, wantStatus: http.StatusBadRequest},
		{name: "roleDescription containing NUL returns 400", token: owner.AccessToken, wsID: ws, targetID: member1.User.Id, body: map[string]any{"roleDescription": "Release\x00Reviewer"}, wantStatus: http.StatusBadRequest},
		{name: "roleDescription exceeds 4000 chars", token: owner.AccessToken, wsID: ws, targetID: member1.User.Id, body: map[string]any{"roleDescription": strings.Repeat("a", 4001)}, wantStatus: http.StatusBadRequest},
		{name: "cross-workspace returns 404", token: owner.AccessToken, wsID: otherWS.Id, targetID: member1.User.Id, body: map[string]any{"roleDescription": "Reviewer"}, wantStatus: http.StatusNotFound},
		{name: "non-member target returns 404", token: owner.AccessToken, wsID: ws, targetID: outsider.User.Id, body: map[string]any{"roleDescription": "Reviewer"}, wantStatus: http.StatusNotFound},
		{name: "viewer cannot edit own role description", token: viewer.AccessToken, wsID: ws, targetID: viewer.User.Id, body: map[string]any{"roleDescription": "Observer"}, wantStatus: http.StatusForbidden},
		{name: "member cannot edit another member role description", token: member1.AccessToken, wsID: ws, targetID: member2.User.Id, body: map[string]any{"roleDescription": "Reviewer"}, wantStatus: http.StatusForbidden},
		{name: "member cannot escalate own access role", token: member1.AccessToken, wsID: ws, targetID: member1.User.Id, body: map[string]any{"role": "admin", "roleDescription": "Reviewer"}, wantStatus: http.StatusForbidden},
		{name: "admin cannot edit owner role description", token: admin1.AccessToken, wsID: ws, targetID: owner.User.Id, body: map[string]any{"roleDescription": "Override"}, wantStatus: http.StatusForbidden},
		{name: "admin cannot edit another admin role description", token: admin1.AccessToken, wsID: ws, targetID: admin2.User.Id, body: map[string]any{"roleDescription": "Override"}, wantStatus: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := patchMember(tc.token, tc.wsID, tc.targetID, tc.body)
			if res.status != tc.wantStatus {
				t.Fatalf("got status=%d (%s), want %d", res.status, res.body, tc.wantStatus)
			}
		})
	}

	// Member edits and removes own role description.
	res := patchMember(member1.AccessToken, ws, member1.User.Id, map[string]any{"roleDescription": "  Release Reviewer  "})
	if res.status != http.StatusOK {
		t.Fatalf("member self-edit=%d %s", res.status, res.body)
	}
	var updated apigen.WorkspaceMember
	res.decode(t, &updated)
	if updated.RoleDescription != "Release Reviewer" || updated.Role != apigen.Member {
		t.Fatalf("unexpected member update: %+v", updated)
	}

	// Admin edits own, member's, and viewer's role description.
	for _, targetID := range []uuid.UUID{admin1.User.Id, member2.User.Id, viewer.User.Id} {
		r := patchMember(admin1.AccessToken, ws, targetID, map[string]any{"roleDescription": "Security Reviewer"})
		if r.status != http.StatusOK {
			t.Fatalf("admin edit %s=%d %s", targetID, r.status, r.body)
		}
	}

	// Owner edits admin2 and clears member1's role description.
	if r := patchMember(owner.AccessToken, ws, admin2.User.Id, map[string]any{"roleDescription": "Ops Lead"}); r.status != http.StatusOK {
		t.Fatalf("owner edit admin2=%d %s", r.status, r.body)
	}
	if r := patchMember(owner.AccessToken, ws, member1.User.Id, map[string]any{"roleDescription": ""}); r.status != http.StatusOK {
		t.Fatalf("owner clear member1=%d %s", r.status, r.body)
	}

	// Verify listMembers reflects final persisted descriptions and audit entry exists.
	listResp := h.request(http.MethodGet, base+"/members", nil, viewer.AccessToken, nil, false)
	if listResp.status != http.StatusOK {
		t.Fatalf("list members=%d %s", listResp.status, listResp.body)
	}
	var list apigen.WorkspaceMemberListResponse
	listResp.decode(t, &list)
	byID := map[uuid.UUID]apigen.WorkspaceMember{}
	for _, m := range list.Members {
		byID[m.UserId] = m
	}
	if byID[member1.User.Id].RoleDescription != "" {
		t.Fatalf("expected cleared roleDescription for member1, got %q", byID[member1.User.Id].RoleDescription)
	}
	if byID[admin2.User.Id].RoleDescription != "Ops Lead" {
		t.Fatalf("expected Ops Lead for admin2, got %q", byID[admin2.User.Id].RoleDescription)
	}

	var auditCount int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE workspace_id = $1 AND action = 'member.role_description_updated'`, ws).Scan(&auditCount); err != nil {
		t.Fatalf("query audit logs: %v", err)
	}
	if auditCount != 6 {
		t.Fatalf("audit count=%d want 6", auditCount)
	}
}

func TestHumanRolesGroupVisibilityHandoffAndNotifications(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue, chatLimit: 50})
	owner, _ := h.completeSetup("owner@dunkel.tech", "CorrectHorse123!", "Owner", "Alpha", "alpha")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()

	maya, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "maya@dunkel.tech", "member", "Maya", "CorrectHorse123!")
	quiet, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "quiet@dunkel.tech", "member", "Quiet", "CorrectHorse123!")
	nonMember, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "other@dunkel.tech", "member", "Outsider", "CorrectHorse123!")

	for _, pair := range []struct {
		token string
		id    uuid.UUID
		desc  string
	}{
		{maya.AccessToken, maya.User.Id, "Release Reviewer — Reviews draft release notes and checks accuracy."},
		{quiet.AccessToken, quiet.User.Id, "Quiet Reviewer — Muted in this group."},
		{nonMember.AccessToken, nonMember.User.Id, "Secret Outsider Role — Must never leak into unrelated groups."},
	} {
		r := h.request(http.MethodPatch, base+"/members/"+pair.id.String(), map[string]any{"roleDescription": pair.desc}, pair.token, nil, true)
		if r.status != http.StatusOK {
			t.Fatalf("set role for %s=%d %s", pair.id, r.status, r.body)
		}
	}

	atlas := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas")
	group := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name:      "Release",
		lead:      atlas.Id,
		botTalk:   true,
		turnLimit: 20,
		members:   []any{groupBot(atlas.Id, ""), groupUser(owner.User.Id, ""), groupUser(maya.User.Id, ""), groupUser(quiet.User.Id, "")},
	})
	setNotify(t, h, ws, group.Id, maya.User.Id, "mentions")
	setNotify(t, h, ws, group.Id, quiet.User.Id, "mute")

	// Group details expose Maya's role but never include nonMember.
	grpResp := h.request(http.MethodGet, base+"/groups/"+group.Id.String(), nil, maya.AccessToken, nil, false)
	if grpResp.status != http.StatusOK {
		t.Fatalf("get group=%d %s", grpResp.status, grpResp.body)
	}
	var gotGroup apigen.Group
	grpResp.decode(t, &gotGroup)
	mayaMember := findGroupMember(t, gotGroup.Members, maya.User.Id)
	if mayaMember.RoleDescription == nil || !strings.Contains(*mayaMember.RoleDescription, "Release Reviewer") {
		t.Fatalf("expected Maya's roleDescription on group member, got %+v", mayaMember)
	}
	for _, m := range gotGroup.Members {
		if m.Id == nonMember.User.Id {
			t.Fatal("non-member appeared in group members")
		}
	}

	// First turn: bot receives Maya's role, does not see nonMember, and @mentions Maya, Quiet, and Outsider.
	postGroup(t, h, base, owner.AccessToken, group.Id, "Draft the v1.4.0 release notes and ask the right reviewer.")
	var seenPrefaces []string
	worker := &engine.Worker{
		Pool: h.pool,
		Now:  h.clock.Now,
		Signal: func(ctx context.Context, wsID, conv uuid.UUID, ids []uuid.UUID) {
			data, err := notify.MarshalSignal(conv, ids)
			if err == nil {
				_ = queue.PublishConversation(ctx, wsID, conv, data)
			}
		},
		Execute: func(_ context.Context, s engine.Session, emit func(engine.Event) error) error {
			seenPrefaces = append(seenPrefaces, s.Preface)
			if len(seenPrefaces) == 1 {
				return emit(engine.Event{
					Kind: engine.EventKindTextDelta,
					Text: "@Maya, please review the draft release notes for accuracy and flag anything that needs changing. @Quiet @Outsider",
				})
			}
			return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "Updated the draft with your feedback, @Maya."})
		},
	}
	drainTurns(t, worker, queue, 2)

	if len(seenPrefaces) != 1 {
		t.Fatalf("expected 1 preface on initial turn, got %d", len(seenPrefaces))
	}
	if !strings.Contains(seenPrefaces[0], "Release Reviewer — Reviews draft release notes") {
		t.Fatalf("initial preface missing Maya's role:\n%s", seenPrefaces[0])
	}
	if strings.Contains(seenPrefaces[0], "Outsider") || strings.Contains(seenPrefaces[0], "Secret Outsider Role") {
		t.Fatalf("non-member role leaked into group preface:\n%s", seenPrefaces[0])
	}

	// Notification preferences: Maya (mentions) gets 1 mention; Quiet (mute) and Outsider get 0.
	notesURL := base + "/notifications"
	if got := len(notesOf(listNotes(t, h, notesURL, maya.AccessToken), apigen.NotificationKindMention, &group.Id)); got != 1 {
		t.Fatalf("maya mentions=%d want 1", got)
	}
	if got := len(notesOf(listNotes(t, h, notesURL, quiet.AccessToken), apigen.NotificationKindMention, &group.Id)); got != 0 {
		t.Fatalf("muted user mentions=%d want 0", got)
	}
	if got := len(notesOf(listNotes(t, h, notesURL, nonMember.AccessToken), apigen.NotificationKindMention, &group.Id)); got != 0 {
		t.Fatalf("non-member mentions=%d want 0", got)
	}

	// Edit Maya's role and have Maya reply in group chat -> next bot turn reflects updated role AND Maya's response.
	if r := h.request(http.MethodPatch, base+"/members/"+maya.User.Id.String(), map[string]any{"roleDescription": "Principal Release Approver"}, maya.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("update maya role=%d %s", r.status, r.body)
	}
	postGroup(t, h, base, maya.AccessToken, group.Id, "@Atlas Please add the zero-downtime migration note.")
	drainTurns(t, worker, queue, 2)

	if len(seenPrefaces) != 2 {
		t.Fatalf("expected 2 prefaces, got %d", len(seenPrefaces))
	}
	if !strings.Contains(seenPrefaces[1], "Principal Release Approver") {
		t.Fatalf("updated role missing on next turn:\n%s", seenPrefaces[1])
	}
	if strings.Contains(seenPrefaces[1], "Release Reviewer — Reviews draft release notes") {
		t.Fatalf("stale role still present on next turn:\n%s", seenPrefaces[1])
	}
	if !strings.Contains(seenPrefaces[1], "Please add the zero-downtime migration note.") {
		t.Fatalf("Maya's reply missing from bot context:\n%s", seenPrefaces[1])
	}

	// Remove Maya's role and verify it disappears on the following turn.
	if r := h.request(http.MethodPatch, base+"/members/"+maya.User.Id.String(), map[string]any{"roleDescription": ""}, owner.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("clear maya role=%d %s", r.status, r.body)
	}
	postGroup(t, h, base, maya.AccessToken, group.Id, "@Atlas Looks good now!")
	drainTurns(t, worker, queue, 2)

	if len(seenPrefaces) != 3 {
		t.Fatalf("expected 3 prefaces, got %d", len(seenPrefaces))
	}
	if strings.Contains(seenPrefaces[2], "Principal Release Approver") {
		t.Fatalf("cleared role still present on next turn:\n%s", seenPrefaces[2])
	}
}

func TestHumanHandoffDisambiguatesSharedNames(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue, chatLimit: 50})
	owner, _ := h.completeSetup("owner@dunkel.tech", "CorrectHorse123!", "Owner", "Alpha", "alpha")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()

	alex1, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "alex1@dunkel.tech", "member", "Alex", "CorrectHorse123!")
	alex2, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "alex2@dunkel.tech", "member", "Alex", "CorrectHorse123!")
	botAlex := createPiBot(t, h, base, owner.AccessToken, "Alex", "alex")

	group := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name:      "Release",
		lead:      botAlex.Id,
		botTalk:   false,
		turnLimit: 20,
		members:   []any{groupBot(botAlex.Id, ""), groupUser(owner.User.Id, ""), groupUser(alex1.User.Id, ""), groupUser(alex2.User.Id, "")},
	})

	turn := 0
	worker := &engine.Worker{
		Pool: h.pool,
		Now:  h.clock.Now,
		Signal: func(ctx context.Context, wsID, conv uuid.UUID, ids []uuid.UUID) {
			data, err := notify.MarshalSignal(conv, ids)
			if err == nil {
				_ = queue.PublishConversation(ctx, wsID, conv, data)
			}
		},
		Execute: func(_ context.Context, s engine.Session, emit func(engine.Event) error) error {
			turn++
			if !strings.Contains(s.Preface, "id="+alex1.User.Id.String()) || !strings.Contains(s.Preface, "id="+alex2.User.Id.String()) {
				t.Errorf("missing stable human ids in preface:\n%s", s.Preface)
			}
			if turn == 1 {
				// Even when a bot shares the name "Alex", @Alex must still reach human Alex members.
				return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "@Alex please review."})
			}
			return emit(engine.Event{
				Kind: engine.EventKindTextDelta,
				Text: "@Alex please review the release notes.\nHandoff to human: [\"" + alex1.User.Id.String() + "\"]",
			})
		},
	}

	postGroup(t, h, base, owner.AccessToken, group.Id, "Who can review?")
	drainTurns(t, worker, queue, 2)

	notesURL := base + "/notifications"
	if got := len(notesOf(listNotes(t, h, notesURL, alex1.AccessToken), apigen.NotificationKindMention, &group.Id)); got != 1 {
		t.Fatalf("alex1 mentions after @Alex=%d want 1", got)
	}
	if got := len(notesOf(listNotes(t, h, notesURL, alex2.AccessToken), apigen.NotificationKindMention, &group.Id)); got != 1 {
		t.Fatalf("alex2 mentions after @Alex=%d want 1", got)
	}

	postGroup(t, h, base, owner.AccessToken, group.Id, "Hand off only to release reviewer Alex.")
	drainTurns(t, worker, queue, 2)

	if got := len(notesOf(listNotes(t, h, notesURL, alex1.AccessToken), apigen.NotificationKindMention, &group.Id)); got != 2 {
		t.Fatalf("alex1 mentions after explicit handoff=%d want 2", got)
	}
	if got := len(notesOf(listNotes(t, h, notesURL, alex2.AccessToken), apigen.NotificationKindMention, &group.Id)); got != 1 {
		t.Fatalf("alex2 mentions after explicit handoff=%d want 1 (should not be notified again)", got)
	}

	historyResp := h.request(http.MethodGet, base+"/groups/"+group.Id.String()+"/messages", nil, owner.AccessToken, nil, false)
	if historyResp.status != http.StatusOK {
		t.Fatalf("get messages=%d %s", historyResp.status, historyResp.body)
	}
	var history apigen.ChatHistory
	historyResp.decode(t, &history)
	for _, msg := range history.Messages {
		if strings.Contains(msg.Content, "Handoff to human:") {
			t.Fatalf("control directive leaked into stored transcript: %q", msg.Content)
		}
	}
}
