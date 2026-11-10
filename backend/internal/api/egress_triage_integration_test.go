package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/google/uuid"
)

type egressAckFakeQueue struct {
	fakeTurnQueue
	ackFn func(ctx context.Context, ws, bot uuid.UUID, rev int64) (int64, error)
}

func (f *egressAckFakeQueue) PublishEgressPolicyRevision(ctx context.Context, ws, bot uuid.UUID, rev int64) (int64, error) {
	if f.ackFn != nil {
		return f.ackFn(ctx, ws, bot, rev)
	}
	return rev, nil
}

func TestEgressBlocklistValidationRevisionsAndRBAC(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t, nil, harnessOpts{})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Alex", "Acme", "acme")
	wsID := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + wsID.String()

	admin, _ := h.inviteAndAcceptNewUser(owner.AccessToken, wsID, "admin@example.com", "admin", "Ada", "CorrectHorse123!")
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, wsID, "member@example.com", "member", "Mia", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, wsID, "viewer@example.com", "viewer", "Sam", "CorrectHorse123!")

	otherWsResp := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other", "slug": "other"}, owner.AccessToken, nil, true)
	if otherWsResp.status != http.StatusCreated {
		t.Fatalf("create other ws: %d %s", otherWsResp.status, otherWsResp.body)
	}
	var otherWs apigen.WorkspaceSummary
	otherWsResp.decode(t, &otherWs)
	eve, _ := h.inviteAndAcceptNewUser(owner.AccessToken, otherWs.Id, "eve@other.com", "owner", "Eve", "CorrectHorse123!")

	ownerBot := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas-seed")
	memberBot := createPiBot(t, h, base, member.AccessToken, "Courier", "courier-seed")

	atlasEgress := base + "/bots/" + ownerBot.Id.String() + "/egress"
	courierEgress := base + "/bots/" + memberBot.Id.String() + "/egress"

	// Create a bot secret to ensure audit/responses never leak secret values.
	const secretVal = "secret_egress_audit_check_9911"
	if r := h.request(http.MethodPost, base+"/bots/"+ownerBot.Id.String()+"/secrets", map[string]any{
		"name": "GH", "domainMatch": "api.github.com", "authScheme": "bearer", "value": secretVal,
	}, owner.AccessToken, nil, true); r.status != http.StatusCreated {
		t.Fatalf("create secret: %d %s", r.status, r.body)
	}

	// Unauthenticated -> 401, cross-workspace -> 404.
	if r := h.request(http.MethodPatch, atlasEgress, map[string]any{"policy": "allowlist", "allowedHosts": []string{}, "expectedRevision": 0}, "", nil, true); r.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated PATCH = %d, want 401", r.status)
	}
	if r := h.request(http.MethodPatch, atlasEgress, map[string]any{"policy": "allowlist", "allowedHosts": []string{}, "expectedRevision": 0}, eve.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("cross-workspace PATCH = %d, want 404", r.status)
	}

	// RBAC matrix: viewer & non-owner member forbidden (403); admin & bot-owner member allowed (200).
	for _, tc := range []struct {
		name  string
		path  string
		token string
		want  int
	}{
		{"viewer", atlasEgress, viewer.AccessToken, http.StatusForbidden},
		{"non-owner member", atlasEgress, member.AccessToken, http.StatusForbidden},
		{"admin on owner bot", atlasEgress, admin.AccessToken, http.StatusOK},
		{"member on own bot", courierEgress, member.AccessToken, http.StatusOK},
	} {
		r := h.request(http.MethodPatch, tc.path, map[string]any{
			"policy":           "allowlist",
			"allowedHosts":     []string{"pypi.org"},
			"blockedHosts":     []string{"telemetry.example.net"},
			"expectedRevision": 0,
		}, tc.token, nil, true)
		if r.status != tc.want {
			t.Fatalf("RBAC %s: status=%d want=%d body=%s", tc.name, r.status, tc.want, r.body)
		}
	}

	// Missing expectedRevision -> 428 Precondition Required without mutation.
	if r := h.request(http.MethodPatch, atlasEgress, map[string]any{
		"policy":       "open",
		"allowedHosts": []string{},
	}, owner.AccessToken, nil, true); r.status != http.StatusPreconditionRequired {
		t.Fatalf("missing revision = %d, want 428 (%s)", r.status, r.body)
	}

	// Stale expectedRevision -> 409 policy_conflict without mutation.
	if r := h.request(http.MethodPatch, atlasEgress, map[string]any{
		"policy":           "open",
		"allowedHosts":     []string{},
		"expectedRevision": 0,
	}, owner.AccessToken, nil, true); r.status != http.StatusConflict || r.errorCode(t) != "policy_conflict" {
		t.Fatalf("stale revision = %d %s, want 409 policy_conflict", r.status, r.body)
	}

	// Validation table for invalid/null/wrong-type/limit/overlap inputs.
	tooMany := make([]string, 201)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("h%d.example.com", i)
	}
	validationCases := []struct {
		name string
		body map[string]any
	}{
		{"null expectedRevision", map[string]any{"policy": "allowlist", "allowedHosts": []string{}, "expectedRevision": nil}},
		{"negative expectedRevision", map[string]any{"policy": "allowlist", "allowedHosts": []string{}, "expectedRevision": -1}},
		{"string expectedRevision", map[string]any{"policy": "allowlist", "allowedHosts": []string{}, "expectedRevision": "1"}},
		{"null blockedHosts", map[string]any{"policy": "allowlist", "allowedHosts": []string{}, "blockedHosts": nil, "expectedRevision": 1}},
		{"wrong-type blockedHosts", map[string]any{"policy": "allowlist", "allowedHosts": []string{}, "blockedHosts": "evil.com", "expectedRevision": 1}},
		{"null allowedHosts", map[string]any{"policy": "allowlist", "allowedHosts": nil, "expectedRevision": 1}},
		{"invalid host pattern", map[string]any{"policy": "allowlist", "allowedHosts": []string{}, "blockedHosts": []string{"https://evil.com/path"}, "expectedRevision": 1}},
		{"too many blocked hosts", map[string]any{"policy": "allowlist", "allowedHosts": []string{}, "blockedHosts": tooMany, "expectedRevision": 1}},
		{"identical normalized overlap", map[string]any{"policy": "allowlist", "allowedHosts": []string{"API.Example.com"}, "blockedHosts": []string{"api.example.com"}, "expectedRevision": 1}},
	}
	for _, tc := range validationCases {
		r := h.request(http.MethodPatch, atlasEgress, tc.body, owner.AccessToken, nil, true)
		if r.status != http.StatusBadRequest {
			t.Fatalf("validation %s: got %d (%s), want 400", tc.name, r.status, r.body)
		}
	}

	// Broader allow (*.example.com) + narrower block (api.example.com) is valid, and normalizes duplicates.
	r := h.request(http.MethodPatch, atlasEgress, map[string]any{
		"policy":           "allowlist",
		"allowedHosts":     []string{"*.Example.com", "*.example.com"},
		"blockedHosts":     []string{"API.Example.com", "telemetry.example.net"},
		"expectedRevision": 1,
	}, owner.AccessToken, nil, true)
	if r.status != http.StatusOK {
		t.Fatalf("broader overlap update = %d %s", r.status, r.body)
	}
	var resp apigen.EgressPolicyResponse
	r.decode(t, &resp)
	if resp.Revision != 2 || len(resp.AllowedHosts) != 1 || len(resp.BlockedHosts) != 2 {
		t.Fatalf("unexpected response: %+v", resp)
	}

	// Omitted blockedHosts preserves existing blocks!
	r = h.request(http.MethodPatch, atlasEgress, map[string]any{
		"policy":           "open",
		"allowedHosts":     []string{"*.example.com"},
		"expectedRevision": 2,
	}, owner.AccessToken, nil, true)
	if r.status != http.StatusOK {
		t.Fatalf("omitted blockedHosts update = %d %s", r.status, r.body)
	}
	r.decode(t, &resp)
	if resp.Revision != 3 || resp.Policy != apigen.Open || len(resp.BlockedHosts) != 2 {
		t.Fatalf("omitted blockedHosts did not preserve blocks: %+v", resp)
	}

	// Explicit [] clears blockedHosts!
	r = h.request(http.MethodPatch, atlasEgress, map[string]any{
		"policy":           "open",
		"allowedHosts":     []string{"*.example.com"},
		"blockedHosts":     []string{},
		"expectedRevision": 3,
	}, owner.AccessToken, nil, true)
	if r.status != http.StatusOK {
		t.Fatalf("clear blockedHosts update = %d %s", r.status, r.body)
	}
	r.decode(t, &resp)
	if resp.Revision != 4 || len(resp.BlockedHosts) != 0 {
		t.Fatalf("explicit [] did not clear blocks: %+v", resp)
	}

	// Verify audit logs include actor, counts, and revision, with zero secret values.
	auditRows, err := h.queries.ListAuditLogsByWorkspace(context.Background(), db.ListAuditLogsByWorkspaceParams{
		WorkspaceID: pgUUID(wsID),
		Limit:       50,
	})
	if err != nil {
		t.Fatalf("ListAuditLogsByWorkspace: %v", err)
	}
	foundEgressAudit := false
	for _, row := range auditRows {
		if strings.Contains(string(row.Metadata), secretVal) {
			t.Fatal("audit log leaked secret value")
		}
		if row.Action == "bot.egress_updated" && row.TargetID == pgUUID(ownerBot.Id) {
			var meta map[string]any
			if err := json.Unmarshal(row.Metadata, &meta); err == nil && meta["revision"] == float64(4) {
				if meta["blocked_count"] == float64(0) && meta["allowed_count"] == float64(1) && row.ActorUserID.Valid {
					foundEgressAudit = true
				}
			}
		}
	}
	if !foundEgressAudit {
		t.Fatal("expected bot.egress_updated audit row with revision, counts, and actor")
	}
}

