// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/api"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"github.com/google/uuid"
)

type gitLabPublicResolver struct{}

func (gitLabPublicResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
}

func TestSelfHostedGitLabCLIGrantsAndRevocation(t *testing.T) {
	ctx := context.Background()
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue, chatLimit: 500})
	owner, _ := h.completeSetup("glab-owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "gitlab-cli")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	atlas := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas-glab")
	mia := createPiBot(t, h, base, owner.AccessToken, "Mia", "mia-glab")
	origin := "https://gitlab.example.org"
	registry := base + "/gitlab-instances"
	must := func(method, path string, body any, status int) {
		t.Helper()
		r := h.request(method, path, body, owner.AccessToken, nil, true)
		if r.status != status {
			t.Fatalf("%s %s: status %d want %d: %s", method, path, r.status, status, r.body)
		}
	}
	must(http.MethodPost, registry, map[string]any{"name": "Company GitLab", "origin": origin}, 201)
	for _, bot := range []uuid.UUID{atlas.Id, mia.Id} {
		must(http.MethodPatch, base+"/bots/"+bot.String()+"/egress", map[string]any{"policy": "allowlist", "allowedHosts": []string{"gitlab.example.org"}, "expectedRevision": 0}, 200)
	}
	assignments := func(bot uuid.UUID, origins []string) {
		t.Helper()
		must(http.MethodPut, base+"/bots/"+bot.String()+"/gitlab-instances", map[string]any{"origins": origins}, 200)
	}
	group := askGroup(t, h, base, owner.AccessToken, "GitLab CLI", atlas.Id, false, 20, []any{}, []any{groupBot(atlas.Id, "lead")})
	adapterID := "gitlab_api"
	var tryAgain bool
	var savedCredentialPrompt string
	worker := &engine.Worker{Pool: h.pool, Now: h.clock.Now, Enqueue: queue.Enqueue, Execute: func(_ context.Context, s engine.Session, emit func(engine.Event) error) error {
		if s.ContinuationPrompt != "" {
			if strings.Contains(s.ContinuationPrompt, "saved GitLab credential") {
				savedCredentialPrompt = s.ContinuationPrompt
			}
			if !tryAgain {
				return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "Continue with the credential placeholder"})
			}
		}
		input, _ := json.Marshal(map[string]string{"kind": "cli_egress", "adapter_id": adapterID, "target_origin": origin, "reason": "Use GitLab CLI"})
		return emit(engine.Event{Kind: engine.EventKindToolCall, ToolID: "gitlab-credential", ToolName: "request_secret", ToolInput: input})
	}}
	history := func() apigen.ChatHistory {
		t.Helper()
		r := h.request(http.MethodGet, base+"/groups/"+group.Id.String()+"/messages", nil, owner.AccessToken, nil, false)
		var result apigen.ChatHistory
		r.decode(t, &result)
		return result
	}
	start := func() {
		t.Helper()
		postGroup(t, h, base, owner.AccessToken, group.Id, "@Atlas configure GitLab")
		drainTurns(t, worker, queue, 2)
	}
	start()
	turns, err := h.queries.ListConversationTurns(ctx, db.ListConversationTurnsParams{WorkspaceID: pgUUID(ws), ConversationID: pgUUID(group.Id)})
	if err != nil || len(turns) == 0 || turns[0].ErrorCode != "secret_request.gitlab_not_assigned" {
		t.Fatalf("unassigned bot received generic failure: %v %+v", err, turns)
	}
	assignments(atlas.Id, []string{origin})
	assignments(mia.Id, []string{origin})
	const realToken = "fixture-real-gitlab-token-never-in-sandbox"
	request := func(scope string, replace bool) apigen.SecretRequestSummary {
		t.Helper()
		start()
		var pending *apigen.SecretRequestSummary
		for _, message := range history().Messages {
			if sr := message.SecretRequest; sr != nil && string(sr.Status) == "pending" {
				copy := *sr
				pending = &copy
			}
		}
		if pending == nil {
			t.Fatal("no pending GitLab credential request")
		}
		if pending.TargetOrigin != origin+":443" || pending.PlaceholderValue == nil || !strings.HasPrefix(*pending.PlaceholderValue, "botbox-placeholder-") {
			t.Fatalf("wrong origin or missing default placeholder: %+v", pending)
		}
		must(http.MethodPost, base+"/secret-requests/"+pending.SecretRequestId.String()+"/fulfill", map[string]any{"secretValue": realToken, "scope": scope, "replaceExisting": replace}, 200)
		return *pending
	}
	ca, err := mitmproxy.NewBotCA("gitlab-upstream")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ca.LeafCertificate("gitlab.example.org")
	if err != nil {
		t.Fatal(err)
	}
	var authenticated atomic.Int64
	var wrongCredential atomic.Bool
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/users/sign_in" {
			_, _ = io.WriteString(w, "Public GitLab sign-in")
			return
		}
		if strings.HasSuffix(r.URL.Path, "/info/refs") {
			username, password, ok := r.BasicAuth()
			if !ok || username != "oauth2" || password != realToken || r.Header.Get("PRIVATE-TOKEN") != "" {
				wrongCredential.Store(true)
				w.WriteHeader(401)
				return
			}
			authenticated.Add(1)
			w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
			_, _ = io.WriteString(w, "001e# service=git-upload-pack\n00000000")
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/v4/") {
			t.Error("credentials escaped reviewed API paths")
			w.WriteHeader(403)
			return
		}
		if r.Header.Get("PRIVATE-TOKEN") != realToken || r.Header.Get("Authorization") != "" {
			wrongCredential.Store(true)
			w.WriteHeader(401)
			return
		}
		authenticated.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v4/echo" {
			_ = json.NewEncoder(w).Encode(map[string]string{"echo": realToken})
			return
		}
		_, _ = io.WriteString(w, `{"id":7,"username":"fixture-user","name":"Fixture User"}`)
	}))
	upstream.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*cert}}
	upstream.StartTLS()
	defer upstream.Close()
	makeProxy := func(bot uuid.UUID) (*http.Client, *mitmproxy.Handler, string, []byte) {
		t.Helper()
		botCA, err := mitmproxy.NewBotCA(bot.String())
		if err != nil {
			t.Fatal(err)
		}
		handler, err := api.NewBotEgressProxyHandler(api.BotEgressProxyDeps{Pool: h.pool, KeyRing: h.keyRing, WorkspaceID: ws, BotID: bot, CA: botCA, UpstreamRootCAs: ca.CertPool(), Now: h.clock.Now, PublicResolver: gitLabPublicResolver{}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != "8.8.8.8:443" {
				t.Errorf("destination was not pinned to validated public IP: %s", address)
			}
			return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(upstream.URL, "https://"))
		}})
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		proxyURL, _ := url.Parse(server.URL)
		transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: botCA.CertPool()}}
		t.Cleanup(transport.CloseIdleConnections)
		return &http.Client{Transport: transport, Timeout: 5 * time.Second}, handler, server.URL, botCA.CertPEM()
	}
	client, proxy, proxyURL, rootPEM := makeProxy(atlas.Id)
	other, otherProxy, _, _ := makeProxy(mia.Id)
	call := func(c *http.Client, path, placeholder string, want bool) {
		t.Helper()
		before := authenticated.Load()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, origin+path, nil)
		req.Header.Set("PRIVATE-TOKEN", placeholder)
		resp, err := c.Do(req)
		if err != nil {
			if want {
				t.Fatal(err)
			}
			if authenticated.Load() != before {
				t.Fatal("revoked request reached upstream")
			}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if want && resp.StatusCode != 200 {
			t.Fatalf("API status %d: %s", resp.StatusCode, body)
		}
		if !want && (resp.StatusCode == 200 || authenticated.Load() != before) {
			t.Fatal("unauthorized credential use")
		}
		if strings.Contains(string(body), realToken) {
			t.Fatal("token echoed into client response")
		}
	}
	publicResponse, err := client.Get(origin + "/users/sign_in")
	if err != nil {
		t.Fatal(err)
	}
	_ = publicResponse.Body.Close()
	if publicResponse.StatusCode != 200 {
		t.Fatalf("public reachability probe was blocked: %d", publicResponse.StatusCode)
	}

	ephemeral := request("ephemeral", false)
	placeholder := *ephemeral.PlaceholderValue
	call(client, "/api/v4/user", placeholder, true)
	call(client, "/api/v4/echo", placeholder, true)
	call(client, "/api/v4evil/user", placeholder, false)
	call(other, "/api/v4/user", placeholder, false)
	// The real CLI receives only a dummy token and talks to a local fake GitLab over the proxy.
	glabPath, err := exec.LookPath("glab")
	if err != nil {
		t.Fatal("glab is required; run tests inside devenv shell")
	}
	home := t.TempDir()
	rootFile := filepath.Join(home, "root.pem")
	if err = os.WriteFile(rootFile, rootPEM, 0600); err != nil {
		t.Fatal(err)
	}
	// #nosec G204 -- glab is a declared devenv executable, invoked only against a local fake GitLab.
	command := exec.CommandContext(ctx, glabPath, "api", "user")
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + home, "GLAB_CONFIG_DIR=" + home, "GITLAB_HOST=gitlab.example.org", "GITLAB_API_HOST=gitlab.example.org", "GLAB_API_PROTOCOL=https", "GITLAB_TOKEN=" + placeholder, "HTTPS_PROXY=" + proxyURL, "HTTP_PROXY=" + proxyURL, "NO_PROXY=", "SSL_CERT_FILE=" + rootFile, "GLAB_CHECK_UPDATE=false"}
	// macOS Go clients use the system trust store; configure the fixture CA explicitly without disabling TLS.
	// #nosec G204 -- only the declared CLI and fixture-owned CA path are passed as arguments.
	configureCA := exec.CommandContext(ctx, glabPath, "config", "set", "ca_cert", rootFile, "--host", "gitlab.example.org")
	configureCA.Env = command.Env
	if output, err := configureCA.CombinedOutput(); err != nil {
		t.Fatalf("configure glab fixture CA: %v %s", err, output)
	}

	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("glab api user failed: %v %s", err, output)
	}
	if !strings.Contains(string(output), "fixture-user") || strings.Contains(string(output), realToken) {
		t.Fatalf("unexpected glab output: %s", output)
	}
	h.clock.Advance(16 * time.Minute)
	if err = proxy.RevalidatePassthrough(ctx); err != nil {
		t.Fatal(err)
	}
	call(client, "/api/v4/user", placeholder, false)
	login := h.request(http.MethodPost, "/api/auth/login", map[string]any{"email": "glab-owner@example.com", "password": "CorrectHorse123!"}, "", nil, true)
	if login.status != 200 {
		t.Fatalf("refresh test login: %d", login.status)
	}
	login.decode(t, &owner)

	drainTurns(t, worker, queue, 2)
	// Persistent bot grants are destroyed by unassignment and never reappear on reassignment.
	botGrant := request("bot_secret", false)
	call(client, "/api/v4/user", *botGrant.PlaceholderValue, true)
	drainTurns(t, worker, queue, 2)
	planning := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name: "Planning", lead: atlas.Id, turnLimit: 20, members: []any{groupBot(mia.Id, ""), groupBot(atlas.Id, "lead")},
	})
	postGroup(t, h, base, owner.AccessToken, planning.Id, "Check GitLab without an explicit mention")
	drainTurns(t, worker, queue, 2)
	planningHistory := groupHistory(t, h, base, owner.AccessToken, planning.Id)
	if !strings.Contains(savedCredentialPrompt, "GITLAB_TOKEN=botbox-placeholder-") ||
		!strings.Contains(savedCredentialPrompt, "GITLAB_HOST=gitlab.example.org") ||
		strings.Contains(savedCredentialPrompt, realToken) {
		t.Fatal("lead did not receive safe CLI guidance for the saved credential")
	}
	if len(planningHistory.Turns) != 1 || planningHistory.Turns[0].BotId == nil || *planningHistory.Turns[0].BotId != atlas.Id ||
		planningHistory.Turns[0].Status != "completed" || getGroup(t, h, base, owner.AccessToken, planning.Id).Paused {
		t.Fatal("expected the lead's original turn to complete without pausing the group")
	}
	for _, message := range planningHistory.Messages {
		if message.SecretRequest != nil || strings.Contains(message.Content, realToken) {
			t.Fatal("saved credential created a request or appeared in chat")
		}
	}

	// The REST credential does not satisfy Git HTTPS or grant access to another bot.
	for _, tc := range []struct{ adapter, name string }{{"gitlab_git", "Atlas"}, {"gitlab_api", "Mia"}} {
		adapterID = tc.adapter
		savedCredentialPrompt = ""
		postGroup(t, h, base, owner.AccessToken, planning.Id, "@"+tc.name+" check GitLab")
		drainTurns(t, worker, queue, 2)
		if savedCredentialPrompt != "" {
			t.Fatalf("unexpected saved credential for %s with %s", tc.name, tc.adapter)
		}
		messages := groupHistory(t, h, base, owner.AccessToken, planning.Id).Messages
		card := messages[len(messages)-1].SecretRequest
		if card == nil || card.Status != "pending" {
			t.Fatal("expected a human request when the scoped credential is missing")
		}
		must(http.MethodPost, base+"/secret-requests/"+card.SecretRequestId.String()+"/deny", nil, 200)
		drainTurns(t, worker, queue, 2)
	}
	// A failed probe may still ask the human to replace the saved credential.
	tryAgain = true
	start()
	tryAgain = false
	var replacement *apigen.SecretRequestSummary
	for _, message := range history().Messages {
		if card := message.SecretRequest; card != nil && card.Status == "pending" {
			replacement = card
			break
		}
	}
	if replacement == nil {
		t.Fatal("expected a human request after retrying the saved credential")
	}
	must(http.MethodPost, base+"/secret-requests/"+replacement.SecretRequestId.String()+"/deny", nil, 200)
	drainTurns(t, worker, queue, 2)
	assignments(atlas.Id, []string{})
	if err = proxy.RevalidatePassthrough(ctx); err != nil {
		t.Fatal(err)
	}
	assignments(atlas.Id, []string{origin})
	call(client, "/api/v4/user", placeholder, false)
	drainTurns(t, worker, queue, 2)
	// Workspace inheritance still requires assignment; per-bot revocation must survive re-adding it.
	workspaceGrant := request("workspace_secret", false)
	call(client, "/api/v4/user", *workspaceGrant.PlaceholderValue, true)
	call(other, "/api/v4/user", placeholder, true)
	assignments(atlas.Id, []string{})
	if err = proxy.RevalidatePassthrough(ctx); err != nil {
		t.Fatal(err)
	}
	assignments(atlas.Id, []string{origin})
	call(client, "/api/v4/user", placeholder, false)
	call(other, "/api/v4/user", placeholder, true)
	drainTurns(t, worker, queue, 2)
	fresh := request("workspace_secret", true)
	call(client, "/api/v4/user", *fresh.PlaceholderValue, true)
	drainTurns(t, worker, queue, 2)
	adapterID = "gitlab_git"
	gitGrant := request("bot_secret", false)
	call(client, "/team/repo.git/info/refs?service=git-upload-pack", *gitGrant.PlaceholderValue, true)
	// #nosec G204 -- git is declared in devenv and the repository URL points only to the fake upstream through the fixture proxy.
	gitCommand := exec.CommandContext(ctx, "git", "ls-remote", origin+"/team/repo.git")
	gitCommand.Env = append(append([]string{}, command.Env...), "GIT_SSL_CAINFO="+rootFile, "GIT_TERMINAL_PROMPT=0")
	if output, err := gitCommand.CombinedOutput(); err != nil {
		t.Fatalf("git ls-remote failed: %v %s", err, output)
	}

	must(http.MethodDelete, registry, map[string]any{"origin": origin}, 204)
	if err = proxy.RevalidatePassthrough(ctx); err != nil {
		t.Fatal(err)
	}
	if err = otherProxy.RevalidatePassthrough(ctx); err != nil {
		t.Fatal(err)
	}
	must(http.MethodPost, registry, map[string]any{"name": "Company GitLab", "origin": origin}, 201)
	assignments(atlas.Id, []string{origin})
	call(client, "/api/v4/user", placeholder, false)
	if wrongCredential.Load() {
		t.Fatal("upstream received a placeholder, wrong header or wrong credential")
	}
	for _, message := range history().Messages {
		if strings.Contains(message.Content, realToken) {
			t.Fatal("token leaked into chat")
		}
	}
	if strings.Contains(h.logs.String(), realToken) {
		t.Fatal("token leaked into logs")
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	_ = filepath.WalkDir(home, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			relative, relErr := filepath.Rel(home, path)
			if relErr != nil {
				return relErr
			}
			data, _ := root.ReadFile(relative)
			if strings.Contains(string(data), realToken) {
				t.Error("token persisted in glab files")
			}
		}
		return nil
	})
}

