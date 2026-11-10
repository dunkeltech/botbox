package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/google/uuid"
)

func TestDeleteGroupAuthorization(t *testing.T) {
	h := newTestHarness(t, nil, harnessOpts{})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	bot := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas")
	admin, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "admin@example.com", "admin", "Admin", "CorrectHorse123!")
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member@example.com", "member", "Member", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "viewer@example.com", "viewer", "Viewer", "CorrectHorse123!")
	outsider, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "other@example.com", "admin", "Other", "CorrectHorse123!")
	other := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other", "slug": "other"}, owner.AccessToken, nil, true)
	var otherWS apigen.WorkspaceSummary
	other.decode(t, &otherWS)

	for _, tc := range []struct {
		name, token string
		status      int
		csrf        bool
	}{
		{"unauthenticated", "", http.StatusUnauthorized, true},
		{"csrf", owner.AccessToken, http.StatusForbidden, false},
		{"viewer", viewer.AccessToken, http.StatusForbidden, true},
		{"non-member admin", outsider.AccessToken, http.StatusNotFound, true},
		{"member", member.AccessToken, http.StatusNoContent, true},
		{"admin", admin.AccessToken, http.StatusNoContent, true},
		{"owner", owner.AccessToken, http.StatusNoContent, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := uuid.New()
			body := newGroupBody(id, "Launch", bot.Id, []any{groupBot(bot.Id, "lead"), groupUser(admin.User.Id, ""), groupUser(member.User.Id, ""), groupUser(viewer.User.Id, "")})
			if r := h.request(http.MethodPost, base+"/groups", body, owner.AccessToken, nil, true); r.status != http.StatusCreated {
				t.Fatalf("create=%d %s", r.status, r.body)
			}
			path := base + "/groups/" + id.String()
			if r := h.request(http.MethodDelete, "/api/workspaces/"+otherWS.Id.String()+"/groups/"+id.String(), nil, owner.AccessToken, nil, true); r.status != http.StatusNotFound {
				t.Fatalf("cross workspace=%d %s", r.status, r.body)
			}
			r := h.request(http.MethodDelete, path, nil, tc.token, nil, tc.csrf)
			if r.status != tc.status {
				t.Fatalf("delete=%d want=%d %s", r.status, tc.status, r.body)
			}
			want := http.StatusOK
			if tc.status == http.StatusNoContent {
				want = http.StatusNotFound
				if len(r.body) != 0 {
					t.Fatalf("204 body=%s", r.body)
				}
			}
			if r := h.request(http.MethodGet, path, nil, owner.AccessToken, nil, false); r.status != want {
				t.Fatalf("after delete=%d want=%d", r.status, want)
			}
		})
	}
	for _, tc := range []struct {
		id     string
		status int
	}{
		{"invalid", http.StatusBadRequest},
		{uuid.Nil.String(), http.StatusNotFound},
		{uuid.NewString(), http.StatusNotFound},
	} {
		if r := h.request(http.MethodDelete, base+"/groups/"+tc.id, nil, owner.AccessToken, nil, true); r.status != tc.status {
			t.Fatalf("id=%s status=%d want=%d", tc.id, r.status, tc.status)
		}
	}
	opened := h.request(http.MethodPost, base+"/bots/"+bot.Id.String()+"/dm", nil, owner.AccessToken, nil, true)
	var dm apigen.ChatConversation
	opened.decode(t, &dm)
	if r := h.request(http.MethodDelete, base+"/groups/"+dm.Id.String(), nil, owner.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("dm through group route=%d %s", r.status, r.body)
	}
}

