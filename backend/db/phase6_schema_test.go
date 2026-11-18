// SPDX-License-Identifier: AGPL-3.0-only

package migrations_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/testutil/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPhase6GroupSchema(t *testing.T) {
	pool := pgtest.Open(t)
	ctx := context.Background()
	ws, user, bot := seedWorkspace(t, ctx, pool, "alpha")
	_, _, otherBot := seedWorkspace(t, ctx, pool, "beta")
	second := insertBot(t, ctx, pool, ws, "second")
	human := insertUser(t, ctx, pool, ws, "member")

	dm := insertDM(t, ctx, pool, ws, user, bot)
	if _, err := pool.Exec(ctx, `INSERT INTO conversations(workspace_id,user_id,bot_id) VALUES($1,$2,$3)`, ws, user, bot); err == nil || !isCode(err, "23505") {
		t.Fatalf("duplicate dm err=%v", err)
	}
	group := insertGroup(t, ctx, pool, ws, bot)
	addMember(t, ctx, pool, ws, group, "bot", bot)
	addMember(t, ctx, pool, ws, group, "bot", second)
	addMember(t, ctx, pool, ws, group, "user", user)
	addMember(t, ctx, pool, ws, group, "user", human)

	msg := insertMessage(t, ctx, pool, ws, group, user)
	secondMsg := insertMessage(t, ctx, pool, ws, group, user)
	dmMsg := insertMessage(t, ctx, pool, ws, dm, user)
	outsiderMsg := insertMessage(t, ctx, pool, ws, group, user)
	if _, err := pool.Exec(ctx, `INSERT INTO bot_turns(workspace_id,bot_id,conversation_id,message_id) VALUES($1,$2,$3,$4)`, ws, bot, group, msg); err != nil {
		t.Fatalf("member turn: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO bot_turns(workspace_id,bot_id,conversation_id,message_id) VALUES($1,$2,$3,$4)`, ws, second, group, secondMsg); err != nil {
		t.Fatalf("second member turn: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO bot_turns(workspace_id,bot_id,conversation_id,message_id) VALUES($1,$2,$3,$4)`, ws, second, dm, dmMsg); err == nil {
		t.Fatal("dm accepted another bot")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO bot_turns(workspace_id,bot_id,conversation_id,message_id) VALUES($1,$2,$3,$4)`, ws, otherBot, group, outsiderMsg); err == nil {
		t.Fatal("group accepted a non-member bot")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO conversations(workspace_id,type,name,lead_bot_id) VALUES($1,'group','nope',$2)`, ws, otherBot); err == nil || !isCode(err, "23503") {
		t.Fatalf("cross-workspace lead err=%v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO conversation_members(workspace_id,conversation_id,member_type,member_id) VALUES($1,$2,'bot',$3)`, ws, group, otherBot); err == nil {
		t.Fatal("cross-workspace member stored")
	}
	if _, err := pool.Exec(ctx, `UPDATE conversations SET answer_policy='all' WHERE id=$1`, group); err == nil || !isCode(err, "23514") {
		t.Fatalf("policy err=%v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE conversations SET turn_limit=0 WHERE id=$1`, group); err == nil || !isCode(err, "23514") {
		t.Fatalf("turn limit err=%v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE conversation_members SET notify='shout' WHERE conversation_id=$1 AND member_id=$2`, group, user); err == nil || !isCode(err, "23514") {
		t.Fatalf("notify err=%v", err)
	}

	budget := insertBudget(t, ctx, pool, ws, bot)
	if _, err := pool.Exec(ctx, `INSERT INTO approvals(workspace_id,bot_id,budget_id) VALUES($1,$2,$3)`, ws, bot, budget); err != nil {
		t.Fatalf("phase4 approval: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO approvals(workspace_id,bot_id,budget_id) VALUES($1,$2,$3)`, ws, bot, budget); err == nil || !isCode(err, "23505") {
		t.Fatalf("duplicate budget approval err=%v", err)
	}
	payload := `{"host":"example.com"}`
	if _, err := pool.Exec(ctx, `INSERT INTO approvals(workspace_id,bot_id,conversation_id,kind,payload) VALUES($1,$2,$3,'new_host',$4)`, ws, bot, group, payload); err != nil {
		t.Fatalf("new host: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO approvals(workspace_id,bot_id,conversation_id,kind,payload) VALUES($1,$2,$3,'new_host',$4)`, ws, bot, group, payload); err == nil || !isCode(err, "23505") {
		t.Fatalf("duplicate host err=%v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO approvals(workspace_id,bot_id,conversation_id,kind,payload) VALUES($1,$2,$3,'new_host','{"host":"other.example"}')`, ws, bot, group); err != nil {
		t.Fatalf("second host: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO approvals(workspace_id,bot_id,kind) VALUES($1,$2,'delete')`, ws, bot); err == nil || !isCode(err, "23514") {
		t.Fatalf("kind err=%v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO notifications(workspace_id,user_id,kind) VALUES($1,$2,'mention')`, ws, human); err != nil {
		t.Fatalf("notification: %v", err)
	}
}

func seedWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, slug string) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	user := insertUser(t, ctx, pool, uuid.Nil, slug)
	var ws uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces(name,slug) VALUES($1,$2) RETURNING id`, slug, slug).Scan(&ws); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES($1,$2,'owner')`, ws, user); err != nil {
		t.Fatalf("member: %v", err)
	}
	return ws, user, insertBot(t, ctx, pool, ws, slug)
}

func insertUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ws uuid.UUID, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	email := name + "@example.com"
	if err := pool.QueryRow(ctx, `INSERT INTO users(email,password_hash,display_name) VALUES($1,'hash',$2) RETURNING id`, email, name).Scan(&id); err != nil {
		t.Fatalf("user: %v", err)
	}
	if ws != uuid.Nil {
		if _, err := pool.Exec(ctx, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES($1,$2,'member')`, ws, id); err != nil {
			t.Fatalf("join: %v", err)
		}
	}
	return id
}

func insertBot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ws uuid.UUID, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(ctx, `INSERT INTO bots(workspace_id,name,avatar_seed,llm_mode,llm_provider,llm_model) VALUES($1,$2,$3,'api','openrouter','fixture') RETURNING id`, ws, name, name).Scan(&id)
	if err != nil {
		t.Fatalf("bot: %v", err)
	}
	return id
}

func insertDM(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ws, user, bot uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(ctx, `INSERT INTO conversations(workspace_id,user_id,bot_id) VALUES($1,$2,$3) RETURNING id`, ws, user, bot).Scan(&id)
	if err != nil {
		t.Fatalf("dm: %v", err)
	}
	return id
}

func insertGroup(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ws, lead uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(ctx, `INSERT INTO conversations(workspace_id,type,name,lead_bot_id,bot_talk) VALUES($1,'group','odoo',$2,true) RETURNING id`, ws, lead).Scan(&id)
	if err != nil {
		t.Fatalf("group: %v", err)
	}
	return id
}

func addMember(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ws, conversation uuid.UUID, kind string, id uuid.UUID) {
	t.Helper()
	_, err := pool.Exec(ctx, `INSERT INTO conversation_members(workspace_id,conversation_id,member_type,member_id) VALUES($1,$2,$3,$4)`, ws, conversation, kind, id)
	if err != nil {
		t.Fatalf("member %s: %v", kind, err)
	}
}

func insertMessage(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ws, conversation, user uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(ctx, `INSERT INTO messages(workspace_id,conversation_id,sender_type,sender_user_id,content) VALUES($1,$2,'user',$3,'hello') RETURNING id`, ws, conversation, user).Scan(&id)
	if err != nil {
		t.Fatalf("message: %v", err)
	}
	return id
}

func insertBudget(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ws, bot uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(ctx, `INSERT INTO budgets(workspace_id,bot_id,period,limit_micros) VALUES($1,$2,'day',100) RETURNING id`, ws, bot).Scan(&id)
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	return id
}

func isCode(err error, code string) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == code
}