func TestLegacyWorkspaceSecretCannotBypassGitLabBoundary(t *testing.T) {
	ctx := context.Background()
	h := newTestHarness(t, nil, harnessOpts{})
	owner, _ := h.completeSetup("legacy-gitlab@example.com", "CorrectHorse123!", "Owner", "Workspace", "legacy-gitlab")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	bot := createPiBot(t, h, base, owner.AccessToken, "Atlas", "legacy-atlas")
	origin := "https://gitlab.example.org"
	must := func(method, path string, body any, status int) {
		t.Helper()
		r := h.request(method, path, body, owner.AccessToken, nil, true)
		if r.status != status {
			t.Fatalf("%s %s status %d: %s", method, path, r.status, r.body)
		}
	}
	// A legacy parent-domain secret used to imply both network access and credential injection.
	must(http.MethodPost, base+"/secrets", map[string]any{"name": "legacy-token", "domainMatch": "example.org", "matchSubdomains": true, "authScheme": "header", "headerName": "PRIVATE-TOKEN", "value": "fixture-legacy-workspace-token"}, 201)
	register := func() {
		t.Helper()
		must(http.MethodPost, base+"/gitlab-instances", map[string]any{"name": "GitLab", "origin": origin}, 201)
	}
	register()
	ca, err := mitmproxy.NewBotCA("legacy-boundary-upstream")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ca.LeafCertificate("gitlab.example.org")
	if err != nil {
		t.Fatal(err)
	}
	var reached atomic.Int64
	var credentialLeaked atomic.Bool
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		if r.Header.Get("PRIVATE-TOKEN") != "" {
			credentialLeaked.Store(true)
		}
		w.WriteHeader(200)
	}))
	upstream.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*cert}}
	upstream.StartTLS()
	defer upstream.Close()
	botCA, err := mitmproxy.NewBotCA("legacy-boundary-bot")
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := api.NewBotEgressProxyHandler(api.BotEgressProxyDeps{Pool: h.pool, KeyRing: h.keyRing, WorkspaceID: ws, BotID: bot.Id, CA: botCA, UpstreamRootCAs: ca.CertPool(), PublicResolver: gitLabPublicResolver{}, Now: h.clock.Now, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(upstream.URL, "https://"))
	}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(proxy)
	defer server.Close()
	proxyURL, _ := url.Parse(server.URL)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: botCA.CertPool()}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	denied := func() {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/api/v4/user", nil)
		req.Header.Set("PRIVATE-TOKEN", "botbox-placeholder-legacy")
		response, err := client.Do(req)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode < 400 {
				t.Fatalf("legacy credential bypass: %d", response.StatusCode)
			}
		}
		if reached.Load() != 0 || credentialLeaked.Load() {
			t.Fatal("legacy wildcard secret bypassed the reviewed GitLab boundary")
		}
	}
	denied()
	must(http.MethodPatch, base+"/bots/"+bot.Id.String()+"/egress", map[string]any{"policy": "allowlist", "allowedHosts": []string{"gitlab.example.org"}, "expectedRevision": 0}, 200)
	denied()
	assignments := base + "/bots/" + bot.Id.String() + "/gitlab-instances"
	must(http.MethodPut, assignments, map[string]any{"origins": []string{origin}}, 200)
	denied()
	must(http.MethodDelete, base+"/gitlab-instances", map[string]any{"origin": origin}, 204)
	denied()
	register()
	must(http.MethodPut, assignments, map[string]any{"origins": []string{origin}}, 200)
	denied()
}