func TestDeleteGroupCascadesButKeepsUsageAndBots(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	bot := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas")
	id := uuid.New()
	created := h.request(http.MethodPost, base+"/groups", newGroupBody(id, "Launch", bot.Id, []any{groupBot(bot.Id, "lead")}), owner.AccessToken, nil, true)
	if created.status != http.StatusCreated {
		t.Fatalf("create=%d %s", created.status, created.body)
	}
	path := base + "/groups/" + id.String()
	for i := 0; i < 2; i++ {
		if r := h.request(http.MethodPost, path+"/messages", map[string]any{"id": uuid.New(), "content": "hello"}, owner.AccessToken, nil, true); r.status != http.StatusAccepted {
			t.Fatalf("post=%d %s", r.status, r.body)
		}
	}
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := h.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE bot_turns SET status='completed' WHERE workspace_id=$1 AND id=$2`, ws, queue.jobs[0].TurnID)
	requestID := uuid.New()
	exec(`INSERT INTO usage_records(workspace_id,bot_id,turn_id,request_id,source,model,input_tokens,output_tokens,cache_tokens,cost_micros)
		VALUES($1,$2,$3,$4,'api_response','fixture',10,20,0,1234)`, ws, bot.Id, queue.jobs[0].TurnID, requestID)
	exec(`INSERT INTO bot_sessions(workspace_id,bot_id,conversation_id,runner,external_session_id) VALUES($1,$2,$3,'api','fixture')`, ws, bot.Id, id)
	exec(`INSERT INTO approvals(workspace_id,bot_id,conversation_id,kind) VALUES($1,$2,$3,'custom')`, ws, bot.Id, id)
	exec(`INSERT INTO notifications(workspace_id,user_id,conversation_id,kind) VALUES($1,$2,$3,'mention')`, ws, owner.User.Id, id)
	if r := h.request(http.MethodDelete, path, nil, owner.AccessToken, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("delete=%d %s logs=%s", r.status, r.body, h.logs)
	}
	for _, table := range []string{"conversation_members", "messages", "bot_turns", "bot_sessions", "approvals", "notifications"} {
		var count int
		if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE workspace_id=$1 AND conversation_id=$2`, ws, id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	var spent int64
	var detached bool
	if err := h.pool.QueryRow(ctx, `SELECT cost_micros,turn_id IS NULL FROM usage_records WHERE workspace_id=$1 AND request_id=$2`, ws, requestID).Scan(&spent, &detached); err != nil || spent != 1234 || !detached {
		t.Fatalf("usage spent=%d detached=%v err=%v", spent, detached, err)
	}
	var audits int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE workspace_id=$1 AND target_id=$2 AND action='group.deleted'`, ws, id).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit=%d err=%v", audits, err)
	}
	for _, suffix := range []string{"", "/messages"} {
		if r := h.request(http.MethodGet, path+suffix, nil, owner.AccessToken, nil, false); r.status != http.StatusNotFound {
			t.Fatalf("deleted read=%d", r.status)
		}
	}
	if r := h.request(http.MethodDelete, path, nil, owner.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("repeat=%d", r.status)
	}
	listed := h.request(http.MethodGet, base+"/groups", nil, owner.AccessToken, nil, false)
	var groups []apigen.Group
	listed.decode(t, &groups)
	if len(groups) != 0 {
		t.Fatalf("groups=%s", listed.body)
	}
	if r := h.request(http.MethodGet, base+"/bots/"+bot.Id.String(), nil, owner.AccessToken, nil, false); r.status != http.StatusOK {
		t.Fatalf("bot deleted=%d", r.status)
	}
	executed := false
	worker := &engine.Worker{Pool: h.pool, Execute: func(context.Context, engine.Session, func(engine.Event) error) error {
		executed = true
		return nil
	}}
	if err := worker.Process(ctx, queue.jobs[1]); err == nil || executed {
		t.Fatalf("deleted queued turn err=%v executed=%v", err, executed)
	}
}

func TestDeleteGroupRejectsActiveWorkAndRollsBackStorageFailure(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	bot := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas")
	id := uuid.New()
	if r := h.request(http.MethodPost, base+"/groups", newGroupBody(id, "Launch", bot.Id, []any{groupBot(bot.Id, "lead")}), owner.AccessToken, nil, true); r.status != http.StatusCreated {
		t.Fatalf("create=%d %s", r.status, r.body)
	}
	path := base + "/groups/" + id.String()
	if r := h.request(http.MethodPost, path+"/messages", map[string]any{"id": uuid.New(), "content": "hello"}, owner.AccessToken, nil, true); r.status != http.StatusAccepted {
		t.Fatalf("post=%d %s", r.status, r.body)
	}
	ctx := context.Background()
	for _, status := range []string{"running", "waiting_secret"} {
		if _, err := h.pool.Exec(ctx, `UPDATE bot_turns SET status=$3 WHERE workspace_id=$1 AND conversation_id=$2`, ws, id, status); err != nil {
			t.Fatal(err)
		}
		r := h.request(http.MethodDelete, path, nil, owner.AccessToken, nil, true)
		if r.status != http.StatusConflict || r.errorCode(t) != "group.busy" {
			t.Fatalf("%s delete=%d %s", status, r.status, r.body)
		}
	}
	if _, err := h.pool.Exec(ctx, `UPDATE bot_turns SET status='completed' WHERE workspace_id=$1 AND conversation_id=$2`, ws, id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `INSERT INTO broker_sessions(workspace_id,bot_id,conversation_id,turn_id,adapter_id,origin,expires_at)
		VALUES($1,$2,$3,$4,'fixture','https://example.com','infinity')`, ws, bot.Id, id, queue.jobs[0].TurnID); err != nil {
		t.Fatal(err)
	}
	if r := h.request(http.MethodDelete, path, nil, owner.AccessToken, nil, true); r.status != http.StatusConflict {
		t.Fatalf("active broker delete=%d %s", r.status, r.body)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE broker_sessions SET status='revoked',revoked_at=now() WHERE workspace_id=$1 AND conversation_id=$2`, ws, id); err != nil {
		t.Fatal(err)
	}
	requestID := uuid.New()
	if _, err := h.pool.Exec(ctx, `INSERT INTO secret_requests(id,workspace_id,bot_id,conversation_id,turn_id,kind,reason,expires_at)
		VALUES($1,$2,$3,$4,$5,'cli_egress','fixture','infinity')`, requestID, ws, bot.Id, id, queue.jobs[0].TurnID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `INSERT INTO secret_leases(workspace_id,bot_id,secret_request_id,turn_id,conversation_id,adapter_id,domain_match,auth_scheme,kek_id,wrapped_dek,encrypted_value,expires_at)
		VALUES($1,$2,$3,$4,$5,'fixture','example.com','bearer','fixture','\x01','\x02','infinity')`, ws, bot.Id, requestID, queue.jobs[0].TurnID, id); err != nil {
		t.Fatal(err)
	}
	if r := h.request(http.MethodDelete, path, nil, owner.AccessToken, nil, true); r.status != http.StatusConflict {
		t.Fatalf("active lease delete=%d %s", r.status, r.body)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE secret_leases SET revoked_at=now() WHERE workspace_id=$1 AND conversation_id=$2`, ws, id); err != nil {
		t.Fatal(err)
	}
	// The audit and cascading delete must commit together, including failures.
	if _, err := h.pool.Exec(ctx, `CREATE FUNCTION reject_group_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private dependency detail'; END $$;
		CREATE TRIGGER reject_group_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_group_audit()`); err != nil {
		t.Fatal(err)
	}
	failed := h.request(http.MethodDelete, path, nil, owner.AccessToken, nil, true)
	if failed.status != http.StatusServiceUnavailable || strings.Contains(string(failed.body), "private dependency detail") {
		t.Fatalf("failure=%d %s", failed.status, failed.body)
	}
	if r := h.request(http.MethodGet, path+"/messages", nil, owner.AccessToken, nil, false); r.status != http.StatusOK || !strings.Contains(string(r.body), "hello") {
		t.Fatalf("rollback=%d %s", r.status, r.body)
	}
	if _, err := h.pool.Exec(ctx, `DROP TRIGGER reject_group_audit ON audit_log`); err != nil {
		t.Fatal(err)
	}
	if r := h.request(http.MethodDelete, path, nil, owner.AccessToken, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("retry=%d %s", r.status, r.body)
	}
}

func TestDeleteGroupCancellationKeepsHistory(t *testing.T) {
	h := newTestHarness(t, nil, harnessOpts{turnQueue: &fakeTurnQueue{}})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	bot := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas")
	id := uuid.New()
	if r := h.request(http.MethodPost, base+"/groups", newGroupBody(id, "Launch", bot.Id, []any{groupBot(bot.Id, "lead")}), owner.AccessToken, nil, true); r.status != http.StatusCreated {
		t.Fatalf("create=%d %s", r.status, r.body)
	}
	path := base + "/groups/" + id.String()
	if r := h.request(http.MethodPost, path+"/messages", map[string]any{"id": uuid.New(), "content": "keep this"}, owner.AccessToken, nil, true); r.status != http.StatusAccepted {
		t.Fatalf("post=%d %s", r.status, r.body)
	}
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `SELECT id FROM conversations WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, id); err != nil {
		t.Fatal(err)
	}
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	req := httptest.NewRequestWithContext(requestCtx, http.MethodDelete, path, nil)
	req.Header.Set("Authorization", "Bearer "+owner.AccessToken)
	req.Header.Set("X-BotBox-CSRF", "1")
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.handler.ServeHTTP(recorder, req)
		close(done)
	}()
	// Wait for the actual database lock, not for an assumed execution delay.
	for {
		var waiting bool
		err = h.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '-- name: GetGroupForUpdate%')`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("cancel status=%d %s", recorder.Code, recorder.Body)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if r := h.request(http.MethodGet, path+"/messages", nil, owner.AccessToken, nil, false); r.status != http.StatusOK || !strings.Contains(string(r.body), "keep this") {
		t.Fatalf("cancelled delete lost history=%d %s", r.status, r.body)
	}
}
