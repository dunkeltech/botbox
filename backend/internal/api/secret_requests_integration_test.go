package api_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/api"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secretrequests"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func pgUUID(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }
func uuidFromPG(id pgtype.UUID) uuid.UUID {
	return uuid.UUID(id.Bytes)
}

func TestSecretRequestsRBACLifecycleIsolationAndProxy(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue, chatLimit: 500})
	owner, _ := h.completeSetup("owner-sr@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()

	atlas := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas-sr")
	admin, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "admin-sr@example.com", "admin", "Admin", "CorrectHorse123!")
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "member-sr@example.com", "member", "Member", "CorrectHorse123!")
	outsider, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "outsider-sr@example.com", "member", "Outsider", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "viewer-sr@example.com", "viewer", "Viewer", "CorrectHorse123!")

	otherResp := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "OtherWS", "slug": "other-ws"}, owner.AccessToken, nil, true)
	var otherWS apigen.WorkspaceSummary
	otherResp.decode(t, &otherWS)
	eve, _ := h.inviteAndAcceptNewUser(owner.AccessToken, otherWS.Id, "eve-sr@other.com", "admin", "Eve", "CorrectHorse123!")
	if eve.AccessToken == "" {
		t.Fatal("expected eve access token")
	}

	const testSecretToken = "ghp_integration_secret_never_leak_9988"

	// Controlled turn executor: when prompt mentions "need-secret", call request_secret; on continuation, emit placeholder echo.
	var execMu sync.Mutex
	var seenPrompts []string
	worker := &engine.Worker{
		Pool:    h.pool,
		Now:     h.clock.Now,
		Enqueue: queue.Enqueue,
		Execute: func(_ context.Context, s engine.Session, emit func(engine.Event) error) error {
			execMu.Lock()
			seenPrompts = append(seenPrompts, s.ContinuationPrompt)
			execMu.Unlock()

			if s.ContinuationPrompt != "" {
				return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "Resumed cleanly: " + s.ContinuationPrompt})
			}
			if len(s.Messages) > 0 && strings.Contains(s.Messages[0].Content, "need-secret") {
				return emit(engine.Event{
					Kind:      engine.EventKindToolCall,
					ToolID:    "call_req_1",
					ToolName:  "mcp__botbox-desktop__request_secret",
					ToolInput: []byte(`{"kind":"cli_egress","adapter_id":"github_api","reason":"List open releases","env_placeholder_name":"GH_TOKEN"}`),
				})
			}
			return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "Normal reply"})
		},
	}

	// 1. Browser login is rejected at capability boundary before creating any fillable request.
	dmResp := h.request(http.MethodPost, base+"/bots/"+atlas.Id.String()+"/dm", nil, member.AccessToken, nil, true)
	var memberDM apigen.ChatConversation
	dmResp.decode(t, &memberDM)
	dmTurnID := uuid.New()
	_, err := h.queries.CreateChatMessage(context.Background(), db.CreateChatMessageParams{
		ID: pgUUID(dmTurnID), WorkspaceID: pgUUID(ws), ConversationID: pgUUID(memberDM.Id),
		SenderType: "user", SenderUserID: pgUUID(member.User.Id), Kind: "text", Content: "browser check", Metadata: []byte("{}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	dmTurn, err := h.queries.CreateBotTurn(context.Background(), db.CreateBotTurnParams{
		WorkspaceID: pgUUID(ws), BotID: pgUUID(atlas.Id), ConversationID: pgUUID(memberDM.Id), MessageID: pgUUID(dmTurnID),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = secretrequests.CreateRequest(context.Background(), h.pool, secretrequests.CreateInput{
		WorkspaceID:    ws,
		BotID:          atlas.Id,
		ConversationID: memberDM.Id,
		TurnID:         uuidFromPG(dmTurn.ID),
		Kind:           secretrequests.KindBrowserLogin,
		AdapterID:      "github_api",
		Reason:         "Browser login",
		Now:            h.clock.Now(),
	})
	if !errors.Is(err, secretrequests.ErrUnsupportedCapability) {
		t.Fatalf("expected ErrUnsupportedCapability for browser_login, got %v", err)
	}
	// Mark helper DM turn completed so queue stays clean.
	_ = h.queries.UpdateBotTurn(context.Background(), db.UpdateBotTurnParams{WorkspaceID: pgUUID(ws), ID: dmTurn.ID, Status: "completed"})

	// 2. Create a group with owner, member, viewer, and Atlas (outsider and admin are NOT group members).
	grp := askGroup(t, h, base, owner.AccessToken, "Ops", atlas.Id, false, 20, []any{}, []any{
		groupUser(member.User.Id, "operator"),
		groupUser(viewer.User.Id, "observer"),
		groupBot(atlas.Id, "lead"),
	})

	postGroup(t, h, base, member.AccessToken, grp.Id, "@Atlas need-secret")
	drainTurns(t, worker, queue, 2)

	// Verify card message + waiting_secret turn state persisted.
	histResp := h.request(http.MethodGet, base+"/groups/"+grp.Id.String()+"/messages", nil, member.AccessToken, nil, false)
	if histResp.status != http.StatusOK {
		t.Fatalf("group history status=%d %s", histResp.status, histResp.body)
	}
	var hist apigen.ChatHistory
	histResp.decode(t, &hist)
	var reqSummary *apigen.SecretRequestSummary
	for i := range hist.Messages {
		if hist.Messages[i].Kind == apigen.ChatMessageKindSecretRequest && hist.Messages[i].SecretRequest != nil {
			reqSummary = hist.Messages[i].SecretRequest
		}
	}
	if reqSummary == nil || reqSummary.Status != apigen.SecretRequestStatusPending {
		t.Fatalf("expected pending secret_request card in chat history, got %+v", hist.Messages)
	}
	if reqSummary.PlaceholderValue == nil || !strings.HasPrefix(*reqSummary.PlaceholderValue, "botbox-placeholder-") {
		t.Fatalf("expected non-secret placeholder value on card, got %+v", reqSummary.PlaceholderValue)
	}
	if len(hist.Turns) == 0 || hist.Turns[0].Status != apigen.ChatTurnStatusWaitingSecret {
		t.Fatalf("expected waiting_secret turn status, got %+v", hist.Turns)
	}

	// 3. Running ExpireStaleBotTurns 30 minutes later must NOT expire waiting_secret as a stale running turn!
	if err := h.queries.ExpireStaleBotTurns(context.Background(), db.ExpireStaleBotTurnsParams{
		WorkspaceID: pgUUID(ws), BotID: pgUUID(atlas.Id), AsOf: pgtype.Timestamptz{Time: h.clock.Now().Add(30 * time.Minute), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	turnRow, err := h.queries.GetBotTurn(context.Background(), db.GetBotTurnParams{WorkspaceID: pgUUID(ws), ID: pgUUID(hist.Turns[0].Id)})
	if err != nil || turnRow.Status != "waiting_secret" {
		t.Fatalf("waiting_secret turn was unexpectedly altered: status=%s err=%v", turnRow.Status, err)
	}

	reqPath := base + "/secret-requests/" + reqSummary.SecretRequestId.String()
	// 4. Negative auth/RBAC/isolation/allowlist checks BEFORE fulfilling:
	// 4a. Unauthenticated (with CSRF) -> 401; missing CSRF -> 403
	if r := h.request(http.MethodPost, reqPath+"/fulfill", map[string]any{"secretValue": testSecretToken, "scope": "ephemeral"}, "", nil, true); r.status != http.StatusUnauthorized {
		t.Fatalf("unauth fulfill=%d", r.status)
	}
	if r := h.request(http.MethodPost, reqPath+"/fulfill", map[string]any{"secretValue": testSecretToken, "scope": "ephemeral"}, "", nil, false); r.status != http.StatusForbidden {
		t.Fatalf("missing csrf fulfill=%d", r.status)
	}
	// 4b. Cross-workspace caller -> 404
	if r := h.request(http.MethodPost, reqPath+"/fulfill", map[string]any{"secretValue": testSecretToken, "scope": "ephemeral"}, eve.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("cross-workspace fulfill=%d", r.status)
	}
	// 4c. Workspace outsider (not in group) & even admin not in group -> 404
	if r := h.request(http.MethodPost, reqPath+"/fulfill", map[string]any{"secretValue": testSecretToken, "scope": "ephemeral"}, outsider.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("non-member outsider fulfill=%d", r.status)
	}
	if r := h.request(http.MethodPost, reqPath+"/fulfill", map[string]any{"secretValue": testSecretToken, "scope": "ephemeral"}, admin.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("non-member admin fulfill=%d", r.status)
	}
	// 4d. Viewer in group -> 403
	if r := h.request(http.MethodPost, reqPath+"/fulfill", map[string]any{"secretValue": testSecretToken, "scope": "ephemeral"}, viewer.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("viewer fulfill=%d", r.status)
	}
	if r := h.request(http.MethodPost, reqPath+"/deny", nil, viewer.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("viewer deny=%d", r.status)
	}
	// 4e. Member requesting workspace_secret -> 403
	if r := h.request(http.MethodPost, reqPath+"/fulfill", map[string]any{"secretValue": testSecretToken, "scope": "workspace_secret"}, member.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("member workspace_secret=%d", r.status)
	}
	// 4f. Host NOT on bot egress allowlist -> 409 host_not_allowed (never implicitly expands allowlist!)
	if r := h.request(http.MethodPost, reqPath+"/fulfill", map[string]any{"secretValue": testSecretToken, "scope": "ephemeral"}, member.AccessToken, nil, true); r.status != http.StatusConflict || r.errorCode(t) != "secret_request.host_not_allowed" {
		t.Fatalf("expected 409 host_not_allowed, got %d %s", r.status, r.body)
	}

	// Explicitly allow api.github.com on bot egress policy (separate permission step).
	if _, err := h.queries.UpsertEgressRule(context.Background(), db.UpsertEgressRuleParams{
		BotID: pgUUID(atlas.Id), WorkspaceID: pgUUID(ws), Policy: "allowlist", AllowedHosts: []string{"api.github.com"}, BlockedHosts: []string{}, Revision: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// 5. Concurrent race: member fulfills AND owner denies at the same time -> exactly ONE 200 winner, loser gets 409!
	startGate := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var r1, r2 apiResponse
	go func() {
		defer wg.Done()
		<-startGate
		r1 = h.request(http.MethodPost, reqPath+"/fulfill", map[string]any{"secretValue": testSecretToken, "scope": "ephemeral"}, member.AccessToken, nil, true)
	}()
	go func() {
		defer wg.Done()
		<-startGate
		r2 = h.request(http.MethodPost, reqPath+"/deny", nil, owner.AccessToken, nil, true)
	}()
	close(startGate)
	wg.Wait()

	// Ensure deterministic test continuation if deny happened to win the race by creating a second request if needed,
	// or verifying exactly one 200 and one 409.
	if (r1.status != http.StatusOK || r2.status != http.StatusConflict) && (r2.status != http.StatusOK || r1.status != http.StatusConflict) {
		t.Fatalf("expected exactly one 200 and one 409 in concurrent fulfill/deny race, got fulfill=%d (%s) deny=%d (%s)", r1.status, r1.body, r2.status, r2.body)
	}
	if r1.status != http.StatusOK {
		// Re-run a fresh request when deny won the race so we test the full ephemeral lease + proxy flow!
		drainTurns(t, worker, queue, 2)
		postGroup(t, h, base, member.AccessToken, grp.Id, "@Atlas need-secret")
		drainTurns(t, worker, queue, 2)
		histResp = h.request(http.MethodGet, base+"/groups/"+grp.Id.String()+"/messages", nil, member.AccessToken, nil, false)
		histResp.decode(t, &hist)
		reqSummary = nil
		for i := range hist.Messages {
			if hist.Messages[i].Kind == apigen.ChatMessageKindSecretRequest && hist.Messages[i].SecretRequest != nil && hist.Messages[i].SecretRequest.Status == apigen.SecretRequestStatusPending {
				reqSummary = hist.Messages[i].SecretRequest
			}
		}
		if reqSummary == nil {
			t.Fatal("expected second pending secret_request")
		}
		reqPath = base + "/secret-requests/" + reqSummary.SecretRequestId.String()
		r1 = h.request(http.MethodPost, reqPath+"/fulfill", map[string]any{"secretValue": testSecretToken, "scope": "ephemeral"}, member.AccessToken, nil, true)
		if r1.status != http.StatusOK {
			t.Fatalf("fulfill failed: %d %s", r1.status, r1.body)
		}
	}
	// Duplicate fulfill must return 409.
	if dup := h.request(http.MethodPost, reqPath+"/fulfill", map[string]any{"secretValue": testSecretToken, "scope": "ephemeral"}, member.AccessToken, nil, true); dup.status != http.StatusConflict {
		t.Fatalf("expected 409 on duplicate fulfill, got %d", dup.status)
	}

	// 6. Verify egress proxy injects ephemeral lease while active, and another conversation's queued turn cannot jump ahead!
	dmMsgID := uuid.New()
	_, err = h.queries.CreateChatMessage(context.Background(), db.CreateChatMessageParams{
		ID: pgUUID(dmMsgID), WorkspaceID: pgUUID(ws), ConversationID: pgUUID(memberDM.Id),
		SenderType: "user", SenderUserID: pgUUID(member.User.Id), Kind: "text", Content: "competing dm turn", Metadata: []byte("{}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	competingTurn, err := h.queries.CreateBotTurn(context.Background(), db.CreateBotTurnParams{
		WorkspaceID: pgUUID(ws), BotID: pgUUID(atlas.Id), ConversationID: pgUUID(memberDM.Id), MessageID: pgUUID(dmMsgID),
	})
	if err != nil {
		t.Fatal(err)
	}
	// NextQueuedBotTurn must skip competingTurn while ephemeral lease belongs to group turn!
	nextTurn, err := h.queries.NextQueuedBotTurn(context.Background(), db.NextQueuedBotTurnParams{WorkspaceID: pgUUID(ws), BotID: pgUUID(atlas.Id)})
	if err != nil {
		t.Fatal(err)
	}
	if nextTurn.ID == competingTurn.ID {
		t.Fatal("NextQueuedBotTurn selected competing conversation turn while ephemeral lease is active")
	}

	// Test NewBotEgressProxyHandler injects ephemeral lease on api.github.com and scrubs echoed token!
	upstreamCA, err := mitmproxy.NewBotCA("upstream-github")
	if err != nil {
		t.Fatal(err)
	}
	ghCert, err := upstreamCA.LeafCertificate("api.github.com")
	if err != nil {
		t.Fatal(err)
	}
	var gotUpstreamAuth string
	ghSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUpstreamAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"echo":%q}`, gotUpstreamAuth)
	}))
	ghSrv.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*ghCert}}
	ghSrv.StartTLS()
	defer ghSrv.Close()

	upRoots := x509.NewCertPool()
	upRoots.AppendCertsFromPEM(upstreamCA.CertPEM())
	ghURL, _ := url.Parse(ghSrv.URL)

	botCA, err := mitmproxy.NewBotCA("bot-atlas-sr")
	if err != nil {
		t.Fatal(err)
	}
	proxyHandler, err := api.NewBotEgressProxyHandler(api.BotEgressProxyDeps{
		Pool:            h.pool,
		KeyRing:         h.keyRing,
		WorkspaceID:     ws,
		BotID:           atlas.Id,
		CA:              botCA,
		UpstreamRootCAs: upRoots,
		Now:             h.clock.Now,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, ghURL.Host)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	proxySrv := httptest.NewServer(proxyHandler)
	defer proxySrv.Close()
	proxyURL, _ := url.Parse(proxySrv.URL)
	clientRoots := x509.NewCertPool()
	clientRoots.AppendCertsFromPEM(botCA.CertPEM())
	clientRoots.AppendCertsFromPEM(upstreamCA.CertPEM())
	client := &http.Client{Transport: &http.Transport{
		Proxy:             http.ProxyURL(proxyURL),
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: clientRoots},
	}}
	resp, err := client.Get("https://api.github.com/user")
	if err != nil {
		t.Fatalf("proxied GET api.github.com: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if gotUpstreamAuth != "Bearer "+testSecretToken {
		t.Fatalf("expected proxy to inject ephemeral lease, got %q", gotUpstreamAuth)
	}
	if strings.Contains(string(respBody), testSecretToken) {
		t.Fatalf("ephemeral secret echoed back to sandbox: %s", respBody)
	}

	// 7. Drain turns -> resumed group turn completes and revokes ephemeral lease, then competing turn runs!
	drainTurns(t, worker, queue, 2)
	// Also process competingTurn now that lease is revoked.
	if err := queue.Enqueue(context.Background(), ws, atlas.Id, uuidFromPG(competingTurn.ID)); err != nil {
		t.Fatal(err)
	}
	drainTurns(t, worker, queue, 2)

	leases, err := h.queries.ListActiveSecretLeasesForBot(context.Background(), db.ListActiveSecretLeasesForBotParams{
		WorkspaceID: pgUUID(ws), BotID: pgUUID(atlas.Id), AsOf: pgtype.Timestamptz{Time: h.clock.Now(), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 0 {
		t.Fatalf("expected all ephemeral leases revoked after turn completion, got %d", len(leases))
	}

	// 8. Scan messages, audit_log, egress_log, and continuation prompts: secret must NEVER appear!
	var leakCount int
	for _, query := range []string{
		"SELECT count(*) FROM messages WHERE content LIKE $1 OR metadata::text LIKE $1",
		"SELECT count(*) FROM bot_turns WHERE continuation_prompt LIKE $1",
		"SELECT count(*) FROM audit_log WHERE metadata::text LIKE $1",
		"SELECT count(*) FROM egress_log WHERE path LIKE $1 OR secret_name LIKE $1",
	} {
		var c int
		if err := h.pool.QueryRow(context.Background(), query, "%"+testSecretToken+"%").Scan(&c); err != nil {
			t.Fatal(err)
		}
		leakCount += c
	}
	if leakCount != 0 {
		t.Fatalf("detected %d secret leaks in persisted DB tables", leakCount)
	}

	// 9. Persistent bot_secret conflict detection (409 rule_conflict unless replaceExisting=true with manage permission).
	postGroup(t, h, base, member.AccessToken, grp.Id, "@Atlas need-secret")
	drainTurns(t, worker, queue, 2)
	histResp = h.request(http.MethodGet, base+"/groups/"+grp.Id.String()+"/messages", nil, member.AccessToken, nil, false)
	histResp.decode(t, &hist)
	var secondReqID string
	for i := range hist.Messages {
		if hist.Messages[i].Kind == apigen.ChatMessageKindSecretRequest && hist.Messages[i].SecretRequest != nil && hist.Messages[i].SecretRequest.Status == apigen.SecretRequestStatusPending {
			secondReqID = hist.Messages[i].SecretRequest.SecretRequestId.String()
		}
	}
	if secondReqID == "" {
		t.Fatal("expected pending secret_request for persistent conflict check")
	}
	// Store bot_secret once.
	if r := h.request(http.MethodPost, base+"/secret-requests/"+secondReqID+"/fulfill", map[string]any{"secretValue": "ghp_persistent_1", "scope": "bot_secret"}, member.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("first bot_secret fulfill=%d %s", r.status, r.body)
	}
	drainTurns(t, worker, queue, 2)

	// Next request for same adapter conflicts without replaceExisting.
	postGroup(t, h, base, member.AccessToken, grp.Id, "@Atlas need-secret")
	drainTurns(t, worker, queue, 2)
	histResp = h.request(http.MethodGet, base+"/groups/"+grp.Id.String()+"/messages", nil, member.AccessToken, nil, false)
	histResp.decode(t, &hist)
	var thirdReqID string
	for i := range hist.Messages {
		if hist.Messages[i].Kind == apigen.ChatMessageKindSecretRequest && hist.Messages[i].SecretRequest != nil && hist.Messages[i].SecretRequest.Status == apigen.SecretRequestStatusPending {
			thirdReqID = hist.Messages[i].SecretRequest.SecretRequestId.String()
		}
	}
	const secondPersistent = "ghp_persistent_2" //nolint:gosec // synthetic test secret
	if r := h.request(http.MethodPost, base+"/secret-requests/"+thirdReqID+"/fulfill", map[string]any{"secretValue": secondPersistent, "scope": "bot_secret"}, owner.AccessToken, nil, true); r.status != http.StatusConflict || r.errorCode(t) != "secret_request.rule_conflict" {
		t.Fatalf("expected 409 rule_conflict, got %d %s", r.status, r.body)
	}
	// Non-bot-owner member trying replaceExisting=true gets 403, while owner succeeds!
	if r := h.request(http.MethodPost, base+"/secret-requests/"+thirdReqID+"/fulfill", map[string]any{"secretValue": secondPersistent, "scope": "bot_secret", "replaceExisting": true}, member.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("expected 403 for non-bot-owner member replaceExisting, got %d", r.status)
	}
	if r := h.request(http.MethodPost, base+"/secret-requests/"+thirdReqID+"/fulfill", map[string]any{"secretValue": secondPersistent, "scope": "bot_secret", "replaceExisting": true}, owner.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("expected 200 for owner replaceExisting, got %d %s", r.status, r.body)
	}
}
