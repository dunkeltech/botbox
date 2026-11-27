// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	migrations "github.com/dunkeltech/botbox/backend/db"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/testutil/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestDeleteBotWithChatHistory(t *testing.T) {
	h := newTestHarness(t, pgtest.OpenAt(t, 36), harnessOpts{})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	bot := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas")
	stale := createPiBot(t, h, base, owner.AccessToken, "Nova", "nova")
	botPath := base + "/bots/" + bot.Id.String()
	groupID := uuid.New()
	groupPath := base + "/groups/" + groupID.String()
	groupBody := newGroupBody(groupID, "Launch", bot.Id, []any{groupBot(bot.Id, "lead"), groupBot(stale.Id, "helper")})
	if r := h.request(http.MethodPost, base+"/groups", groupBody, owner.AccessToken, nil, true); r.status != http.StatusCreated {
		t.Fatalf("create group: %d %s", r.status, r.body)
	}
	if r := h.request(http.MethodDelete, base+"/bots/"+stale.Id.String(), nil, owner.AccessToken, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("delete non-lead before migration: %d %s", r.status, r.body)
	}
	opened := h.request(http.MethodPost, botPath+"/dm", nil, owner.AccessToken, nil, true)
	if opened.status != http.StatusOK && opened.status != http.StatusCreated {
		t.Fatalf("open DM: %d %s", opened.status, opened.body)
	}
	var dm apigen.ChatConversation
	opened.decode(t, &dm)

	ctx := context.Background()
	insertReply := `INSERT INTO messages(workspace_id, conversation_id, sender_type, sender_bot_id, content)
		VALUES ($1, $2, 'bot', $3, 'I finished the task')`
	for _, conversationID := range []uuid.UUID{dm.Id, groupID} {
		if _, err := h.pool.Exec(ctx, insertReply, ws, conversationID, bot.Id); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrations.MigrateUp(ctx, h.pool); err != nil {
		t.Fatalf("upgrade with chat history: %v", err)
	}
	otherResponse := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other", "slug": "other"}, owner.AccessToken, nil, true)
	if otherResponse.status != http.StatusCreated {
		t.Fatalf("create other workspace: %d %s", otherResponse.status, otherResponse.body)
	}
	var other apigen.WorkspaceSummary
	otherResponse.decode(t, &other)
	otherBot := createPiBot(t, h, "/api/workspaces/"+other.Id.String(), owner.AccessToken, "Scout", "scout")
	deleteResponse := h.request(http.MethodDelete, botPath, nil, owner.AccessToken, nil, true)
	if deleteResponse.status != http.StatusNoContent {
		t.Fatalf("delete bot: %d %s logs=%s", deleteResponse.status, deleteResponse.body, h.logs)
	}
	for _, path := range []string{botPath, base + "/conversations/" + dm.Id.String() + "/messages"} {
		if r := h.request(http.MethodGet, path, nil, owner.AccessToken, nil, false); r.status != http.StatusNotFound {
			t.Fatalf("get %s after deletion: %d %s", path, r.status, r.body)
		}
	}
	if r := h.request(http.MethodDelete, botPath, nil, owner.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("repeat delete: %d %s", r.status, r.body)
	}
	read := h.request(http.MethodGet, groupPath+"/messages", nil, owner.AccessToken, nil, false)
	if read.status != http.StatusOK {
		t.Fatalf("get group history: %d %s", read.status, read.body)
	}
	var history apigen.ChatHistory
	read.decode(t, &history)
	if len(history.Messages) != 1 {
		t.Fatalf("group messages: %s", read.body)
	}
	message := history.Messages[0]
	if message.Content != "I finished the task" || message.SenderType != "bot" || message.SenderBotId == nil || *message.SenderBotId != bot.Id {
		t.Fatalf("group message lost its author or content: %s", read.body)
	}
	for _, sender := range []struct {
		name string
		id   uuid.UUID
	}{{"deleted", bot.Id}, {"other workspace", otherBot.Id}} {
		t.Run(sender.name, func(t *testing.T) {
			_, err := h.pool.Exec(ctx, insertReply, ws, groupID, sender.id)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
				t.Fatalf("insert with invalid author: %v", err)
			}
		})
	}

	group := h.request(http.MethodGet, groupPath, nil, owner.AccessToken, nil, false)
	var current apigen.Group
	group.decode(t, &current)
	if group.status != http.StatusOK || current.LeadBotId != nil || len(current.Members) != 1 || current.Members[0].Id != owner.User.Id {
		t.Fatalf("group after bot deletion: %d %s", group.status, group.body)
	}
	updated := h.request(http.MethodPatch, groupPath, map[string]any{
		"name": "Launch", "leadBotId": nil, "botTalk": false, "turnLimit": 10,
		"answerPolicy": "mentioned_else_lead", "askBefore": []any{}, "members": []any{groupUser(owner.User.Id, "")},
	}, owner.AccessToken, nil, true)
	updated.decode(t, &current)
	if updated.status != http.StatusOK || current.TurnLimit != 10 || current.LeadBotId != nil {
		t.Fatalf("update group without a lead: %d %s", updated.status, updated.body)
	}
}
