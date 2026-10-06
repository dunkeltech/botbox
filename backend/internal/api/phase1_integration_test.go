package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/api"
	"github.com/dunkeltech/botbox/backend/internal/auth"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/testutil/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock(t time.Time) *testClock {
	return &testClock{now: t.UTC()}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type testHarness struct {
	t        *testing.T
	pool     *pgxpool.Pool
	queries  *db.Queries
	clock    *testClock
	setupMgr *auth.SetupManager
	setupOut *bytes.Buffer
	logs     *bytes.Buffer
	logger   *slog.Logger
	handler  http.Handler
	jwtKey   []byte
	secret   string
}

type harnessOpts struct {
	argon2Iterations uint32
	setupLimit       int
	loginIPLimit     int
	loginEmailLimit  int
	envSetupSecret   string
}

func newTestHarness(t *testing.T, existingPool *pgxpool.Pool, opts harnessOpts) *testHarness {
	t.Helper()

	pool := existingPool
	if pool == nil {
		pool = pgtest.Open(t)
	}
	if opts.argon2Iterations == 0 {
		opts.argon2Iterations = 1
	}
	if opts.setupLimit == 0 {
		opts.setupLimit = 20
	}
	if opts.loginIPLimit == 0 {
		opts.loginIPLimit = 50
	}
	if opts.loginEmailLimit == 0 {
		opts.loginEmailLimit = 20
	}

	clock := newTestClock(time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC))
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	var setupOut bytes.Buffer
	setupMgr := auth.NewSetupManager(&setupOut)

	queries := db.New(pool)
	count, err := queries.CountUsers(context.Background())
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	secret, _, err := setupMgr.EnsureSetupSecret(context.Background(), count, opts.envSetupSecret, logger)
	if err != nil {
		t.Fatalf("EnsureSetupSecret: %v", err)
	}

	privKey, err := auth.GenerateEd25519Key()
	if err != nil {
		t.Fatalf("GenerateEd25519Key: %v", err)
	}
	jwtSvc, err := auth.NewJWTService(privKey, auth.DefaultAccessTokenTTL, clock.Now)
	if err != nil {
		t.Fatalf("NewJWTService: %v", err)
	}

	handler := api.NewHandlerWithConfig(api.Config{
		Logger: logger,
		Pool:   pool,
		JWT:    jwtSvc,
		Argon2Params: auth.Argon2Params{
			MemoryKiB:   1024,
			Iterations:  opts.argon2Iterations,
			Parallelism: 1,
			SaltLen:     16,
			KeyLen:      32,
		},
		Setup:                 setupMgr,
		SetupRateLimiter:      auth.NewRateLimiter(opts.setupLimit, 15*time.Minute, clock.Now),
		LoginIPRateLimiter:    auth.NewRateLimiter(opts.loginIPLimit, time.Minute, clock.Now),
		LoginEmailRateLimiter: auth.NewRateLimiter(opts.loginEmailLimit, 15*time.Minute, clock.Now),
		RefreshRateLimiter:    auth.NewRateLimiter(50, time.Minute, clock.Now),
		PublicBaseURL:         "http://127.0.0.1:3000",
		Now:                   clock.Now,
	})

	return &testHarness{
		t:        t,
		pool:     pool,
		queries:  queries,
		clock:    clock,
		setupMgr: setupMgr,
		setupOut: &setupOut,
		logs:     &logs,
		logger:   logger,
		handler:  handler,
		jwtKey:   privKey,
		secret:   secret,
	}
}

type apiResponse struct {
	status  int
	body    []byte
	headers http.Header
	cookies []*http.Cookie
}

func (h *testHarness) request(method, target string, body any, bearer string, cookies []*http.Cookie, includeCSRF bool) apiResponse {
	h.t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}

	req := httptest.NewRequestWithContext(context.Background(), method, target, reader)
	req.RemoteAddr = "127.0.0.1:42424"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if includeCSRF {
		req.Header.Set("X-BotBox-CSRF", "1")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}

	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	res := rec.Result()
	defer func() {
		if err := res.Body.Close(); err != nil {
			h.t.Fatalf("close body: %v", err)
		}
	}()

	rawBody, err := io.ReadAll(res.Body)
	if err != nil {
		h.t.Fatalf("read response body: %v", err)
	}

	return apiResponse{
		status:  res.StatusCode,
		body:    rawBody,
		headers: res.Header,
		cookies: res.Cookies(),
	}
}

func (r apiResponse) decode(t *testing.T, dst any) {
	t.Helper()
	if err := json.Unmarshal(r.body, dst); err != nil {
		t.Fatalf("decode response JSON (%d %s): %v\nbody=%s", r.status, http.StatusText(r.status), err, r.body)
	}
}

func (r apiResponse) errorCode(t *testing.T) string {
	t.Helper()
	var env apigen.ErrorEnvelope
	r.decode(t, &env)
	return env.Error.Code
}