func TestEgressAppliedPendingAndNotRunningEnforcement(t *testing.T) {
	t.Parallel()

	var ackMu sync.Mutex
	ackMode := "applied"
	queue := &egressAckFakeQueue{
		ackFn: func(_ context.Context, _, _ uuid.UUID, rev int64) (int64, error) {
			ackMu.Lock()
			m := ackMode
			ackMu.Unlock()
			if m == "pending" {
				return 0, errors.New("proxy ack timeout")
			}
			return rev, nil
		},
	}

	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Alex", "Acme", "acme")
	wsID := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + wsID.String()
	bot := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas-seed")
	egressURL := base + "/bots/" + bot.Id.String() + "/egress"

	// 1. Stopped/unstarted bot -> 200 not_running.
	r := h.request(http.MethodPatch, egressURL, map[string]any{
		"policy":           "allowlist",
		"allowedHosts":     []string{"docs.example.com"},
		"blockedHosts":     []string{},
		"expectedRevision": 0,
	}, owner.AccessToken, nil, true)
	if r.status != http.StatusOK {
		t.Fatalf("not_running status=%d %s", r.status, r.body)
	}
	var resp apigen.EgressPolicyResponse
	r.decode(t, &resp)
	if resp.EnforcementStatus != apigen.EgressEnforcementStatusNotRunning {
		t.Fatalf("expected not_running, got %s", resp.EnforcementStatus)
	}

	// Mark environment running.
	if _, err := h.queries.UpsertEnvironment(context.Background(), db.UpsertEnvironmentParams{
		BotID:     pgUUID(bot.Id),
		Driver:    "docker",
		EnvID:     "env-1",
		Status:    "running",
		Image:     "botbox/sandbox:dev",
		CpuMillis: 2000,
		MemoryMib: 2048,
	}); err != nil {
		t.Fatalf("UpsertEnvironment: %v", err)
	}

	// 2. Running bot with ack -> 200 applied.
	r = h.request(http.MethodPatch, egressURL, map[string]any{
		"policy":           "allowlist",
		"allowedHosts":     []string{"docs.example.com"},
		"blockedHosts":     []string{"evil.example.com"},
		"expectedRevision": 1,
	}, owner.AccessToken, nil, true)
	if r.status != http.StatusOK {
		t.Fatalf("applied status=%d %s", r.status, r.body)
	}
	r.decode(t, &resp)
	if resp.EnforcementStatus != apigen.EgressEnforcementStatusApplied || resp.AcknowledgedRevision != 2 {
		t.Fatalf("expected applied rev 2, got %+v", resp)
	}

	// 3. Running bot without timely proxy ack -> 202 pending, intent kept in DB!
	ackMu.Lock()
	ackMode = "pending"
	ackMu.Unlock()
	r = h.request(http.MethodPatch, egressURL, map[string]any{
		"policy":           "allowlist",
		"allowedHosts":     []string{"docs.example.com"},
		"blockedHosts":     []string{"evil.example.com", "ads.example.com"},
		"expectedRevision": 2,
	}, owner.AccessToken, nil, true)
	if r.status != http.StatusAccepted {
		t.Fatalf("pending status=%d %s, want 202", r.status, r.body)
	}
	r.decode(t, &resp)
	if resp.EnforcementStatus != apigen.EgressEnforcementStatusPending || resp.Revision != 3 || resp.AcknowledgedRevision != 2 {
		t.Fatalf("expected pending rev 3 ack 2, got %+v", resp)
	}

	// GET also reports pending until proxy acknowledges rev 3.
	getR := h.request(http.MethodGet, egressURL, nil, owner.AccessToken, nil, false)
	getR.decode(t, &resp)
	if resp.EnforcementStatus != apigen.EgressEnforcementStatusPending {
		t.Fatalf("GET expected pending, got %+v", resp)
	}
}

