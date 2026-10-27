package api_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/api"
	"github.com/dunkeltech/botbox/backend/internal/approvals"
	"github.com/dunkeltech/botbox/backend/internal/budgets"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestPhase6ApprovalDecisions(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue, chatLimit: 300})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	atlas := createAPIBot(t, h, base, owner.AccessToken, "Atlas", "atlas-seed")
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member@example.com", "member", "Member", "CorrectHorse123!")
	admin, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "admin@example.com", "admin", "Admin", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "viewer@example.com", "viewer", "Viewer", "CorrectHorse123!")
	other := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other", "slug": "other"}, owner.AccessToken, nil, true)
	var otherWS apigen.WorkspaceSummary
	other.decode(t, &otherWS)
	eve, _ := h.inviteAndAcceptNewUser(owner.AccessToken, otherWS.Id, "eve@other.com", "owner", "Eve", "CorrectHorse123!")

	reply := "done"
	var tool string
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Enqueue: queue.Enqueue, Execute: func(_ context.Context, _ engine.Session, emit func(engine.Event) error) error {
		if tool != "" {
			if err := emit(engine.Event{Kind: engine.EventKindToolCall, ToolName: "git", ToolInput: []byte(tool)}); err != nil {
				return err
			}
		}
		if reply == "" {
			return nil
		}
		return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: reply})
	}}
	members := []any{groupUser(viewer.User.Id, "reader"), groupBot(atlas.Id, "lead")}
	customRule := []any{map[string]any{"kind": "custom", "label": "ship the release"}}

	if r := h.request(http.MethodGet, base+"/approvals", nil, "", nil, false); r.status != http.StatusUnauthorized {
		t.Fatalf("unauth list=%d", r.status)
	}
	if r := h.request(http.MethodGet, base+"/approvals?status=nope", nil, member.AccessToken, nil, false); r.status != http.StatusBadRequest || r.errorCode(t) != "validation_error" {
		t.Fatalf("status=%d %s", r.status, r.body)
	}

	push := askGroup(t, h, base, member.AccessToken, "Push", atlas.Id, false, 20, []any{map[string]any{"kind": "push_protected", "label": ""}}, members)
	tool, reply = "push the remaining notes", ""
	postGroup(t, h, base, member.AccessToken, push.Id, "@Atlas status")
	drainTurns(t, worker, queue, 2)
	if paused := getGroup(t, h, base, member.AccessToken, push.Id); paused.Paused || len(queue.jobs) != 0 {
		t.Fatalf("unmatched push paused=%v jobs=%d", paused.Paused, len(queue.jobs))
	}
	tool = "push origin main"
	postGroup(t, h, base, member.AccessToken, push.Id, "@Atlas publish")
	drainTurns(t, worker, queue, 2)
	pushCard := findApproval(t, listApprovals(t, h, base, member.AccessToken, "pending"), apigen.ApprovalKindPushProtected, "")
	if pushCard.Branch == nil || *pushCard.Branch != "main" || bytes.Contains(mustListBody(t, h, base, member.AccessToken), []byte("triggerMessageId")) {
		t.Fatalf("push card leaked or branch=%v", pushCard.Branch)
	}
	pushed := getGroup(t, h, base, member.AccessToken, push.Id)
	if !pushed.Paused || pushed.PauseReason != "approval" || !historyHas(groupHistory(t, h, base, member.AccessToken, push.Id), "Protected push · main") {
		t.Fatalf("push pause=%s", mustJSON(pushed))
	}

	tool, reply = "", "please ship the release"
	ship := askGroup(t, h, base, member.AccessToken, "Ship", atlas.Id, true, 20, customRule, members)
	postGroup(t, h, base, member.AccessToken, ship.Id, "@Atlas go")
	drainTurns(t, worker, queue, 3)
	card := findApproval(t, listApprovals(t, h, base, member.AccessToken, ""), apigen.ApprovalKindCustom, "")
	if card.Label == nil || *card.Label != "ship the release" {
		t.Fatalf("label=%v", card.Label)
	}
	before := getGroup(t, h, base, member.AccessToken, ship.Id)
	if !before.Paused || before.PauseReason != "approval" || before.TurnsSinceHuman != 1 || len(queue.jobs) != 0 {
		t.Fatalf("custom pause=%s jobs=%d", mustJSON(before), len(queue.jobs))
	}
	ownerList := listApprovals(t, h, base, owner.AccessToken, "pending")
	if approvalIDs(ownerList)[card.Id] {
		t.Fatal("non-member owner saw the group approval")
	}
	if !approvalIDs(listApprovals(t, h, base, viewer.AccessToken, "pending"))[card.Id] {
		t.Fatal("viewer member cannot see the card")
	}
	decision := base + "/approvals/" + card.Id.String() + "/decision"
	if r := h.request(http.MethodPost, decision, map[string]any{"decision": "approved"}, "", nil, true); r.status != http.StatusUnauthorized {
		t.Fatalf("unauth decide=%d", r.status)
	}
	if r := h.request(http.MethodPost, decision, map[string]any{"decision": "approved"}, member.AccessToken, nil, false); r.status != http.StatusForbidden || r.errorCode(t) != "auth.csrf_required" {
		t.Fatalf("csrf=%d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, decision, map[string]any{}, member.AccessToken, nil, true); r.status != http.StatusBadRequest {
		t.Fatalf("missing=%d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, decision, map[string]any{"decision": "maybe"}, member.AccessToken, nil, true); r.status != http.StatusBadRequest || r.errorCode(t) != "validation_error" {
		t.Fatalf("bad decision=%d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, decision, map[string]any{"decision": "approved"}, viewer.AccessToken, nil, true); r.status != http.StatusForbidden || r.errorCode(t) != "auth.forbidden" {
		t.Fatalf("viewer=%d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, base+"/approvals/"+card.Id.String()+"/decision", map[string]any{"decision": "approved"}, eve.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("cross workspace=%d %s", r.status, r.body)
	}
	otherBase := "/api/workspaces/" + otherWS.Id.String()
	if r := h.request(http.MethodPost, otherBase+"/approvals/"+card.Id.String()+"/decision", map[string]any{"decision": "approved"}, eve.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("other workspace id=%d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, decision, map[string]any{"decision": "approved"}, member.AccessToken, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("approve=%d %s", r.status, r.body)
	}
	resumed := getGroup(t, h, base, member.AccessToken, ship.Id)
	if resumed.Paused || resumed.TurnsSinceHuman != 1 || len(queue.jobs) != 1 {
		t.Fatalf("resume paused=%v turns=%d jobs=%d", resumed.Paused, resumed.TurnsSinceHuman, len(queue.jobs))
	}
	if r := h.request(http.MethodPost, decision, map[string]any{"decision": "denied"}, member.AccessToken, nil, true); r.status != http.StatusConflict || r.errorCode(t) != "approval.conflict" {
		t.Fatalf("second=%d %s", r.status, r.body)
	}
	assertDecisionAudit(t, h, ws, "approved", "custom")
	reply = "done"
	drainTurns(t, worker, queue, 2)

	tool, reply = "", "ship the release"
	denied := askGroup(t, h, base, member.AccessToken, "Deny", atlas.Id, false, 20, customRule, members)
	postGroup(t, h, base, member.AccessToken, denied.Id, "check this")
	drainTurns(t, worker, queue, 2)
	denyCard := findApproval(t, listApprovals(t, h, base, member.AccessToken, "pending"), apigen.ApprovalKindCustom, "")
	if r := h.request(http.MethodPost, base+"/approvals/"+denyCard.Id.String()+"/decision", map[string]any{"decision": "denied"}, admin.AccessToken, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("deny=%d %s", r.status, r.body)
	}
	afterDeny := getGroup(t, h, base, member.AccessToken, denied.Id)
	if !afterDeny.Paused || afterDeny.PauseReason != "approval" || afterDeny.TurnsSinceHuman != 1 || len(queue.jobs) != 0 {
		t.Fatalf("deny left paused=%s jobs=%d", mustJSON(afterDeny), len(queue.jobs))
	}
	if !historyHas(groupHistory(t, h, base, member.AccessToken, denied.Id), "Denied · this request stays blocked") {
		t.Fatal("missing deny notice")
	}

	reply = "done"
	limited := askGroup(t, h, base, member.AccessToken, "Limit", atlas.Id, true, 1, customRule, members)
	postGroup(t, h, base, member.AccessToken, limited.Id, "once")
	drainTurns(t, worker, queue, 3)
	limitedGroup := getGroup(t, h, base, member.AccessToken, limited.Id)
	if !limitedGroup.Paused || limitedGroup.PauseReason != "turn_limit" || limitedGroup.TurnsSinceHuman != 1 {
		t.Fatalf("limit=%s", mustJSON(limitedGroup))
	}
	var trigger uuid.UUID
	for _, message := range groupHistory(t, h, base, member.AccessToken, limited.Id).Messages {
		if message.SenderType == apigen.ChatMessageSenderTypeUser {
			trigger = message.Id
		}
	}
	tx, err := h.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = approvals.Open(context.Background(), db.New(tx), approvals.Card{
		WorkspaceID:      pgtype.UUID{Bytes: ws, Valid: true},
		ConversationID:   pgtype.UUID{Bytes: limited.Id, Valid: true},
		BotID:            pgtype.UUID{Bytes: atlas.Id, Valid: true},
		TriggerMessageID: pgtype.UUID{Bytes: trigger, Valid: true},
		Kind:             "custom",
		Label:            "ship the release",
	}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	limitCard := findApproval(t, listApprovals(t, h, base, member.AccessToken, "pending"), apigen.ApprovalKindCustom, "")
	if r := h.request(http.MethodPost, base+"/approvals/"+limitCard.Id.String()+"/decision", map[string]any{"decision": "approved"}, member.AccessToken, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("limit decide=%d %s", r.status, r.body)
	}
	stays := getGroup(t, h, base, member.AccessToken, limited.Id)
	if !stays.Paused || stays.PauseReason != "turn_limit" || stays.TurnsSinceHuman != 1 || len(queue.jobs) != 0 {
		t.Fatalf("turn limit moved=%s jobs=%d", mustJSON(stays), len(queue.jobs))
	}

	quill := createAPIBot(t, h, base, owner.AccessToken, "Quill", "quill-seed")
	reply = "ship the release"
	full := askGroup(t, h, base, member.AccessToken, "Full", quill.Id, false, 20, customRule, []any{groupBot(quill.Id, "lead"), groupUser(viewer.User.Id, "reader")})
	postGroup(t, h, base, member.AccessToken, full.Id, "fill card")
	drainTurns(t, worker, queue, 2)
	fullCard := findApproval(t, listApprovals(t, h, base, member.AccessToken, "pending"), apigen.ApprovalKindCustom, "")
	filler := askGroup(t, h, base, member.AccessToken, "Filler", quill.Id, false, 100, []any{}, []any{groupBot(quill.Id, "lead")})
	for range 100 {
		postGroup(t, h, base, member.AccessToken, filler.Id, "fill")
	}
	if len(queue.jobs) != 100 {
		t.Fatalf("filled %d", len(queue.jobs))
	}
	if r := h.request(http.MethodPost, base+"/approvals/"+fullCard.Id.String()+"/decision", map[string]any{"decision": "approved"}, member.AccessToken, nil, true); r.status != http.StatusTooManyRequests || r.errorCode(t) != "chat.queue_full" {
		t.Fatalf("queue=%d %s", r.status, r.body)
	}
	if len(queue.jobs) != 100 {
		t.Fatalf("rollback queued %d", len(queue.jobs))
	}
	still := findApproval(t, listApprovals(t, h, base, member.AccessToken, "pending"), apigen.ApprovalKindCustom, "")
	if still.Id != fullCard.Id || still.Status != apigen.ApprovalStatusPending {
		t.Fatalf("decision was kept %+v", still)
	}

	meter := createAPIBot(t, h, base, owner.AccessToken, "Meter", "meter-seed")
	budget := map[string]any{"botId": meter.Id, "period": "day", "limitMicros": 100}
	price := map[string]any{"provider": "openrouter", "model": "fixture", "inputMicrosPerMillion": 1000000, "outputMicrosPerMillion": 1000000, "cacheMicrosPerMillion": 1000000}
	if r := h.request(http.MethodPut, base+"/budgets", budget, owner.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("budget=%d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPut, base+"/model-prices", price, owner.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("price=%d %s", r.status, r.body)
	}
	opened := h.request(http.MethodPost, base+"/bots/"+meter.Id.String()+"/dm", nil, owner.AccessToken, nil, true)
	var conversation apigen.ChatConversation
	opened.decode(t, &conversation)
	if r := h.request(http.MethodPost, base+"/conversations/"+conversation.Id.String()+"/messages", map[string]any{"id": uuid.New(), "content": "Meter this turn"}, owner.AccessToken, nil, true); r.status != http.StatusAccepted {
		t.Fatalf("dm=%d %s", r.status, r.body)
	}
	meterJob := queue.jobs[len(queue.jobs)-1]
	if meterJob.BotID != meter.Id {
		t.Fatalf("meter job=%v", meterJob.BotID)
	}
	worker.Execute = func(_ context.Context, _ engine.Session, emit func(engine.Event) error) error {
		return emit(engine.Event{Kind: engine.EventKindUsage, InputTokens: 100})
	}
	if err := worker.Process(context.Background(), meterJob); err != nil {
		t.Fatal(err)
	}
	if err := budgets.Check(context.Background(), h.queries, ws, meter.Id, h.clock.Now()); !errors.Is(err, llmproxy.ErrBudgetExceeded) {
		t.Fatalf("phase4 check=%v", err)
	}
	phase4 := findApproval(t, listApprovals(t, h, base, admin.AccessToken, "pending"), apigen.ApprovalKindRaiseBudget, "")
	if phase4.BudgetId == nil || phase4.ConversationId != nil {
		t.Fatalf("phase4 card=%+v", phase4)
	}
	if approvalIDs(listApprovals(t, h, base, member.AccessToken, "pending"))[phase4.Id] {
		t.Fatal("member saw a workspace budget approval")
	}
	if r := h.request(http.MethodPost, base+"/approvals/"+phase4.Id.String()+"/decision", map[string]any{"decision": "approved"}, member.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("member phase4=%d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, base+"/approvals/"+phase4.Id.String()+"/decision", map[string]any{"decision": "approved"}, admin.AccessToken, nil, true); r.status != http.StatusConflict || r.errorCode(t) != "approval.cap_not_raised" {
		t.Fatalf("cap=%d %s", r.status, r.body)
	}
	budget["limitMicros"] = 200
	if r := h.request(http.MethodPut, base+"/budgets", budget, admin.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("raise=%d %s", r.status, r.body)
	}
	if approvalIDs(listApprovals(t, h, base, admin.AccessToken, "pending"))[phase4.Id] {
		t.Fatal("budget endpoint left the phase 4 approval pending")
	}
}

func TestPhase6BlockedHostApproval(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue, chatLimit: 20})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	atlas := createAPIBot(t, h, base, owner.AccessToken, "Atlas", "atlas-seed")
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member@example.com", "member", "Member", "CorrectHorse123!")
	if r := h.request(http.MethodPatch, base+"/bots/"+atlas.Id.String()+"/egress", map[string]any{"policy": "allowlist", "allowedHosts": []string{"docs.example.com"}, "expectedRevision": 0}, owner.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("egress=%d %s", r.status, r.body)
	}
	group := askGroup(t, h, base, member.AccessToken, "Hosts", atlas.Id, false, 20, []any{map[string]any{"kind": "new_host"}}, []any{groupBot(atlas.Id, "lead")})
	started := make(chan struct{})
	release := make(chan struct{})
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Enqueue: queue.Enqueue, Execute: func(_ context.Context, _ engine.Session, emit func(engine.Event) error) error {
		close(started)
		<-release
		return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "done"})
	}}
	postGroup(t, h, base, member.AccessToken, group.Id, "look up the docs")
	errCh := make(chan error, 1)
	job := queue.jobs[0]
	queue.jobs = queue.jobs[1:]
	go func() {
		errCh <- worker.Process(context.Background(), job)
	}()
	select {
	case err := <-errCh:
		t.Fatal(err)
	case <-started:
	}
	client, upstream := botProxyClient(t, h, ws, atlas.Id)
	defer upstream.Close()
	if resp, err := client.Get("https://docs.example.com/guide"); err != nil {
		t.Fatalf("allowed host failed: %v", err)
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	mustFailGet(t, client, "https://evil.example.com/secret")
	mustFailGet(t, client, "https://port.example.com:8443/secret")
	mustFailGet(t, client, "https://sneak.example.com/secret")
	close(release)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	pending := listApprovals(t, h, base, member.AccessToken, "pending")
	evil := findApproval(t, pending, apigen.ApprovalKindNewHost, "evil.example.com")
	sneak := findApproval(t, pending, apigen.ApprovalKindNewHost, "sneak.example.com")
	for _, row := range pending.Approvals {
		if row.Host != nil && *row.Host == "docs.example.com" {
			t.Fatal("allowed host created a card")
		}
		if row.Host != nil && *row.Host == "port.example.com" {
			t.Fatal("port block created a host card")
		}
	}
	if bytes.Contains(mustListBody(t, h, base, member.AccessToken), []byte("triggerMessageId")) {
		t.Fatal("list exposed triggerMessageId")
	}
	if r := h.request(http.MethodPatch, base+"/bots/"+atlas.Id.String()+"/egress", map[string]any{"policy": "allowlist", "allowedHosts": []string{"docs.example.com", "sneak.example.com"}, "expectedRevision": 1}, owner.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("patch sneak=%d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, base+"/approvals/"+sneak.Id.String()+"/decision", map[string]any{"decision": "approved"}, member.AccessToken, nil, true); r.status != http.StatusConflict || r.errorCode(t) != "approval.host_not_blocked" {
		t.Fatalf("already allowed=%d %s", r.status, r.body)
	}
	if findApproval(t, listApprovals(t, h, base, member.AccessToken, "pending"), apigen.ApprovalKindNewHost, "sneak.example.com").Id != sneak.Id {
		t.Fatal("rejected approval was closed")
	}
	mustFailGet(t, client, "https://evil.example.com/still")
	if r := h.request(http.MethodPost, base+"/approvals/"+evil.Id.String()+"/decision", map[string]any{"decision": "approved"}, member.AccessToken, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("approve host=%d %s", r.status, r.body)
	}
	hosts := egressHosts(t, h, atlas.Id)
	if len(hosts) != 3 || !containsAll(hosts, "docs.example.com", "sneak.example.com", "evil.example.com") {
		t.Fatalf("hosts=%v", hosts)
	}
	if resp, err := client.Get("https://evil.example.com/ok"); err != nil {
		t.Fatalf("approved host failed: %v", err)
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	if r := h.request(http.MethodPost, base+"/approvals/"+evil.Id.String()+"/decision", map[string]any{"decision": "denied"}, member.AccessToken, nil, true); r.status != http.StatusConflict {
		t.Fatalf("second host decision=%d %s", r.status, r.body)
	}
	assertDecisionAudit(t, h, ws, "approved", "new_host")
	// The approval queues Atlas again. Drain it before the next group, or the
	// older turn blocks the new one.
	worker.Execute = func(_ context.Context, _ engine.Session, emit func(engine.Event) error) error {
		return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "done"})
	}
	drainTurns(t, worker, queue, 2)

	denied := askGroup(t, h, base, member.AccessToken, "Deny host", atlas.Id, false, 20, []any{map[string]any{"kind": "new_host"}}, []any{groupBot(atlas.Id, "lead")})
	started = make(chan struct{})
	release = make(chan struct{})
	worker.Execute = func(_ context.Context, _ engine.Session, emit func(engine.Event) error) error {
		close(started)
		<-release
		return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "done"})
	}
	postGroup(t, h, base, member.AccessToken, denied.Id, "again")
	job = queue.jobs[0]
	queue.jobs = queue.jobs[1:]
	go func() {
		errCh <- worker.Process(context.Background(), job)
	}()
	select {
	case err := <-errCh:
		t.Fatal(err)
	case <-started:
	}
	before := egressHosts(t, h, atlas.Id)
	mustFailGet(t, client, "https://blocked.example.com/no")
	close(release)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	blocked := findApproval(t, listApprovals(t, h, base, member.AccessToken, "pending"), apigen.ApprovalKindNewHost, "blocked.example.com")
	if r := h.request(http.MethodPost, base+"/approvals/"+blocked.Id.String()+"/decision", map[string]any{"decision": "denied"}, member.AccessToken, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("deny host=%d %s", r.status, r.body)
	}
	if hosts := egressHosts(t, h, atlas.Id); !sameHosts(before, hosts) {
		t.Fatalf("deny changed hosts %v -> %v", before, hosts)
	}
	mustFailGet(t, client, "https://blocked.example.com/no")
	deniedGroup := getGroup(t, h, base, member.AccessToken, denied.Id)
	if !deniedGroup.Paused || deniedGroup.PauseReason != "approval" {
		t.Fatalf("deny host pause=%s", mustJSON(deniedGroup))
	}
}

func askGroup(t *testing.T, h *testHarness, base, token, name string, lead uuid.UUID, botTalk bool, turnLimit int, rules []any, members []any) apigen.Group {
	t.Helper()
	body := newGroupBody(uuid.New(), name, lead, members)
	body["botTalk"] = botTalk
	body["turnLimit"] = turnLimit
	body["askBefore"] = rules
	r := h.request(http.MethodPost, base+"/groups", body, token, nil, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create %s=%d %s", name, r.status, r.body)
	}
	var group apigen.Group
	r.decode(t, &group)
	return group
}

func listApprovals(t *testing.T, h *testHarness, base, token, status string) apigen.ApprovalList {
	t.Helper()
	path := base + "/approvals"
	if status != "" {
		path += "?status=" + status
	}
	r := h.request(http.MethodGet, path, nil, token, nil, false)
	if r.status != http.StatusOK {
		t.Fatalf("list=%d %s", r.status, r.body)
	}
	var out apigen.ApprovalList
	r.decode(t, &out)
	return out
}

func mustListBody(t *testing.T, h *testHarness, base, token string) []byte {
	t.Helper()
	r := h.request(http.MethodGet, base+"/approvals", nil, token, nil, false)
	if r.status != http.StatusOK {
		t.Fatalf("list=%d %s", r.status, r.body)
	}
	return r.body
}

func findApproval(t *testing.T, list apigen.ApprovalList, kind apigen.ApprovalKind, host string) apigen.Approval {
	t.Helper()
	for _, row := range list.Approvals {
		if row.Kind != kind || row.Status != apigen.ApprovalStatusPending {
			continue
		}
		if host != "" && (row.Host == nil || *row.Host != host) {
			continue
		}
		return row
	}
	t.Fatalf("missing pending %s host=%s in %d rows", kind, host, len(list.Approvals))
	return apigen.Approval{}
}

func approvalIDs(list apigen.ApprovalList) map[uuid.UUID]bool {
	out := map[uuid.UUID]bool{}
	for _, row := range list.Approvals {
		out[row.Id] = true
	}
	return out
}

func assertDecisionAudit(t *testing.T, h *testHarness, ws uuid.UUID, decision, kind string) {
	t.Helper()
	var raw []byte
	if err := h.pool.QueryRow(context.Background(), `SELECT metadata FROM audit_log WHERE workspace_id=$1 AND action='approval.decided' ORDER BY created_at DESC LIMIT 1`, ws).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var meta map[string]string
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if len(meta) != 2 || meta["decision"] != decision || meta["kind"] != kind {
		t.Fatalf("audit=%s", raw)
	}
}

func egressHosts(t *testing.T, h *testHarness, bot uuid.UUID) []string {
	t.Helper()
	var hosts []string
	if err := h.pool.QueryRow(context.Background(), `SELECT allowed_hosts FROM egress_rules WHERE bot_id=$1`, bot).Scan(&hosts); err != nil {
		t.Fatal(err)
	}
	return hosts
}

func mustFailGet(t *testing.T, client *http.Client, rawURL string) {
	t.Helper()
	resp, err := client.Get(rawURL)
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatalf("%s was reachable", rawURL)
	}
}

