// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	migrations "github.com/dunkeltech/botbox/backend/db"
	"github.com/dunkeltech/botbox/backend/internal/auth"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/testutil/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestDeleteBotWithChatHistory(t *testing.T) {
	h := newTestHarness(t, pgtest.OpenAt(t, 36), harnessOpts{})
	ctx := context.Background()

	// Seed v36 schema directly rather than calling current HTTP handlers, because
	// newer migrations add columns queried by the current API server.
	ownerID := uuid.New()
	ws := uuid.New()
	botID := uuid.New()
	staleID := uuid.New()
	groupID := uuid.New()
	dmID := uuid.New()
	hash, err := auth.HashPassword("CorrectHorse123!", auth.DefaultArgon2Params())
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, display_name)
		VALUES ($1, 'owner@example.com', $2, 'Owner')
	`, ownerID, hash); err != nil {
		t.Fatalf("insert owner: %v", err)
	}
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO workspaces (id, name, slug)
		VALUES ($1, 'Workspace', 'workspace')
	`, ws); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO workspace_members (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, ws, ownerID); err != nil {
		t.Fatalf("insert workspace member: %v", err)
	}
	for _, b := range []struct {
		id   uuid.UUID
		name string
		seed string
	}{
		{botID, "Atlas", "atlas"},
		{staleID, "Nova", "nova"},
	} {
		if _, err := h.pool.Exec(ctx, `
			INSERT INTO bots (id, workspace_id, owner_user_id, name, avatar_seed, llm_mode, llm_provider, llm_model)
			VALUES ($1, $2, $3, $4, $5, 'cli', 'openrouter', 'openrouter/fixture')
		`, b.id, ws, ownerID, b.name, b.seed); err != nil {
			t.Fatalf("insert bot %s: %v", b.name, err)
		}
	}
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO conversations (id, workspace_id, type, name, lead_bot_id, bot_talk, turn_limit, answer_policy, ask_before)
		VALUES ($1, $2, 'group', 'Launch', $3, false, 20, 'mentioned_else_lead', '[]')
	`, groupID, ws, botID); err != nil {
		t.Fatalf("insert group: %v", err)
	}
	for _, m := range []struct {
		kind string
		id   uuid.UUID
		role string
	}{
		{"user", ownerID, ""},
		{"bot", botID, "lead"},
		{"bot", staleID, "helper"},
	} {
		if _, err := h.pool.Exec(ctx, `
			INSERT INTO conversation_members (workspace_id, conversation_id, member_type, member_id, role_text)
			VALUES ($1, $2, $3, $4, $5)
		`, ws, groupID, m.kind, m.id, m.role); err != nil {
			t.Fatalf("insert group member %s: %v", m.kind, err)
		}
	}
	if _, err := h.pool.Exec(ctx, `DELETE FROM bots WHERE workspace_id = $1 AND id = $2`, ws, staleID); err != nil {
		t.Fatalf("delete non-lead bot at v36: %v", err)
	}
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO conversations (id, workspace_id, type, user_id, bot_id)
		VALUES ($1, $2, 'dm', $3, $4)
	`, dmID, ws, ownerID, botID); err != nil {
		t.Fatalf("insert dm: %v", err)
	}
	for _, m := range []struct {
		kind string
		id   uuid.UUID
	}{
		{"user", ownerID},
		{"bot", botID},
	} {
		if _, err := h.pool.Exec(ctx, `
			INSERT INTO conversation_members (workspace_id, conversation_id, member_type, member_id)
			VALUES ($1, $2, $3, $4)
		`, ws, dmID, m.kind, m.id); err != nil {
			t.Fatalf("insert dm member %s: %v", m.kind, err)
		}
	}

	insertReply := `INSERT INTO messages(workspace_id, conversation_id, sender_type, sender_bot_id, content)
		VALUES ($1, $2, 'bot', $3, 'I finished the task')`
	for _, conversationID := range []uuid.UUID{dmID, groupID} {
		if _, err := h.pool.Exec(ctx, insertReply, ws, conversationID, botID); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrations.MigrateUp(ctx, h.pool); err != nil {
		t.Fatalf("upgrade with chat history: %v", err)
	}

	loginResp := h.request(http.MethodPost, "/api/auth/login", map[string]any{
		"email":    "owner@example.com",
		"password": "CorrectHorse123!",
	}, "", nil, true)
	if loginResp.status != http.StatusOK {
		t.Fatalf("login after migration: %d %s", loginResp.status, loginResp.body)
	}
	var owner apigen.AuthSessionResponse
	loginResp.decode(t, &owner)

	base := "/api/workspaces/" + ws.String()
	botPath := base + "/bots/" + botID.String()
	groupPath := base + "/groups/" + groupID.String()
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
	for _, path := range []string{botPath, base + "/conversations/" + dmID.String() + "/messages"} {
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
	if message.Content != "I finished the task" || message.SenderType != "bot" || message.SenderBotId == nil || *message.SenderBotId != botID {
		t.Fatalf("group message lost its author or content: %s", read.body)
	}
	for _, sender := range []struct {
		name string
		id   uuid.UUID
	}{{"deleted", botID}, {"other workspace", otherBot.Id}} {
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
	if group.status != http.StatusOK || current.LeadBotId != nil || len(current.Members) != 1 || current.Members[0].Id != ownerID {
		t.Fatalf("group after bot deletion: %d %s", group.status, group.body)
	}
	updated := h.request(http.MethodPatch, groupPath, map[string]any{
		"name": "Launch", "leadBotId": nil, "botTalk": false, "turnLimit": 10,
		"answerPolicy": "mentioned_else_lead", "askBefore": []any{}, "members": []any{groupUser(ownerID, "")},
	}, owner.AccessToken, nil, true)
	updated.decode(t, &current)
	if updated.status != http.StatusOK || current.TurnLimit != 10 || current.LeadBotId != nil {
		t.Fatalf("update group without a lead: %d %s", updated.status, updated.body)
	}
}