func (r apiResponse) cookie(name string) *http.Cookie {
	for _, c := range r.cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (h *testHarness) completeSetup(email, password, displayName, wsName, wsSlug string) (apigen.AuthSessionResponse, []*http.Cookie) {
	h.t.Helper()
	resp := h.request(http.MethodPost, "/api/setup/complete", map[string]any{
		"setupSecret":   h.secret,
		"displayName":   displayName,
		"email":         email,
		"password":      password,
		"workspaceName": wsName,
		"workspaceSlug": wsSlug,
	}, "", nil, true)
	if resp.status != http.StatusCreated {
		h.t.Fatalf("setup failed: status=%d body=%s", resp.status, resp.body)
	}
	var session apigen.AuthSessionResponse
	resp.decode(h.t, &session)
	return session, resp.cookies
}

func (h *testHarness) inviteAndAcceptNewUser(ownerToken string, wsID uuid.UUID, email, role, displayName, password string) (apigen.AuthSessionResponse, []*http.Cookie) {
	h.t.Helper()
	createResp := h.request(http.MethodPost, "/api/workspaces/"+wsID.String()+"/invitations", map[string]any{
		"email": email,
		"role":  role,
	}, ownerToken, nil, true)
	if createResp.status != http.StatusCreated {
		h.t.Fatalf("create invitation (%s %s): status=%d body=%s", email, role, createResp.status, createResp.body)
	}
	var created apigen.CreatedInvitationResponse
	createResp.decode(h.t, &created)

	acceptResp := h.request(http.MethodPost, "/api/invitations/"+created.Token+"/accept", map[string]any{
		"email":       email,
		"displayName": displayName,
		"password":    password,
	}, "", nil, true)
	if acceptResp.status != http.StatusOK {
		h.t.Fatalf("accept invitation (%s): status=%d body=%s", email, acceptResp.status, acceptResp.body)
	}
	var session apigen.AuthSessionResponse
	acceptResp.decode(h.t, &session)
	return session, acceptResp.cookies
}

func TestSetupWizardLifecycleAndLockout(t *testing.T) {
	h := newTestHarness(t, nil, harnessOpts{
		setupLimit: 2,
		// #nosec G101 -- Test setup secret fixture, not a real credential.
		envSetupSecret: "bbx-setup-custom-test-secret",
	})

	// 1. Setup is active when database has zero users.
	statusResp := h.request(http.MethodGet, "/api/setup/status", nil, "", nil, false)
	if statusResp.status != http.StatusOK {
		t.Fatalf("GET /api/setup/status = %d, want 200 (%s)", statusResp.status, statusResp.body)
	}
	var statusBody apigen.SetupStatusResponse
	statusResp.decode(t, &statusBody)
	if !statusBody.SetupRequired {
		t.Fatalf("SetupRequired = false, want true")
	}

	// 2. Env-provided secret must never be printed to stdout or structured logs.
	if h.setupOut.Len() != 0 {
		t.Fatalf("expected empty stdout when env setup secret is configured, got %q", h.setupOut.String())
	}

	// 3. Missing CSRF header is rejected before processing setup.
	csrfResp := h.request(http.MethodPost, "/api/setup/complete", map[string]any{
		"setupSecret": h.secret,
	}, "", nil, false)
	if csrfResp.status != http.StatusForbidden || csrfResp.errorCode(t) != "auth.csrf_required" {
		t.Fatalf("missing CSRF = (%d %s), want (403 auth.csrf_required)", csrfResp.status, csrfResp.body)
	}

	// 4. Invalid setup secret returns 403 and consumes a rate-limit slot.
	badSecretResp := h.request(http.MethodPost, "/api/setup/complete", map[string]any{
		"setupSecret":   "wrong-secret",
		"displayName":   "Ada",
		"email":         "ada@example.com",
		"password":      "correct horse battery staple",
		"workspaceName": "Acme Lab",
		"workspaceSlug": "acme-lab",
	}, "", nil, true)
	if badSecretResp.status != http.StatusForbidden || badSecretResp.errorCode(t) != "setup.invalid_secret" {
		t.Fatalf("wrong secret = (%d %s), want (403 setup.invalid_secret)", badSecretResp.status, badSecretResp.body)
	}

	// 5. Validation errors (short password, invalid slug) return 400.
	badInputResp := h.request(http.MethodPost, "/api/setup/complete", map[string]any{
		"setupSecret":   h.secret,
		"displayName":   "Ada",
		"email":         "ada@example.com",
		"password":      "short",
		"workspaceName": "Acme Lab",
		"workspaceSlug": "INVALID SLUG!",
	}, "", nil, true)
	if badInputResp.status != http.StatusBadRequest {
		t.Fatalf("bad input status = %d, want 400 (%s)", badInputResp.status, badInputResp.body)
	}

	// 6. Rate limiter trips on the 3rd attempt (limit=2) and recovers deterministically after window.
	rlResp := h.request(http.MethodPost, "/api/setup/complete", map[string]any{
		"setupSecret": h.secret,
	}, "", nil, true)
	if rlResp.status != http.StatusTooManyRequests || rlResp.errorCode(t) != "rate_limited" {
		t.Fatalf("rate limit = (%d %s), want (429 rate_limited)", rlResp.status, rlResp.body)
	}
	h.clock.Advance(16 * time.Minute)

	// 7. Successful setup creates initial owner + workspace + HttpOnly refresh cookie.
	ownerPassword := "correct horse battery staple"
	session, cookies := h.completeSetup("Ada@Example.com", ownerPassword, "Ada Lovelace", "Acme Lab", "acme-lab")
	if session.User.Email != "ada@example.com" {
		t.Fatalf("normalized user email = %q, want ada@example.com", session.User.Email)
	}
	if len(session.Workspaces) != 1 || session.Workspaces[0].Role != apigen.Owner || session.ActiveWorkspace == nil {
		t.Fatalf("unexpected setup workspaces: %+v", session.Workspaces)
	}
	rtCookie := (apiResponse{cookies: cookies}).cookie("botbox_refresh")
	if rtCookie == nil || !rtCookie.HttpOnly || rtCookie.SameSite != http.SameSiteLaxMode || rtCookie.Path != "/api" {
		t.Fatalf("unexpected refresh cookie attributes: %+v", rtCookie)
	}

	// 8. Setup endpoints lock out permanently with 404 once complete.
	postStatus := h.request(http.MethodGet, "/api/setup/status", nil, "", nil, false)
	if postStatus.status != http.StatusNotFound {
		t.Fatalf("post-setup GET /api/setup/status = %d, want 404", postStatus.status)
	}
	postComplete := h.request(http.MethodPost, "/api/setup/complete", map[string]any{
		"setupSecret":   h.secret,
		"displayName":   "Attacker",
		"email":         "attacker@example.com",
		"password":      "another valid password 123",
		"workspaceName": "Evil",
		"workspaceSlug": "evil",
	}, "", nil, true)
	if postComplete.status != http.StatusNotFound {
		t.Fatalf("post-setup POST /api/setup/complete = %d, want 404", postComplete.status)
	}

	// 9. Restarting against populated DB keeps setup disabled with 404 and prints no secret.
	restarted := newTestHarness(t, h.pool, harnessOpts{})
	if restarted.setupMgr.Active() || restarted.setupOut.Len() != 0 {
		t.Fatalf("restarted server unexpectedly re-armed setup: active=%v out=%q", restarted.setupMgr.Active(), restarted.setupOut.String())
	}
	restartedStatus := restarted.request(http.MethodGet, "/api/setup/status", nil, "", nil, false)
	if restartedStatus.status != http.StatusNotFound {
		t.Fatalf("restarted GET /api/setup/status = %d, want 404", restartedStatus.status)
	}

	// 10. Verify security invariant: neither setup secret, raw password, nor hash leaked to logs or bodies.
	logText := h.logs.String()
	for _, secretVal := range []string{h.secret, ownerPassword, "$argon2id$", rtCookie.Value} {
		if strings.Contains(logText, secretVal) {
			t.Fatalf("sensitive value %q leaked into slog output: %s", secretVal, logText)
		}
	}
}

func TestSetupDependencyUnavailableReturns503(t *testing.T) {
	handler := api.NewHandlerWithConfig(api.Config{
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/setup/status", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	res := rec.Result()
	defer func() {
		_ = res.Body.Close()
	}()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when pool is nil", res.StatusCode)
	}
}

func TestAuthLoginUpgradeRefreshReuseFamilyRevocationAndLogout(t *testing.T) {
	h := newTestHarness(t, nil, harnessOpts{
		argon2Iterations: 1,
		loginIPLimit:     20,
		loginEmailLimit:  3,
	})
	password := "very strong passphrase!"
	session, _ := h.completeSetup("owner@example.com", password, "Owner", "Team One", "team-one")

	// 1. Unauthenticated /api/auth/me must return 401.
	meUnauth := h.request(http.MethodGet, "/api/auth/me", nil, "", nil, false)
	if meUnauth.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /api/auth/me = %d, want 401", meUnauth.status)
	}

	// 2. Wrong password returns 401 and consumes email rate limit.
	badLogin := h.request(http.MethodPost, "/api/auth/login", map[string]any{
		"email":    "owner@example.com",
		"password": "wrong-password-1234",
	}, "", nil, true)
	if badLogin.status != http.StatusUnauthorized || badLogin.errorCode(t) != "auth.invalid_credentials" {
		t.Fatalf("wrong password = (%d %s), want (401 auth.invalid_credentials)", badLogin.status, badLogin.body)
	}

	// 3. Successful login succeeds and sets fresh refresh token R1.
	loginResp := h.request(http.MethodPost, "/api/auth/login", map[string]any{
		"email":    "OWNER@example.com",
		"password": password,
	}, "", nil, true)
	if loginResp.status != http.StatusOK {
		t.Fatalf("login failed: status=%d body=%s", loginResp.status, loginResp.body)
	}
	var loginSession apigen.AuthSessionResponse
	loginResp.decode(h.t, &loginSession)
	r1 := loginResp.cookie("botbox_refresh")
	if r1 == nil || r1.Value == "" {
		t.Fatalf("expected botbox_refresh cookie on login")
	}

	// 4. Exhaust per-email login rate limit (limit=3; 2 used so far), verify 429, then advance clock.
	_ = h.request(http.MethodPost, "/api/auth/login", map[string]any{"email": "owner@example.com", "password": "wrong-password-1234"}, "", nil, true)
	rlLogin := h.request(http.MethodPost, "/api/auth/login", map[string]any{"email": "owner@example.com", "password": password}, "", nil, true)
	if rlLogin.status != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after exhausting email rate limit, got %d (%s)", rlLogin.status, rlLogin.body)
	}

	// 5. Transparent Argon2id parameter upgrade on login when target iterations bump from 1 to 2.
	h2 := newTestHarness(t, h.pool, harnessOpts{argon2Iterations: 2})
	upgradeResp := h2.request(http.MethodPost, "/api/auth/login", map[string]any{
		"email":    "owner@example.com",
		"password": password,
	}, "", nil, true)
	if upgradeResp.status != http.StatusOK {
		t.Fatalf("upgrade login failed: status=%d body=%s", upgradeResp.status, upgradeResp.body)
	}
	storedUser, err := h2.queries.GetUserByEmail(context.Background(), "owner@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if !strings.Contains(storedUser.PasswordHash, "m=1024,t=2,p=1") {
		t.Fatalf("expected upgraded Argon2id hash t=2, got %q", storedUser.PasswordHash)
	}

	// 6. Refresh token rotation: R1 -> R2 -> R3.
	ref1 := h.request(http.MethodPost, "/api/auth/refresh", nil, "", []*http.Cookie{r1}, true)
	if ref1.status != http.StatusOK {
		t.Fatalf("first refresh failed: status=%d body=%s", ref1.status, ref1.body)
	}
	r2 := ref1.cookie("botbox_refresh")
	if r2 == nil || r2.Value == "" || r2.Value == r1.Value {
		t.Fatalf("expected rotated R2 cookie distinct from R1")
	}

	ref2 := h.request(http.MethodPost, "/api/auth/refresh", nil, "", []*http.Cookie{r2}, true)
	if ref2.status != http.StatusOK {
		t.Fatalf("second refresh failed: status=%d body=%s", ref2.status, ref2.body)
	}
	r3 := ref2.cookie("botbox_refresh")
	if r3 == nil || r3.Value == "" || r3.Value == r2.Value {
		t.Fatalf("expected rotated R3 cookie distinct from R2")
	}

	// 7. Replaying already-used R1 revokes the entire token family, killing R3 as well.
	replay := h.request(http.MethodPost, "/api/auth/refresh", nil, "", []*http.Cookie{r1}, true)
	if replay.status != http.StatusUnauthorized || replay.errorCode(t) != "auth.refresh_token_reused" {
		t.Fatalf("replay R1 = (%d %s), want (401 auth.refresh_token_reused)", replay.status, replay.body)
	}
	afterRevoke := h.request(http.MethodPost, "/api/auth/refresh", nil, "", []*http.Cookie{r3}, true)
	if afterRevoke.status != http.StatusUnauthorized || afterRevoke.errorCode(t) != "auth.refresh_token_reused" {
		t.Fatalf("R3 after family revocation = (%d %s), want (401 auth.refresh_token_reused)", afterRevoke.status, afterRevoke.body)
	}

	// 8. Access token expiration via deterministic clock advance.
	h.clock.Advance(16 * time.Minute)
	expiredMe := h.request(http.MethodGet, "/api/auth/me", nil, session.AccessToken, nil, false)
	if expiredMe.status != http.StatusUnauthorized || expiredMe.errorCode(t) != "auth.token_expired" {
		t.Fatalf("expired token /api/auth/me = (%d %s), want (401 auth.token_expired)", expiredMe.status, expiredMe.body)
	}

	// 9. Logout clears cookie and revokes token.
	loginAgain := h2.request(http.MethodPost, "/api/auth/login", map[string]any{
		"email":    "owner@example.com",
		"password": password,
	}, "", nil, true)
	if loginAgain.status != http.StatusOK {
		t.Fatalf("re-login failed: %d %s", loginAgain.status, loginAgain.body)
	}
	rFresh := loginAgain.cookie("botbox_refresh")
	logoutResp := h2.request(http.MethodPost, "/api/auth/logout", nil, "", []*http.Cookie{rFresh}, true)
	if logoutResp.status != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", logoutResp.status)
	}
	cleared := logoutResp.cookie("botbox_refresh")
	if cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("expected MaxAge < 0 cookie clear on logout, got %+v", cleared)
	}
	afterLogout := h2.request(http.MethodPost, "/api/auth/refresh", nil, "", []*http.Cookie{rFresh}, true)
	if afterLogout.status != http.StatusUnauthorized {
		t.Fatalf("refresh after logout = %d, want 401", afterLogout.status)
	}

	// 10. Refresh token expiration via deterministic clock advance (> 30d).
	loginForExp := h2.request(http.MethodPost, "/api/auth/login", map[string]any{
		"email":    "owner@example.com",
		"password": password,
	}, "", nil, true)
	rExp := loginForExp.cookie("botbox_refresh")
	h2.clock.Advance(31 * 24 * time.Hour)
	expResp := h2.request(http.MethodPost, "/api/auth/refresh", nil, "", []*http.Cookie{rExp}, true)
	if expResp.status != http.StatusUnauthorized || expResp.errorCode(t) != "auth.refresh_token_expired" {
		t.Fatalf("expired refresh = (%d %s), want (401 auth.refresh_token_expired)", expResp.status, expResp.body)
	}
}

