package api_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/api"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestPhase3SecretsWriteOnlyCRUDRBACAndValidation(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t, nil, harnessOpts{})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Alex", "Acme Bots", "acme")
	wsID := owner.ActiveWorkspace.Id.String()

	// Create two bots: one owned by Alex (owner), one owned by Mia (member).
	createBot := func(token, name string) apigen.Bot {
		resp := h.request(http.MethodPost, "/api/workspaces/"+wsID+"/bots", map[string]any{
			"name":        name,
			"avatarSeed":  strings.ToLower(name) + "-seed",
			"llmMode":     "cli",
			"llmProvider": "anthropic",
			"llmModel":    "claude-sonnet-4",
		}, token, nil, true)
		if resp.status != http.StatusCreated {
			t.Fatalf("create bot %s: status=%d body=%s", name, resp.status, resp.body)
		}
		var bot apigen.Bot
		resp.decode(t, &bot)
		return bot
	}

	alexBot := createBot(owner.AccessToken, "Atlas")
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, owner.ActiveWorkspace.Id, "mia@example.com", "member", "Mia", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, owner.ActiveWorkspace.Id, "sam@example.com", "viewer", "Sam", "CorrectHorse123!")
	miaBot := createBot(member.AccessToken, "Courier")

	// Provision isolated second workspace with Eve for cross-workspace 404 checks.
	otherWsResp := h.request(http.MethodPost, "/api/workspaces", map[string]any{
		"name": "Other Org",
		"slug": "other-org",
	}, owner.AccessToken, nil, true)
	if otherWsResp.status != http.StatusCreated {
		t.Fatalf("create other workspace: status=%d body=%s", otherWsResp.status, otherWsResp.body)
	}
	var otherWs apigen.WorkspaceSummary
	otherWsResp.decode(t, &otherWs)
	eve, _ := h.inviteAndAcceptNewUser(owner.AccessToken, otherWs.Id, "eve@other.com", "owner", "Eve", "CorrectHorse123!")

	atlasBase := "/api/workspaces/" + wsID + "/bots/" + alexBot.Id.String()
	miaBotBase := "/api/workspaces/" + wsID + "/bots/" + miaBot.Id.String()
	wsSecretsURL := "/api/workspaces/" + wsID + "/secrets"

	// 1. Unauthenticated -> 401.
	for _, ep := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, wsSecretsURL},
		{http.MethodPost, wsSecretsURL},
		{http.MethodGet, atlasBase + "/secrets"},
		{http.MethodPost, atlasBase + "/secrets"},
		{http.MethodGet, atlasBase + "/egress"},
		{http.MethodPatch, atlasBase + "/egress"},
		{http.MethodGet, atlasBase + "/egress/logs"},
	} {
		r := h.request(ep.method, ep.path, nil, "", nil, ep.method != http.MethodGet)
		if r.status != http.StatusUnauthorized {
			t.Fatalf("%s %s unauthenticated = %d, want 401", ep.method, ep.path, r.status)
		}
	}

	// 2. Workspace secret write-only creation & DB ciphertext check.
	const wsPlaintext = "npm_secret_value_abc123def99"
	createWS := h.request(http.MethodPost, wsSecretsURL, map[string]any{
		"name":        "CORP_NPM_TOKEN",
		"domainMatch": "registry.npmjs.org",
		"authScheme":  "bearer",
		"value":       wsPlaintext,
	}, owner.AccessToken, nil, true)
	if createWS.status != http.StatusCreated {
		t.Fatalf("create workspace secret: status=%d body=%s", createWS.status, createWS.body)
	}
	if bytes.Contains(createWS.body, []byte(wsPlaintext)) {
		t.Fatalf("create workspace secret response leaked plaintext: %s", createWS.body)
	}
	var wsSecret apigen.SecretMetadata
	createWS.decode(t, &wsSecret)
	if wsSecret.Scope != apigen.SecretScopeWorkspace || wsSecret.BotId != nil || wsSecret.MaskedPreview == "" || wsSecret.LastUsedAt != nil {
		t.Fatalf("unexpected workspace secret metadata: %+v", wsSecret)
	}

	// Raw Postgres row must never store plaintext secret bytes.
	storedWS, err := h.queries.GetWorkspaceSecretByWorkspaceAndID(context.Background(), db.GetWorkspaceSecretByWorkspaceAndIDParams{
		WorkspaceID: pgtype.UUID{Bytes: owner.ActiveWorkspace.Id, Valid: true},
		ID:          pgtype.UUID{Bytes: wsSecret.Id, Valid: true},
	})
	if err != nil {
		t.Fatalf("load raw workspace secret: %v", err)
	}
	if bytes.Contains(storedWS.EncryptedValue, []byte(wsPlaintext)) || bytes.Contains(storedWS.WrappedDek, []byte(wsPlaintext)) {
		t.Fatal("DB columns contain plaintext secret material")
	}

	// 3. Duplicate workspace secret name -> 409 secret.already_exists.
	dupWS := h.request(http.MethodPost, wsSecretsURL, map[string]any{
		"name":        "CORP_NPM_TOKEN",
		"domainMatch": "registry.npmjs.org",
		"authScheme":  "bearer",
		"value":       "another-value-999",
	}, owner.AccessToken, nil, true)
	if dupWS.status != http.StatusConflict {
		t.Fatalf("duplicate workspace secret = %d, want 409: %s", dupWS.status, dupWS.body)
	}

	// 4. Negative validation tests -> 400.
	invalidPayloads := []struct {
		name string
		body map[string]any
	}{
		{"blank value", map[string]any{"name": "K1", "domainMatch": "api.github.com", "authScheme": "bearer", "value": "   "}},
		{"scheme in domainMatch", map[string]any{"name": "K2", "domainMatch": "https://api.github.com", "authScheme": "bearer", "value": "secret-123"}},
		{"port in domainMatch", map[string]any{"name": "K3", "domainMatch": "api.github.com:443", "authScheme": "bearer", "value": "secret-123"}},
		{"wildcard in domainMatch", map[string]any{"name": "K4", "domainMatch": "*.github.com", "authScheme": "bearer", "value": "secret-123"}},
		{"header scheme missing headerName", map[string]any{"name": "K5", "domainMatch": "api.github.com", "authScheme": "header", "value": "secret-123"}},
		{"reserved headerName Host", map[string]any{"name": "K6", "domainMatch": "api.github.com", "authScheme": "header", "headerName": "Host", "value": "secret-123"}},
		{"basic scheme missing username", map[string]any{"name": "K7", "domainMatch": "api.github.com", "authScheme": "basic", "value": "secret-123"}},
		{"pathPrefix with query string", map[string]any{"name": "K8", "domainMatch": "api.github.com", "pathPrefix": "/v1?x=1", "authScheme": "bearer", "value": "secret-123"}},
	}
	for _, tc := range invalidPayloads {
		r := h.request(http.MethodPost, atlasBase+"/secrets", tc.body, owner.AccessToken, nil, true)
		if r.status != http.StatusBadRequest {
			t.Fatalf("%s: status=%d, want 400 (body=%s)", tc.name, r.status, r.body)
		}
	}

	// 5. Role-based access control on workspace secrets (member/viewer forbidden from writes, allowed reads).
	for _, nonAdminToken := range []string{member.AccessToken, viewer.AccessToken} {
		if r := h.request(http.MethodGet, wsSecretsURL, nil, nonAdminToken, nil, false); r.status != http.StatusOK {
			t.Fatalf("non-admin GET workspace secrets = %d, want 200", r.status)
		}
		if r := h.request(http.MethodPost, wsSecretsURL, map[string]any{
			"name": "BAD", "domainMatch": "api.github.com", "authScheme": "bearer", "value": "val-1234567890",
		}, nonAdminToken, nil, true); r.status != http.StatusForbidden {
			t.Fatalf("non-admin POST workspace secret = %d, want 403", r.status)
		}
		if r := h.request(http.MethodPut, wsSecretsURL+"/"+wsSecret.Id.String(), map[string]any{
			"name": "CORP_NPM_TOKEN", "domainMatch": "registry.npmjs.org", "authScheme": "bearer",
		}, nonAdminToken, nil, true); r.status != http.StatusForbidden {
			t.Fatalf("non-admin PUT workspace secret = %d, want 403", r.status)
		}
		if r := h.request(http.MethodDelete, wsSecretsURL+"/"+wsSecret.Id.String(), nil, nonAdminToken, nil, true); r.status != http.StatusForbidden {
			t.Fatalf("non-admin DELETE workspace secret = %d, want 403", r.status)
		}
	}

	// 6. Metadata-only PUT preserves ciphertext and maskedPreview; rotating value updates preview.
	updMeta := h.request(http.MethodPut, wsSecretsURL+"/"+wsSecret.Id.String(), map[string]any{
		"name":            "CORP_NPM_TOKEN",
		"domainMatch":     "npm.corp.example",
		"matchSubdomains": true,
		"authScheme":      "bearer",
	}, owner.AccessToken, nil, true)
	if updMeta.status != http.StatusOK {
		t.Fatalf("metadata-only PUT workspace secret = %d: %s", updMeta.status, updMeta.body)
	}
	var updatedWS apigen.SecretMetadata
	updMeta.decode(t, &updatedWS)
	if updatedWS.MaskedPreview != wsSecret.MaskedPreview || updatedWS.DomainMatch != "npm.corp.example" || !updatedWS.MatchSubdomains {
		t.Fatalf("metadata-only PUT altered preview or dropped fields: %+v", updatedWS)
	}

	// 7. Per-bot secrets & egress RBAC:
	// - Viewer forbidden on Alex's and Mia's bots.
	// - Member (Mia) forbidden on Alex's bot, ALLOWED on Mia's own bot!
	const miaPlaintext = "ghp_mia_bot_secret_value_889900"
	if r := h.request(http.MethodPost, atlasBase+"/secrets", map[string]any{
		"name": "FORBIDDEN_BOT_KEY", "domainMatch": "api.github.com", "authScheme": "bearer", "value": miaPlaintext,
	}, member.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("non-owner member create bot secret = %d, want 403", r.status)
	}
	if r := h.request(http.MethodPatch, atlasBase+"/egress", map[string]any{
		"policy": "open", "allowedHosts": []string{},
	}, member.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("non-owner member PATCH egress = %d, want 403", r.status)
	}
	if r := h.request(http.MethodPatch, miaBotBase+"/egress", map[string]any{
		"policy": "open", "allowedHosts": []string{},
	}, viewer.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("viewer PATCH egress = %d, want 403", r.status)
	}

	miaCreate := h.request(http.MethodPost, miaBotBase+"/secrets", map[string]any{
		"name": "MIA_GH_TOKEN", "domainMatch": "api.github.com", "authScheme": "bearer", "value": miaPlaintext,
	}, member.AccessToken, nil, true)
	if miaCreate.status != http.StatusCreated {
		t.Fatalf("bot owner member create secret = %d: %s", miaCreate.status, miaCreate.body)
	}
	var miaSecret apigen.SecretMetadata
	miaCreate.decode(t, &miaSecret)

	// Listing bot secrets includes both Mia's bot secret and Acme's workspace secret (zero plaintext).
	listMia := h.request(http.MethodGet, miaBotBase+"/secrets", nil, viewer.AccessToken, nil, false)
	if listMia.status != http.StatusOK {
		t.Fatalf("viewer list bot secrets = %d: %s", listMia.status, listMia.body)
	}
	if bytes.Contains(listMia.body, []byte(miaPlaintext)) || bytes.Contains(listMia.body, []byte(wsPlaintext)) {
		t.Fatalf("list bot secrets leaked plaintext: %s", listMia.body)
	}
	var listMiaResp apigen.SecretListResponse
	listMia.decode(t, &listMiaResp)
	if len(listMiaResp.Secrets) != 2 {
		t.Fatalf("expected 2 secrets (bot + workspace), got %d", len(listMiaResp.Secrets))
	}

	// Mia updates egress policy on her own bot -> 200, includes effectiveSecretHosts.
	patchMiaEgress := h.request(http.MethodPatch, miaBotBase+"/egress", map[string]any{
		"policy":       "allowlist",
		"allowedHosts": []string{"pypi.org", "*.pythonhosted.org", "PYPI.ORG"},
	}, member.AccessToken, nil, true)
	if patchMiaEgress.status != http.StatusOK {
		t.Fatalf("bot owner PATCH egress = %d: %s", patchMiaEgress.status, patchMiaEgress.body)
	}
	var egressResp apigen.EgressPolicyResponse
	patchMiaEgress.decode(t, &egressResp)
	if egressResp.Policy != apigen.Allowlist || len(egressResp.AllowedHosts) != 2 || len(egressResp.EffectiveSecretHosts) < 2 {
		t.Fatalf("unexpected egress response: %+v", egressResp)
	}

	// 8. Cross-workspace isolation -> 404 across all secret and egress endpoints.
	for _, ep := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, wsSecretsURL, nil},
		{http.MethodPost, wsSecretsURL, map[string]any{"name": "X", "domainMatch": "api.github.com", "authScheme": "bearer", "value": "val-123456"}},
		{http.MethodPut, wsSecretsURL + "/" + wsSecret.Id.String(), map[string]any{"name": "X", "domainMatch": "api.github.com", "authScheme": "bearer"}},
		{http.MethodDelete, wsSecretsURL + "/" + wsSecret.Id.String(), nil},
		{http.MethodGet, miaBotBase + "/secrets", nil},
		{http.MethodPost, miaBotBase + "/secrets", map[string]any{"name": "X", "domainMatch": "api.github.com", "authScheme": "bearer", "value": "val-123456"}},
		{http.MethodPut, miaBotBase + "/secrets/" + miaSecret.Id.String(), map[string]any{"name": "X", "domainMatch": "api.github.com", "authScheme": "bearer"}},
		{http.MethodDelete, miaBotBase + "/secrets/" + miaSecret.Id.String(), nil},
		{http.MethodGet, miaBotBase + "/egress", nil},
		{http.MethodPatch, miaBotBase + "/egress", map[string]any{"policy": "open", "allowedHosts": []string{}}},
		{http.MethodGet, miaBotBase + "/egress/logs", nil},
	} {
		r := h.request(ep.method, ep.path, ep.body, eve.AccessToken, nil, ep.method != http.MethodGet)
		if r.status != http.StatusNotFound {
			t.Fatalf("cross-workspace %s %s = %d, want 404 (body=%s)", ep.method, ep.path, r.status, r.body)
		}
	}

	// Owner can delete bot secret; second delete returns 404 secret.not_found.
	if r := h.request(http.MethodDelete, miaBotBase+"/secrets/"+miaSecret.Id.String(), nil, member.AccessToken, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("delete bot secret = %d, want 204", r.status)
	}
	if r := h.request(http.MethodDelete, miaBotBase+"/secrets/"+miaSecret.Id.String(), nil, member.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("repeat delete bot secret = %d, want 404", r.status)
	}

	// Structured logs must never contain secret plaintext.
	logText := h.logs.String()
	if strings.Contains(logText, wsPlaintext) || strings.Contains(logText, miaPlaintext) {
		t.Fatal("API structured logs leaked plaintext secret")
	}
}