func TestEgressApprovalsBlocklistPermissionsAndRaces(t *testing.T) {
	t.Parallel()

	queue := &egressAckFakeQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue, chatLimit: 20})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()

	atlas := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas-seed")
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member@example.com", "member", "Member", "CorrectHorse123!")

	// Owner explicitly blocks exact.blocked.example and wildcard *.wild.example under open mode!
	if r := h.request(http.MethodPatch, base+"/bots/"+atlas.Id.String()+"/egress", map[string]any{
		"policy":           "open",
		"allowedHosts":     []string{},
		"blockedHosts":     []string{"exact.blocked.example", "*.wild.example"},
		"expectedRevision": 0,
	}, owner.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("seed policy=%d %s", r.status, r.body)
	}

	group := askGroup(t, h, base, member.AccessToken, "Hosts", atlas.Id, false, 20, []any{map[string]any{"kind": "new_host"}}, []any{groupBot(atlas.Id, "lead")})
	started := make(chan struct{})
	release := make(chan struct{})
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Enqueue: queue.Enqueue, Execute: func(_ context.Context, _ engine.Session, emit func(engine.Event) error) error {
		close(started)
		<-release
		return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "done"})
	}}
	postGroup(t, h, base, member.AccessToken, group.Id, "fetch external hosts")
	errCh := make(chan error, 1)
	job := queue.jobs[0]
	queue.jobs = queue.jobs[1:]
	go func() { errCh <- worker.Process(context.Background(), job) }()
	<-started

	client, upstream := botProxyClient(t, h, ws, atlas.Id)
	defer upstream.Close()
	mustFailGet(t, client, "https://exact.blocked.example/a")
	mustFailGet(t, client, "https://sub.wild.example/b")
	close(release)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}

	pending := listApprovals(t, h, base, member.AccessToken, "pending")
	exactCard := findApproval(t, pending, apigen.ApprovalKindNewHost, "exact.blocked.example")
	wildCard := findApproval(t, pending, apigen.ApprovalKindNewHost, "sub.wild.example")

	// 1. Group member without bot-management rights cannot remove manager's exact block (403, stays pending).
	if r := h.request(http.MethodPost, base+"/approvals/"+exactCard.Id.String()+"/decision", map[string]any{"decision": "approved"}, member.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("non-manager unblock exact block = %d, want 403 (%s)", r.status, r.body)
	}
	_ = findApproval(t, listApprovals(t, h, base, member.AccessToken, "pending"), apigen.ApprovalKindNewHost, "exact.blocked.example")

	// 2. Wildcard block cannot be approved even by workspace owner (409 approval.wildcard_blocked, stays pending).
	if r := h.request(http.MethodPost, base+"/approvals/"+wildCard.Id.String()+"/decision", map[string]any{"decision": "approved"}, owner.AccessToken, nil, true); r.status != http.StatusConflict || r.errorCode(t) != "approval.wildcard_blocked" {
		t.Fatalf("owner approve wildcard-blocked = %d %s, want 409 approval.wildcard_blocked", r.status, r.body)
	}
	_ = findApproval(t, listApprovals(t, h, base, member.AccessToken, "pending"), apigen.ApprovalKindNewHost, "sub.wild.example")

	// 3. Authorized owner unblocks exact.blocked.example in open mode -> 204, revision advances to 2!
	if r := h.request(http.MethodPost, base+"/approvals/"+exactCard.Id.String()+"/decision", map[string]any{"decision": "approved"}, owner.AccessToken, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("owner approve exact block = %d %s", r.status, r.body)
	}
	getAfter := h.request(http.MethodGet, base+"/bots/"+atlas.Id.String()+"/egress", nil, owner.AccessToken, nil, false)
	var afterResp apigen.EgressPolicyResponse
	getAfter.decode(t, &afterResp)
	if afterResp.Revision != 2 || len(afterResp.BlockedHosts) != 1 || afterResp.BlockedHosts[0] != "*.wild.example" {
		t.Fatalf("unexpected policy after authorized unblock: %+v", afterResp)
	}

	// 4. Race between PATCH and an approval decision: serialized cleanly on shared revision.
	raceBot := createPiBot(t, h, base, owner.AccessToken, "Courier", "courier-seed")
	if r := h.request(http.MethodPatch, base+"/bots/"+raceBot.Id.String()+"/egress", map[string]any{
		"policy":           "allowlist",
		"allowedHosts":     []string{"existing.example"},
		"blockedHosts":     []string{"*.wild.example"},
		"expectedRevision": 0,
	}, owner.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("seed raceBot allowlist = %d %s", r.status, r.body)
	}

	// Open a fresh pending new_host card for concurrent.example while a turn runs.
	group2 := askGroup(t, h, base, member.AccessToken, "HostsRace", raceBot.Id, false, 20, []any{map[string]any{"kind": "new_host"}}, []any{groupBot(raceBot.Id, "lead")})
	started2 := make(chan struct{})
	release2 := make(chan struct{})
	worker2 := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Enqueue: queue.Enqueue, Execute: func(_ context.Context, _ engine.Session, emit func(engine.Event) error) error {
		close(started2)
		<-release2
		return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "done"})
	}}
	queue.jobs = nil
	postGroup(t, h, base, member.AccessToken, group2.Id, "check concurrent host")
	job2 := queue.jobs[0]
	queue.jobs = queue.jobs[1:]
	errCh2 := make(chan error, 1)
	go func() { errCh2 <- worker2.Process(context.Background(), job2) }()
	<-started2
	raceClient, raceUpstream := botProxyClient(t, h, ws, raceBot.Id)
	defer raceUpstream.Close()
	mustFailGet(t, raceClient, "https://concurrent.example/ping")
	close(release2)
	if err := <-errCh2; err != nil {
		t.Fatal(err)
	}
	raceCard := findApproval(t, listApprovals(t, h, base, member.AccessToken, "pending"), apigen.ApprovalKindNewHost, "concurrent.example")

	// Race approval decision against PATCH on revision 1: shared lock serializes both writers;
	// if approval wins first, PATCH gets 409; if PATCH wins first, approval reads rev 2 and adds concurrent.example on rev 3!
	var wg sync.WaitGroup
	gate := make(chan struct{})
	var patchStatus, approveStatus int
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-gate
		r := h.request(http.MethodPatch, base+"/bots/"+raceBot.Id.String()+"/egress", map[string]any{
			"policy":           "allowlist",
			"allowedHosts":     []string{"existing.example", "manual.example"},
			"expectedRevision": 1,
		}, owner.AccessToken, nil, true)
		patchStatus = r.status
	}()
	go func() {
		defer wg.Done()
		<-gate
		r := h.request(http.MethodPost, base+"/approvals/"+raceCard.Id.String()+"/decision", map[string]any{"decision": "approved"}, member.AccessToken, nil, true)
		approveStatus = r.status
	}()
	close(gate)
	wg.Wait()

	if approveStatus != http.StatusNoContent {
		t.Fatalf("expected approval 204, got %d (patch=%d)", approveStatus, patchStatus)
	}
	if patchStatus != http.StatusOK && patchStatus != http.StatusConflict {
		t.Fatalf("expected PATCH 200 or 409, got %d", patchStatus)
	}
	getFinal := h.request(http.MethodGet, base+"/bots/"+raceBot.Id.String()+"/egress", nil, owner.AccessToken, nil, false)
	var finalResp apigen.EgressPolicyResponse
	getFinal.decode(t, &finalResp)
	if !containsAll(finalResp.AllowedHosts, "existing.example", "concurrent.example") {
		t.Fatalf("race lost approved host: %+v", finalResp)
	}
	if patchStatus == http.StatusOK && (!containsAll(finalResp.AllowedHosts, "manual.example") || finalResp.Revision != 3) {
		t.Fatalf("PATCH won first so both hosts and rev 3 expected, got %+v", finalResp)
	}
	if patchStatus == http.StatusConflict && finalResp.Revision != 2 {
		t.Fatalf("approval won first so rev 2 expected, got %+v", finalResp)
	}
	_ = time.Second
}