func TestInvitationsLifecycleAndEdgeCases(t *testing.T) {
	h := newTestHarness(t, nil, harnessOpts{})
	ownerSess, _ := h.completeSetup("owner@example.com", "correct horse battery staple", "Owner", "Alpha", "alpha")
	wsAlpha := ownerSess.Workspaces[0].Id

	// Unknown invitation returns 404.
	if resp := h.request(http.MethodGet, "/api/invitations/unknown-token", nil, "", nil, false); resp.status != http.StatusNotFound {
		t.Fatalf("unknown invite preview = %d, want 404", resp.status)
	}

	// Create email-targeted invitation and verify DB stores hash, not plaintext token.
	createResp := h.request(http.MethodPost, "/api/workspaces/"+wsAlpha.String()+"/invitations", map[string]any{
		"email": "Bob@Example.com",
		"role":  "member",
	}, ownerSess.AccessToken, nil, true)
	if createResp.status != http.StatusCreated {
		t.Fatalf("create invitation = %d %s", createResp.status, createResp.body)
	}
	var created apigen.CreatedInvitationResponse
	createResp.decode(h.t, &created)
	if !strings.HasPrefix(created.Token, "bbx_inv_") || !strings.HasSuffix(created.InviteUrl, "/invite/"+created.Token) {
		t.Fatalf("unexpected invitation token/url: %+v", created)
	}
	invRow, err := h.queries.GetInvitationByTokenHash(context.Background(), auth.HashToken(created.Token))
	if err != nil || bytes.Equal(invRow.TokenHash, []byte(created.Token)) {
		t.Fatalf("expected hashed invitation token in DB, err=%v", err)
	}

	// Email mismatch rejected with 403.
	mismatchResp := h.request(http.MethodPost, "/api/invitations/"+created.Token+"/accept", map[string]any{
		"email":       "eve@example.com",
		"displayName": "Eve",
		"password":    "another safe password 123",
	}, "", nil, true)
	if mismatchResp.status != http.StatusForbidden || mismatchResp.errorCode(t) != "invitation.email_mismatch" {
		t.Fatalf("email mismatch = (%d %s), want (403 invitation.email_mismatch)", mismatchResp.status, mismatchResp.body)
	}

	// Unauthenticated accept using already-registered email returns 409 auth.account_exists.
	openInviteResp := h.request(http.MethodPost, "/api/workspaces/"+wsAlpha.String()+"/invitations", map[string]any{
		"role": "viewer",
	}, ownerSess.AccessToken, nil, true)
	var openCreated apigen.CreatedInvitationResponse
	openInviteResp.decode(h.t, &openCreated)
	existingEmailResp := h.request(http.MethodPost, "/api/invitations/"+openCreated.Token+"/accept", map[string]any{
		"email":       "owner@example.com",
		"displayName": "Impostor",
		"password":    "some valid password 1234",
	}, "", nil, true)
	if existingEmailResp.status != http.StatusConflict || existingEmailResp.errorCode(t) != "auth.account_exists" {
		t.Fatalf("existing account unauthenticated accept = (%d %s), want (409 auth.account_exists)", existingEmailResp.status, existingEmailResp.body)
	}

	// Preview works before acceptance, Bob accepts, then single-use token returns 404.
	prevResp := h.request(http.MethodGet, "/api/invitations/"+created.Token, nil, "", nil, false)
	if prevResp.status != http.StatusOK {
		t.Fatalf("preview = %d %s", prevResp.status, prevResp.body)
	}
	acceptResp := h.request(http.MethodPost, "/api/invitations/"+created.Token+"/accept", map[string]any{
		"email":       "bob@example.com",
		"displayName": "Bob",
		"password":    "bobs secure passphrase!",
	}, "", nil, true)
	if acceptResp.status != http.StatusOK {
		t.Fatalf("bob accept = %d %s", acceptResp.status, acceptResp.body)
	}
	var bobSess apigen.AuthSessionResponse
	acceptResp.decode(h.t, &bobSess)
	if len(bobSess.Workspaces) != 1 || bobSess.Workspaces[0].Role != apigen.Member {
		t.Fatalf("unexpected bob workspaces: %+v", bobSess.Workspaces)
	}

	if rePreview := h.request(http.MethodGet, "/api/invitations/"+created.Token, nil, "", nil, false); rePreview.status != http.StatusNotFound {
		t.Fatalf("re-preview used token = %d, want 404", rePreview.status)
	}
	if reAccept := h.request(http.MethodPost, "/api/invitations/"+created.Token+"/accept", map[string]any{
		"email":       "bob@example.com",
		"displayName": "Bob2",
		"password":    "bobs secure passphrase!",
	}, "", nil, true); reAccept.status != http.StatusNotFound {
		t.Fatalf("re-accept used token = %d, want 404", reAccept.status)
	}

	// Owner creates second workspace Beta and invites Bob; signed-in Bob accepts into Beta.
	wsBetaResp := h.request(http.MethodPost, "/api/workspaces", map[string]any{
		"name": "Beta",
		"slug": "beta",
	}, ownerSess.AccessToken, nil, true)
	if wsBetaResp.status != http.StatusCreated {
		t.Fatalf("create Beta = %d %s", wsBetaResp.status, wsBetaResp.body)
	}
	var wsBeta apigen.WorkspaceSummary
	wsBetaResp.decode(h.t, &wsBeta)

	betaInviteResp := h.request(http.MethodPost, "/api/workspaces/"+wsBeta.Id.String()+"/invitations", map[string]any{
		"email": "bob@example.com",
		"role":  "viewer",
	}, ownerSess.AccessToken, nil, true)
	var betaInvite apigen.CreatedInvitationResponse
	betaInviteResp.decode(h.t, &betaInvite)

	bobBetaAccept := h.request(http.MethodPost, "/api/invitations/"+betaInvite.Token+"/accept", nil, bobSess.AccessToken, nil, true)
	if bobBetaAccept.status != http.StatusOK {
		t.Fatalf("signed-in accept Beta = %d %s", bobBetaAccept.status, bobBetaAccept.body)
	}
	var bobMulti apigen.AuthSessionResponse
	bobBetaAccept.decode(h.t, &bobMulti)
	if len(bobMulti.Workspaces) != 2 {
		t.Fatalf("expected Bob to belong to 2 workspaces, got %d", len(bobMulti.Workspaces))
	}

	// Expired invitation returns 404 on preview and accept.
	h.clock.Advance(8 * 24 * time.Hour)
	expiredPrev := h.request(http.MethodGet, "/api/invitations/"+openCreated.Token, nil, "", nil, false)
	if expiredPrev.status != http.StatusNotFound {
		t.Fatalf("expired invite preview = %d, want 404", expiredPrev.status)
	}

	// Mint a fresh JWT after advancing clock so owner can revoke the unaccepted invite cleanly.
	freshOwnerToken, err := h.queries.GetUserByEmail(context.Background(), "owner@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	jwtSvc, err := auth.NewJWTService(h.jwtKey, auth.DefaultAccessTokenTTL, h.clock.Now)
	if err != nil {
		t.Fatalf("NewJWTService: %v", err)
	}
	freshToken, err := jwtSvc.IssueAccessToken(auth.User{
		ID:          uuid.UUID(freshOwnerToken.ID.Bytes),
		Email:       freshOwnerToken.Email,
		DisplayName: freshOwnerToken.DisplayName,
		Locale:      freshOwnerToken.Locale,
	})
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	revokeResp := h.request(http.MethodDelete, "/api/workspaces/"+wsAlpha.String()+"/invitations/"+openCreated.Invitation.Id.String(), nil, freshToken, nil, true)
	if revokeResp.status != http.StatusNoContent {
		t.Fatalf("revoke invitation = %d %s, want 204", revokeResp.status, revokeResp.body)
	}
}

func TestRBACMatrixAndCrossWorkspaceIsolation(t *testing.T) {
	h := newTestHarness(t, nil, harnessOpts{})

	ownerASess, _ := h.completeSetup("ownera@example.com", "correct horse battery staple", "Owner A", "Workspace A", "ws-a")
	wsA := ownerASess.Workspaces[0].Id

	// Owner A invites Admin A, Member A1, Member A2, Viewer A into Workspace A.
	adminASess, _ := h.inviteAndAcceptNewUser(ownerASess.AccessToken, wsA, "admina@example.com", "admin", "Admin A", "correct horse battery staple")
	member1Sess, _ := h.inviteAndAcceptNewUser(ownerASess.AccessToken, wsA, "member1@example.com", "member", "Member A1", "correct horse battery staple")
	member2Sess, _ := h.inviteAndAcceptNewUser(ownerASess.AccessToken, wsA, "member2@example.com", "member", "Member A2", "correct horse battery staple")
	viewerASess, _ := h.inviteAndAcceptNewUser(ownerASess.AccessToken, wsA, "viewera@example.com", "viewer", "Viewer A", "correct horse battery staple")

	// Separate user creates Workspace B via invitation from a temporary workspace or create a second independent owner via DB+invite,
	// or create Workspace B directly via DB + membership for complete multi-tenant independence.
	wsBResp := h.request(http.MethodPost, "/api/workspaces", map[string]any{
		"name": "Workspace B",
		"slug": "ws-b",
	}, ownerASess.AccessToken, nil, true)
	if wsBResp.status != http.StatusCreated {
		t.Fatalf("create wsB = %d %s", wsBResp.status, wsBResp.body)
	}
	var wsB apigen.WorkspaceSummary
	wsBResp.decode(h.t, &wsB)
	// Invite Owner B into Workspace B, then remove Owner A from Workspace B so Workspace B is strictly isolated from Owner A.
	ownerBSess, _ := h.inviteAndAcceptNewUser(ownerASess.AccessToken, wsB.Id, "ownerb@example.com", "owner", "Owner B", "correct horse battery staple")
	remOwnerA := h.request(http.MethodDelete, "/api/workspaces/"+wsB.Id.String()+"/members/"+ownerASess.User.Id.String(), nil, ownerBSess.AccessToken, nil, true)
	if remOwnerA.status != http.StatusNoContent {
		t.Fatalf("remove Owner A from Workspace B = %d %s", remOwnerA.status, remOwnerA.body)
	}

	// Owner B creates Bot B inside Workspace B and Invitation B inside Workspace B.
	botBResp := h.request(http.MethodPost, "/api/workspaces/"+wsB.Id.String()+"/bots", map[string]any{
		"name":        "Secret B Bot",
		"avatarSeed":  "secret-b-bot",
		"llmMode":     "cli",
		"llmProvider": "anthropic",
		"llmModel":    "claude-sonnet-4-5",
	}, ownerBSess.AccessToken, nil, true)
	if botBResp.status != http.StatusCreated {
		t.Fatalf("create Bot B = %d %s", botBResp.status, botBResp.body)
	}
	var botB apigen.Bot
	botBResp.decode(h.t, &botB)

	invBResp := h.request(http.MethodPost, "/api/workspaces/"+wsB.Id.String()+"/invitations", map[string]any{
		"role": "member",
	}, ownerBSess.AccessToken, nil, true)
	if invBResp.status != http.StatusCreated {
		t.Fatalf("create Invite B = %d %s", invBResp.status, invBResp.body)
	}
	var invB apigen.CreatedInvitationResponse
	invBResp.decode(h.t, &invB)

	// Member A1 creates Bot A1 inside Workspace A; invalid avatar seed returns 400.
	badBotResp := h.request(http.MethodPost, "/api/workspaces/"+wsA.String()+"/bots", map[string]any{
		"name":        "Bad Seed",
		"avatarSeed":  "UPPERCASE INVALID!",
		"llmMode":     "cli",
		"llmProvider": "anthropic",
		"llmModel":    "claude-sonnet-4-5",
	}, member1Sess.AccessToken, nil, true)
	if badBotResp.status != http.StatusBadRequest {
		t.Fatalf("bad avatar seed = %d %s, want 400", badBotResp.status, badBotResp.body)
	}

	botA1Resp := h.request(http.MethodPost, "/api/workspaces/"+wsA.String()+"/bots", map[string]any{
		"name":        "Bot A1",
		"avatarSeed":  "research-owl-01",
		"llmMode":     "cli",
		"llmProvider": "anthropic",
		"llmModel":    "claude-sonnet-4-5",
	}, member1Sess.AccessToken, nil, true)
	if botA1Resp.status != http.StatusCreated {
		t.Fatalf("create Bot A1 = %d %s", botA1Resp.status, botA1Resp.body)
	}
	var botA1 apigen.Bot
	botA1Resp.decode(h.t, &botA1)

	// Member A2 creates Bot A2 (testing owner vs non-owner member bot permissions).
	botA2Resp := h.request(http.MethodPost, "/api/workspaces/"+wsA.String()+"/bots", map[string]any{
		"name":        "Bot A2",
		"avatarSeed":  "ops-fox-02",
		"llmMode":     "api",
		"llmProvider": "openai",
		"llmModel":    "gpt-4o",
	}, member2Sess.AccessToken, nil, true)
	if botA2Resp.status != http.StatusCreated {
		t.Fatalf("create Bot A2 = %d %s", botA2Resp.status, botA2Resp.body)
	}
	var botA2 apigen.Bot
	botA2Resp.decode(h.t, &botA2)

	// Table-driven RBAC & cross-workspace matrix.
	cases := []struct {
		name       string
		method     string
		path       string
		body       any
		token      string
		wantStatus int
	}{
		// Unauthenticated -> 401
		{name: "unauth list bots", method: http.MethodGet, path: "/api/workspaces/" + wsA.String() + "/bots", token: "", wantStatus: http.StatusUnauthorized},
		{name: "unauth create bot", method: http.MethodPost, path: "/api/workspaces/" + wsA.String() + "/bots", body: map[string]any{"name": "x"}, token: "", wantStatus: http.StatusUnauthorized},
		{name: "unauth update member", method: http.MethodPatch, path: "/api/workspaces/" + wsA.String() + "/members/" + viewerASess.User.Id.String(), body: map[string]any{"role": "member"}, token: "", wantStatus: http.StatusUnauthorized},

		// Cross-workspace access MUST return 404 (never 403 or 200)
		{name: "cross-ws get workspace", method: http.MethodGet, path: "/api/workspaces/" + wsB.Id.String(), token: ownerASess.AccessToken, wantStatus: http.StatusNotFound},
		{name: "cross-ws list members", method: http.MethodGet, path: "/api/workspaces/" + wsB.Id.String() + "/members", token: ownerASess.AccessToken, wantStatus: http.StatusNotFound},
		{name: "cross-ws list invitations", method: http.MethodGet, path: "/api/workspaces/" + wsB.Id.String() + "/invitations", token: ownerASess.AccessToken, wantStatus: http.StatusNotFound},
		{name: "cross-ws list bots", method: http.MethodGet, path: "/api/workspaces/" + wsB.Id.String() + "/bots", token: ownerASess.AccessToken, wantStatus: http.StatusNotFound},
		{name: "cross-ws get bot via wsB", method: http.MethodGet, path: "/api/workspaces/" + wsB.Id.String() + "/bots/" + botB.Id.String(), token: ownerASess.AccessToken, wantStatus: http.StatusNotFound},
		{name: "cross-ws get botB via wsA", method: http.MethodGet, path: "/api/workspaces/" + wsA.String() + "/bots/" + botB.Id.String(), token: ownerASess.AccessToken, wantStatus: http.StatusNotFound},
		{name: "cross-ws delete botB via wsA", method: http.MethodDelete, path: "/api/workspaces/" + wsA.String() + "/bots/" + botB.Id.String(), token: ownerASess.AccessToken, wantStatus: http.StatusNotFound},
		{name: "cross-ws revoke inviteB via wsA", method: http.MethodDelete, path: "/api/workspaces/" + wsA.String() + "/invitations/" + invB.Invitation.Id.String(), token: ownerASess.AccessToken, wantStatus: http.StatusNotFound},

		// Viewer permissions
		{name: "viewer reads workspace", method: http.MethodGet, path: "/api/workspaces/" + wsA.String(), token: viewerASess.AccessToken, wantStatus: http.StatusOK},
		{name: "viewer reads bots", method: http.MethodGet, path: "/api/workspaces/" + wsA.String() + "/bots", token: viewerASess.AccessToken, wantStatus: http.StatusOK},
		{name: "viewer cannot create bot", method: http.MethodPost, path: "/api/workspaces/" + wsA.String() + "/bots", body: map[string]any{"name": "v", "avatarSeed": "v-seed", "llmMode": "cli", "llmProvider": "anthropic", "llmModel": "claude"}, token: viewerASess.AccessToken, wantStatus: http.StatusForbidden},
		{name: "viewer cannot update bot", method: http.MethodPatch, path: "/api/workspaces/" + wsA.String() + "/bots/" + botA1.Id.String(), body: map[string]any{"name": "hijack"}, token: viewerASess.AccessToken, wantStatus: http.StatusForbidden},
		{name: "viewer cannot list invitations", method: http.MethodGet, path: "/api/workspaces/" + wsA.String() + "/invitations", token: viewerASess.AccessToken, wantStatus: http.StatusForbidden},
		{name: "viewer cannot change member role", method: http.MethodPatch, path: "/api/workspaces/" + wsA.String() + "/members/" + member1Sess.User.Id.String(), body: map[string]any{"role": "viewer"}, token: viewerASess.AccessToken, wantStatus: http.StatusForbidden},

		// Member permissions (own bot vs others' bots)
		{name: "member1 updates own bot", method: http.MethodPatch, path: "/api/workspaces/" + wsA.String() + "/bots/" + botA1.Id.String(), body: map[string]any{"description": "updated by owner"}, token: member1Sess.AccessToken, wantStatus: http.StatusOK},
		{name: "member2 cannot update member1 bot", method: http.MethodPatch, path: "/api/workspaces/" + wsA.String() + "/bots/" + botA1.Id.String(), body: map[string]any{"description": "hijacked"}, token: member2Sess.AccessToken, wantStatus: http.StatusForbidden},
		{name: "member2 cannot delete member1 bot", method: http.MethodDelete, path: "/api/workspaces/" + wsA.String() + "/bots/" + botA1.Id.String(), token: member2Sess.AccessToken, wantStatus: http.StatusForbidden},
		{name: "member1 cannot create invitation", method: http.MethodPost, path: "/api/workspaces/" + wsA.String() + "/invitations", body: map[string]any{"role": "viewer"}, token: member1Sess.AccessToken, wantStatus: http.StatusForbidden},
		{name: "member1 cannot remove member2", method: http.MethodDelete, path: "/api/workspaces/" + wsA.String() + "/members/" + member2Sess.User.Id.String(), token: member1Sess.AccessToken, wantStatus: http.StatusForbidden},

		// Admin permissions (can manage members/invitations up to member, any bot, NOT owner/admin elevation)
		{name: "admin lists invitations", method: http.MethodGet, path: "/api/workspaces/" + wsA.String() + "/invitations", token: adminASess.AccessToken, wantStatus: http.StatusOK},
		{name: "admin cannot invite admin", method: http.MethodPost, path: "/api/workspaces/" + wsA.String() + "/invitations", body: map[string]any{"role": "admin"}, token: adminASess.AccessToken, wantStatus: http.StatusForbidden},
		{name: "admin cannot invite owner", method: http.MethodPost, path: "/api/workspaces/" + wsA.String() + "/invitations", body: map[string]any{"role": "owner"}, token: adminASess.AccessToken, wantStatus: http.StatusForbidden},
		{name: "admin invites member", method: http.MethodPost, path: "/api/workspaces/" + wsA.String() + "/invitations", body: map[string]any{"role": "member"}, token: adminASess.AccessToken, wantStatus: http.StatusCreated},
		{name: "admin updates member1 bot", method: http.MethodPatch, path: "/api/workspaces/" + wsA.String() + "/bots/" + botA1.Id.String(), body: map[string]any{"description": "updated by admin"}, token: adminASess.AccessToken, wantStatus: http.StatusOK},
		{name: "admin cannot demote owner", method: http.MethodPatch, path: "/api/workspaces/" + wsA.String() + "/members/" + ownerASess.User.Id.String(), body: map[string]any{"role": "member"}, token: adminASess.AccessToken, wantStatus: http.StatusForbidden},
		{name: "admin cannot promote viewer to admin", method: http.MethodPatch, path: "/api/workspaces/" + wsA.String() + "/members/" + viewerASess.User.Id.String(), body: map[string]any{"role": "admin"}, token: adminASess.AccessToken, wantStatus: http.StatusForbidden},
		{name: "admin promotes viewer to member", method: http.MethodPatch, path: "/api/workspaces/" + wsA.String() + "/members/" + viewerASess.User.Id.String(), body: map[string]any{"role": "member"}, token: adminASess.AccessToken, wantStatus: http.StatusOK},

		// Owner permissions & last-owner safeguard
		{name: "owner cannot demote last owner", method: http.MethodPatch, path: "/api/workspaces/" + wsA.String() + "/members/" + ownerASess.User.Id.String(), body: map[string]any{"role": "admin"}, token: ownerASess.AccessToken, wantStatus: http.StatusBadRequest},
		{name: "owner cannot remove last owner", method: http.MethodDelete, path: "/api/workspaces/" + wsA.String() + "/members/" + ownerASess.User.Id.String(), token: ownerASess.AccessToken, wantStatus: http.StatusBadRequest},
		{name: "owner deletes member2 bot", method: http.MethodDelete, path: "/api/workspaces/" + wsA.String() + "/bots/" + botA2.Id.String(), token: ownerASess.AccessToken, wantStatus: http.StatusNoContent},
		{name: "owner removes member2", method: http.MethodDelete, path: "/api/workspaces/" + wsA.String() + "/members/" + member2Sess.User.Id.String(), token: ownerASess.AccessToken, wantStatus: http.StatusNoContent},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.request(tc.method, tc.path, tc.body, tc.token, nil, true)
			if resp.status != tc.wantStatus {
				t.Fatalf("%s %s = %d %s, want %d", tc.method, tc.path, resp.status, resp.body, tc.wantStatus)
			}
			// Verify no password hashes or secret fields ever appear in responses.
			if bytes.Contains(resp.body, []byte("$argon2id$")) || bytes.Contains(resp.body, []byte("password_hash")) || bytes.Contains(resp.body, []byte("passwordHash")) {
				t.Fatalf("password hash field leaked in response: %s", resp.body)
			}
		})
	}

	// Verify audit trail recorded inside Workspace A.
	auditRows, err := h.queries.ListAuditLogsByWorkspace(context.Background(), db.ListAuditLogsByWorkspaceParams{
		WorkspaceID: pgtype.UUID{Bytes: wsA, Valid: true},
		Limit:       20,
	})
	if err != nil {
		t.Fatalf("ListAuditLogsByWorkspace: %v", err)
	}
	if len(auditRows) < 5 {
		t.Fatalf("expected audit log entries in workspace A, got %d", len(auditRows))
	}
}