func TestPhase3LiveEgressProxyEndToEndAndPrecedence(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t, nil, harnessOpts{})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Alex", "Acme Bots", "acme")
	wsID := owner.ActiveWorkspace.Id.String()

	botResp := h.request(http.MethodPost, "/api/workspaces/"+wsID+"/bots", map[string]any{
		"name":        "ProxyBot",
		"avatarSeed":  "proxybot-seed",
		"llmMode":     "api",
		"llmProvider": "anthropic",
		"llmModel":    "claude-sonnet-4",
	}, owner.AccessToken, nil, true)
	if botResp.status != http.StatusCreated {
		t.Fatalf("create bot: %d %s", botResp.status, botResp.body)
	}
	var bot apigen.Bot
	botResp.decode(t, &bot)
	botBase := "/api/workspaces/" + wsID + "/bots/" + bot.Id.String()

	const (
		wsSecretVal  = "ws-shared-token-1111"    //nolint:gosec // synthetic integration test fixture
		botSecretVal = "bot-override-token-2222" //nolint:gosec // synthetic integration test fixture
	)

	// Workspace secret covers api.example.com.
	r := h.request(http.MethodPost, "/api/workspaces/"+wsID+"/secrets", map[string]any{
		"name":        "SHARED_API",
		"domainMatch": "api.example.com",
		"authScheme":  "bearer",
		"value":       wsSecretVal,
	}, owner.AccessToken, nil, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create workspace secret: %d %s", r.status, r.body)
	}

	// Bot secret overrides api.example.com specifically under /v2/special with custom header.
	r = h.request(http.MethodPost, botBase+"/secrets", map[string]any{
		"name":        "BOT_API_OVERRIDE",
		"domainMatch": "api.example.com",
		"pathPrefix":  "/v2/special",
		"authScheme":  "header",
		"headerName":  "X-Custom-Auth",
		"value":       botSecretVal,
	}, owner.AccessToken, nil, true)
	if r.status != http.StatusCreated {
		t.Fatalf("create bot secret: %d %s", r.status, r.body)
	}

	// Configure passthrough allowlist for docs.example.com.
	r = h.request(http.MethodPatch, botBase+"/egress", map[string]any{
		"policy":       "allowlist",
		"allowedHosts": []string{"docs.example.com"},
	}, owner.AccessToken, nil, true)
	if r.status != http.StatusOK {
		t.Fatalf("patch egress: %d %s", r.status, r.body)
	}

	// Upstream TLS server captures received paths + headers.
	var upMu sync.Mutex
	var captured []string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upMu.Lock()
		captured = append(captured, r.URL.Path+"|auth="+r.Header.Get("Authorization")+"|custom="+r.Header.Get("X-Custom-Auth"))
		upMu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok-upstream"))
	}))
	defer upstream.Close()

	ca, err := mitmproxy.NewBotCA(bot.Id.String())
	if err != nil {
		t.Fatalf("NewBotCA: %v", err)
	}
	upURL, _ := url.Parse(upstream.URL)
	proxyHandler, err := api.NewBotEgressProxyHandler(api.BotEgressProxyDeps{
		Pool:            h.pool,
		KeyRing:         h.keyRing,
		WorkspaceID:     owner.ActiveWorkspace.Id,
		BotID:           bot.Id,
		CA:              ca,
		UpstreamRootCAs: upstream.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, upURL.Host)
		},
		Logger: h.logger,
	})
	if err != nil {
		t.Fatalf("NewBotEgressProxyHandler: %v", err)
	}
	proxySrv := httptest.NewServer(proxyHandler)
	defer proxySrv.Close()

	proxyURL, _ := url.Parse(proxySrv.URL)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca.CertPEM())
	roots.AddCert(upstream.Certificate())

	client := &http.Client{
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(proxyURL),
			DisableKeepAlives: true,
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    roots,
			},
		},
	}

	// 1. Non-allowlisted host is blocked with 403.
	blockedResp, err := client.Get("https://blocked.notexample.com/nope")
	if blockedResp != nil && blockedResp.Body != nil {
		_ = blockedResp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected blocked host to fail")
	}

	// 2. Allowlisted non-secret host passes through cleanly without secret injection.
	resp, err := client.Get("https://docs.example.com/guide")
	if err != nil {
		t.Fatalf("passthrough GET: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	// 3. Workspace secret injection + placeholder stripping + query string scrubbing.
	reqWS, _ := http.NewRequest(http.MethodGet, "https://api.example.com/v1/users?token=leak_attempt", nil)
	reqWS.Header.Set("Authorization", "Bearer botbox-placeholder")
	resp, err = client.Do(reqWS)
	if err != nil {
		t.Fatalf("workspace MITM GET: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	// 4. Bot-scoped secret wins on /v2/special.
	resp, err = client.Get("https://api.example.com/v2/special/action?verbose=true")
	if err != nil {
		t.Fatalf("bot override MITM GET: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	upMu.Lock()
	gotCaptured := append([]string(nil), captured...)
	upMu.Unlock()
	if len(gotCaptured) != 3 {
		t.Fatalf("upstream count = %d, want 3: %v", len(gotCaptured), gotCaptured)
	}
	if gotCaptured[0] != "/guide|auth=|custom=" {
		t.Fatalf("passthrough modified auth headers: %q", gotCaptured[0])
	}
	if gotCaptured[1] != "/v1/users|auth=Bearer "+wsSecretVal+"|custom=" {
		t.Fatalf("workspace secret injection mismatch: %q", gotCaptured[1])
	}
	if gotCaptured[2] != "/v2/special/action|auth=|custom="+botSecretVal {
		t.Fatalf("bot secret override mismatch: %q", gotCaptured[2])
	}

	// Verify egress_log entries via API (and query-string stripping & filtering).
	logsResp := h.request(http.MethodGet, botBase+"/egress/logs", nil, owner.AccessToken, nil, false)
	if logsResp.status != http.StatusOK {
		t.Fatalf("GET egress logs = %d: %s", logsResp.status, logsResp.body)
	}
	if bytes.Contains(logsResp.body, []byte("leak_attempt")) || bytes.Contains(logsResp.body, []byte(wsSecretVal)) || bytes.Contains(logsResp.body, []byte(botSecretVal)) {
		t.Fatalf("egress logs leaked query string or secret value: %s", logsResp.body)
	}
	var allLogs apigen.EgressLogListResponse
	logsResp.decode(t, &allLogs)
	if len(allLogs.Logs) != 4 {
		t.Fatalf("expected 4 egress log rows, got %d: %+v", len(allLogs.Logs), allLogs.Logs)
	}

	filteredResp := h.request(http.MethodGet, botBase+"/egress/logs?action=mitm_token_added", nil, owner.AccessToken, nil, false)
	if filteredResp.status != http.StatusOK {
		t.Fatalf("GET filtered logs = %d: %s", filteredResp.status, filteredResp.body)
	}
	var tokenLogs apigen.EgressLogListResponse
	filteredResp.decode(t, &tokenLogs)
	if len(tokenLogs.Logs) != 2 {
		t.Fatalf("expected 2 mitm_token_added logs, got %d", len(tokenLogs.Logs))
	}

	// Verify invalid action filter returns 400.
	if badFilter := h.request(http.MethodGet, botBase+"/egress/logs?action=invalid", nil, owner.AccessToken, nil, false); badFilter.status != http.StatusBadRequest {
		t.Fatalf("invalid action filter = %d, want 400", badFilter.status)
	}

	// Verify secret usage counters incremented cleanly on both bot and workspace secrets.
	secResp := h.request(http.MethodGet, botBase+"/secrets", nil, owner.AccessToken, nil, false)
	if secResp.status != http.StatusOK {
		t.Fatalf("GET bot secrets = %d", secResp.status)
	}
	var secList apigen.SecretListResponse
	secResp.decode(t, &secList)
	for _, s := range secList.Secrets {
		if s.UsedCount != 1 || s.LastUsedAt == nil {
			t.Fatalf("expected usedCount=1 and non-nil lastUsedAt for %s, got %+v", s.Name, s)
		}
	}

	// Structured logs must never contain real credentials.
	if strings.Contains(h.logs.String(), wsSecretVal) || strings.Contains(h.logs.String(), botSecretVal) {
		t.Fatal("proxy/API logs leaked real credential material")
	}
}