func containsAll(got []string, want ...string) bool {
	have := map[string]bool{}
	for _, host := range got {
		have[host] = true
	}
	for _, host := range want {
		if !have[host] {
			return false
		}
	}
	return true
}

func sameHosts(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	return containsAll(a, b...) && containsAll(b, a...)
}

func botProxyClient(t *testing.T, h *testHarness, ws, bot uuid.UUID) (*http.Client, *httptest.Server) {
	t.Helper()
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	ca, err := mitmproxy.NewBotCA(bot.String())
	if err != nil {
		t.Fatal(err)
	}
	upURL, _ := url.Parse(upstream.URL)
	handler, err := api.NewBotEgressProxyHandler(api.BotEgressProxyDeps{
		Pool:            h.pool,
		KeyRing:         h.keyRing,
		WorkspaceID:     ws,
		BotID:           bot,
		CA:              ca,
		UpstreamRootCAs: upstream.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, upURL.Host)
		},
		Logger: h.logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxySrv := httptest.NewServer(handler)
	t.Cleanup(proxySrv.Close)
	proxyURL, _ := url.Parse(proxySrv.URL)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca.CertPEM())
	roots.AddCert(upstream.Certificate())
	client := &http.Client{Transport: &http.Transport{
		Proxy:             http.ProxyURL(proxyURL),
		DisableKeepAlives: true,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
		},
	}}
	return client, upstream
}
