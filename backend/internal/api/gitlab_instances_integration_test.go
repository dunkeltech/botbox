// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/authbroker"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/google/uuid"
)

func TestSelfHostedGitLabRegistrationAndLogin(t *testing.T) {
	ctx := context.Background()
	eng := authbroker.NewMemoryBrowserEngine()
	svc := authbroker.NewService(eng)
	handler, err := authbroker.NewHTTPHandler(svc, "selfhosted-broker-token-1234")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()
	client, err := authbroker.NewHTTPClient(srv.URL, "selfhosted-broker-token-1234")
	if err != nil {
		t.Fatal(err)
	}
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{authBroker: client, turnQueue: queue, chatLimit: 500})
	svc.SetNow(h.clock.Now)
	svc.SetGitLabPolicy(func(ctx context.Context, ws, bot, session uuid.UUID, origin string) error {
		return authbroker.ValidateGitLabAccess(ctx, db.New(h.pool), ws, bot, session, origin)
	})
	owner, _ := h.completeSetup("owner-selfhost@example.com", "CorrectHorse123!", "Owner", "Workspace", "selfhost")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	path := base + "/gitlab-instances"
	admin, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "admin-selfhost@example.com", "admin", "Admin", "CorrectHorse123!")
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member-selfhost@example.com", "member", "Member", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "viewer-selfhost@example.com", "viewer", "Viewer", "CorrectHorse123!")
	input := map[string]any{"name": "Company GitLab", "origin": "https://gitlab.example.org"}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		if r := h.request(method, path, input, "", nil, true); r.status != 401 {
			t.Fatalf("unauth %s: %d %s", method, r.status, r.body)
		}
	}
	for _, token := range []string{member.AccessToken, viewer.AccessToken} {
		for _, method := range []string{http.MethodPost, http.MethodDelete} {
			if r := h.request(method, path, input, token, nil, true); r.status != 403 {
				t.Fatalf("forbidden %s: %d", method, r.status)
			}
		}
	}
	invalid := []any{map[string]any{}, map[string]any{"name": 1, "origin": true}, map[string]any{"name": "", "origin": "https://gitlab.example.org"}, map[string]any{"name": strings.Repeat("x", 101), "origin": "https://gitlab.example.org"}}
	for _, origin := range []string{"", "http://gitlab.example.org", "https://gitlab.example.org:8443", "https://127.0.0.1", "https://gitlab.example.org/path", "https://gitlab.com", "https://user:password@gitlab.example.org"} {
		invalid = append(invalid, map[string]any{"name": "GitLab", "origin": origin})
	}
	for _, body := range invalid {
		if r := h.request(http.MethodPost, path, body, owner.AccessToken, nil, true); r.status != 400 {
			t.Fatalf("invalid input accepted: %d %s", r.status, r.body)
		}
	}
	if r := h.request(http.MethodPost, path, input, admin.AccessToken, nil, true); r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, path, input, owner.AccessToken, nil, true); r.status != 409 {
		t.Fatalf("duplicate: %d", r.status)
	}
	for _, token := range []string{owner.AccessToken, admin.AccessToken, member.AccessToken, viewer.AccessToken} {
		r := h.request(http.MethodGet, path, nil, token, nil, false)
		var list apigen.GitLabInstanceList
		r.decode(t, &list)
		if r.status != 200 || len(list.Instances) != 1 || list.Instances[0].Origin != "https://gitlab.example.org:443" {
			t.Fatalf("list: %d %s", r.status, r.body)
		}
	}
	otherResp := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other", "slug": "other-selfhost"}, owner.AccessToken, nil, true)
	var otherWS apigen.WorkspaceSummary
	otherResp.decode(t, &otherWS)
	eve, _ := h.inviteAndAcceptNewUser(owner.AccessToken, otherWS.Id, "eve-selfhost@example.com", "admin", "Eve", "CorrectHorse123!")
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		if r := h.request(method, path, input, eve.AccessToken, nil, true); r.status != 404 {
			t.Fatalf("cross workspace %s: %d", method, r.status)
		}
	}
	atlas := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas-selfhost")
	// Registration alone never overrides the bot egress policy.
	open := authbroker.OpenSessionInput{WorkspaceID: ws, BotID: atlas.Id, AdapterID: "gitlab_web", TargetOrigin: "https://gitlab.example.org"}
	if _, err := client.OpenSession(ctx, open); !errors.Is(err, authbroker.ErrNavigationForbidden) {
		t.Fatalf("egress bypass: %v", err)
	}
	if r := h.request(http.MethodPatch, base+"/bots/"+atlas.Id.String()+"/egress", map[string]any{"policy": "allowlist", "allowedHosts": []string{"gitlab.example.org"}, "expectedRevision": 0}, owner.AccessToken, nil, true); r.status != 200 {
		t.Fatalf("egress: %d %s", r.status, r.body)
	}
	// Network access and registration alone do not assign a GitLab instance to a bot.
	if _, err := client.OpenSession(ctx, open); !errors.Is(err, authbroker.ErrNavigationForbidden) {
		t.Fatalf("missing bot assignment accepted: %v", err)
	}
	assignments := base + "/bots/" + atlas.Id.String() + "/gitlab-instances"
	assignmentBody := map[string]any{"origins": []string{"https://gitlab.example.org"}}
	for _, tt := range []struct {
		token  string
		status int
	}{{"", 401}, {viewer.AccessToken, 403}, {member.AccessToken, 403}, {admin.AccessToken, 200}, {owner.AccessToken, 200}, {eve.AccessToken, 404}} {
		if r := h.request(http.MethodPut, assignments, assignmentBody, tt.token, nil, true); r.status != tt.status {
			t.Fatalf("assignment RBAC: %d want %d %s", r.status, tt.status, r.body)
		}
	}
	for _, body := range []any{map[string]any{}, map[string]any{"origins": nil}, map[string]any{"origins": "bad"}, map[string]any{"origins": []string{"https://missing.example.org"}}, map[string]any{"origins": []string{"https://gitlab.example.org", "https://gitlab.example.org:443"}}, map[string]any{"origins": []string{"http://gitlab.example.org"}}} {
		if r := h.request(http.MethodPut, assignments, body, owner.AccessToken, nil, true); r.status != 400 {
			t.Fatalf("invalid assignment: %d %s", r.status, r.body)
		}
	}
	otherBot := createPiBot(t, h, base, member.AccessToken, "Mia", "mia-selfhost")
	if r := h.request(http.MethodPost, path, map[string]any{"name": "Second GitLab", "origin": "https://second.example.org"}, owner.AccessToken, nil, true); r.status != 201 {
		t.Fatalf("second instance: %d", r.status)
	}
	if r := h.request(http.MethodPut, base+"/bots/"+otherBot.Id.String()+"/gitlab-instances", map[string]any{"origins": []string{"https://second.example.org"}}, member.AccessToken, nil, true); r.status != 200 {
		t.Fatalf("bot owner assignment: %d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPatch, base+"/bots/"+otherBot.Id.String()+"/egress", map[string]any{"policy": "allowlist", "allowedHosts": []string{"*.example.org"}, "expectedRevision": 0}, member.AccessToken, nil, true); r.status != 200 {
		t.Fatalf("second bot egress: %d", r.status)
	}
	for _, tt := range []struct {
		bot     uuid.UUID
		origin  string
		allowed bool
	}{{atlas.Id, "https://gitlab.example.org:443", true}, {atlas.Id, "https://second.example.org:443", false}, {otherBot.Id, "https://gitlab.example.org:443", false}, {otherBot.Id, "https://second.example.org:443", true}} {
		err := authbroker.ValidateGitLabAccess(ctx, h.queries, ws, tt.bot, uuid.Nil, tt.origin)
		if (err == nil) != tt.allowed {
			t.Fatalf("bot isolation %s %s: %v", tt.bot, tt.origin, err)
		}
	}

	for _, step := range []struct {
		revision int
		blocked  []string
		allowed  bool
	}{
		{1, []string{"gitlab.example.org"}, false}, {2, []string{}, true},
	} {
		body := map[string]any{"policy": "allowlist", "allowedHosts": []string{"*.example.org"}, "blockedHosts": step.blocked, "expectedRevision": step.revision}
		if r := h.request(http.MethodPatch, base+"/bots/"+atlas.Id.String()+"/egress", body, owner.AccessToken, nil, true); r.status != 200 {
			t.Fatalf("block policy: %d %s", r.status, r.body)
		}
		err := authbroker.ValidateGitLabAccess(ctx, h.queries, ws, atlas.Id, uuid.Nil, "https://gitlab.example.org:443")
		if (err == nil) != step.allowed {
			t.Fatalf("block override allowed=%v err=%v", step.allowed, err)
		}
	}

	grp := askGroup(t, h, base, owner.AccessToken, "GitLabOps", atlas.Id, false, 20, []any{}, []any{groupUser(member.User.Id, "operator"), groupBot(atlas.Id, "lead")})
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, AuthBroker: client, Enqueue: queue.Enqueue, Execute: func(_ context.Context, s engine.Session, emit func(engine.Event) error) error {
		if s.ContinuationPrompt != "" {
			return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "Continued"})
		}
		return emit(engine.Event{Kind: engine.EventKindToolCall, ToolID: "login", ToolName: "request_secret", ToolInput: []byte(`{"kind":"browser_login","adapter_id":"gitlab_web","target_origin":"https://gitlab.example.org","reason":"Review project"}`)})
	}}
	requestLogin := func() apigen.SecretRequestSummary {
		t.Helper()
		postGroup(t, h, base, owner.AccessToken, grp.Id, "@Atlas log in")
		drainTurns(t, worker, queue, 2)
		r := h.request(http.MethodGet, base+"/groups/"+grp.Id.String()+"/messages", nil, owner.AccessToken, nil, false)
		var hist apigen.ChatHistory
		r.decode(t, &hist)
		for i := len(hist.Messages) - 1; i >= 0; i-- {
			if sr := hist.Messages[i].SecretRequest; sr != nil && sr.Status == apigen.SecretRequestStatusPending {
				return *sr
			}
		}
		t.Fatalf("no pending request: %s", r.body)
		return apigen.SecretRequestSummary{}
	}
	sr := requestLogin()
	if sr.TargetOrigin != "https://gitlab.example.org:443" || sr.BrokerSessionId == nil {
		t.Fatalf("wrong request: %+v", sr)
	}
	const password = "self-hosted-password-never-in-context"
	r := h.request(http.MethodPost, base+"/secret-requests/"+sr.SecretRequestId.String()+"/fulfill", map[string]any{"username": "alice", "secretValue": password, "scope": "ephemeral"}, member.AccessToken, nil, true)
	if r.status != 200 || strings.Contains(string(r.body), password) {
		t.Fatalf("fulfill: %d %s", r.status, r.body)
	}
	read, err := client.BotRead(ctx, ws, atlas.Id, *sr.BrokerSessionId, "body")
	if err != nil || strings.Contains(read.Text, password) {
		t.Fatalf("read: %v %q", err, read.Text)
	}
	if _, err := client.BotNavigate(ctx, ws, atlas.Id, *sr.BrokerSessionId, "https://gitlab.com/"); !errors.Is(err, authbroker.ErrNavigationForbidden) {
		t.Fatalf("origin escape: %v", err)
	}
	drainTurns(t, worker, queue, 2)
	pending := requestLogin()
	if r := h.request(http.MethodPut, assignments, map[string]any{"origins": []string{}}, owner.AccessToken, nil, true); r.status != 200 {
		t.Fatalf("unassign: %d %s", r.status, r.body)
	}
	if err := authbroker.ValidateGitLabAccess(ctx, h.queries, ws, otherBot.Id, uuid.Nil, "https://second.example.org:443"); err != nil {
		t.Fatalf("other bot lost access: %v", err)
	}

	if r := h.request(http.MethodDelete, path, map[string]any{"origin": "https://gitlab.example.org"}, owner.AccessToken, nil, true); r.status != 204 {
		t.Fatalf("delete: %d %s", r.status, r.body)
	}
	if _, err := client.BotRead(ctx, ws, atlas.Id, *sr.BrokerSessionId, "body"); err == nil {
		t.Fatal("revoked session usable")
	}
	r = h.request(http.MethodPost, base+"/secret-requests/"+pending.SecretRequestId.String()+"/fulfill", map[string]any{"username": "alice", "secretValue": password, "scope": "ephemeral"}, member.AccessToken, nil, true)
	if r.status != 409 {
		t.Fatalf("removed pending request: %d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, path, input, owner.AccessToken, nil, true); r.status != 201 {
		t.Fatalf("re-add: %d %s", r.status, r.body)
	}
	if _, err := client.GetSession(ctx, ws, *sr.BrokerSessionId); err == nil {
		t.Fatal("old session resurrected")
	}
	if strings.Contains(h.logs.String(), password) {
		t.Fatal("password leaked in logs")
	}
	// Registry limits and failed policy dependencies never expand access.
	for i := 0; i < 18; i++ {
		body := map[string]any{"name": "Extra", "origin": fmt.Sprintf("https://git%d.example.org", i)}
		if r := h.request(http.MethodPost, path, body, owner.AccessToken, nil, true); r.status != 201 {
			t.Fatalf("quota setup: %d %s", r.status, r.body)
		}
	}
	if r := h.request(http.MethodPost, path, map[string]any{"name": "Over limit", "origin": "https://extra.example.org"}, owner.AccessToken, nil, true); r.status != 409 {
		t.Fatalf("quota: %d", r.status)
	}
	if r := h.request(http.MethodDelete, path, map[string]any{"origin": "https://missing.example.org"}, admin.AccessToken, nil, true); r.status != 404 {
		t.Fatalf("missing delete: %d", r.status)
	}
	if r := h.request(http.MethodPut, assignments, assignmentBody, owner.AccessToken, nil, true); r.status != 200 {
		t.Fatalf("reassign: %d %s", r.status, r.body)
	}

	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := authbroker.ValidateGitLabAccess(cancelledCtx, h.queries, ws, atlas.Id, uuid.Nil, "https://gitlab.example.org:443"); !errors.Is(err, authbroker.ErrNavigationForbidden) {
		t.Fatalf("cancelled policy: %v", err)
	}
	h.pool.Close()
	if err := authbroker.ValidateGitLabAccess(ctx, h.queries, ws, atlas.Id, uuid.Nil, "https://gitlab.example.org:443"); !errors.Is(err, authbroker.ErrNavigationForbidden) {
		t.Fatalf("database outage: %v", err)
	}

}
