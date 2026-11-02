// Package codexplan manages ChatGPT plan authorization in the control plane.
package codexplan

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	issuer   = "https://auth.openai.com"
	resource = "https://api.openai.com/v1"
	scopes   = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
	// DynamicClient registers a new user-authorized application.
	DynamicClient = "dynamic_agent_client"
	// CallbackPath is the fixed path used with an IPv4 loopback redirect.
	CallbackPath = "/auth/callback"
)

// ErrReconnect indicates an unusable or revoked authorization.
var ErrReconnect = errors.New("ChatGPT plan requires reconnection")

// Tokens contains only the material needed for plan inference and renewal.
type Tokens struct {
	Access  string    `json:"access"`
	Refresh string    `json:"refresh"`
	Expires time.Time `json:"expires"`
}

// Grant contains the OAuth callback and its proof of authorization.
type Grant struct {
	Code     string `json:"code"`
	ClientID string `json:"clientId"`
	Verifier string `json:"verifier"`
	Nonce    string `json:"nonce"`
	Redirect string `json:"redirect"`
}

// OAuth exchanges codes and refreshes credentials at fixed OpenAI endpoints.
type OAuth struct {
	client *http.Client
	now    func() time.Time
}

// NewOAuth creates a bounded client. Transport can be replaced by a fixture in tests.
func NewOAuth(transport http.RoundTripper, now func() time.Time) *OAuth {
	if now == nil {
		now = time.Now
	}
	return &OAuth{client: &http.Client{Transport: transport, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}, now: now}
}

// AuthorizationURL starts the documented direct ChatGPT plan flow.
func AuthorizationURL(clientID, hostID, redirect, state, nonce, challenge string) string {
	values := url.Values{"client_id": {clientID}, "ext_agent_host_id": {hostID},
		"response_type": {"code"}, "redirect_uri": {redirect}, "scope": {scopes}, "resource": {resource},
		"state": {state}, "nonce": {nonce}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	if clientID == DynamicClient {
		values.Set("agent_name_hint", "bot.Box")
	}
	return issuer + "/api/accounts/authorize?" + values.Encode()
}

// ValidClientID accepts the registered client identifier returned by OpenAI.
func ValidClientID(id string) bool {
	return strings.HasPrefix(id, "oaiapp_") && len(id) > len("oaiapp_") && validOpaque(id, 200)
}

func validOpaque(value string, maxLen int) bool {
	return value != "" && len(value) <= maxLen && strings.IndexFunc(value, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', strings.ContainsRune("-._~", r):
			return false
		default:
			return true
		}
	}) == -1
}

// Valid checks callback values before bot.Box consumes the pairing.
func (g Grant) Valid() bool {
	u, err := url.Parse(g.Redirect)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Path != CallbackPath ||
		u.RawPath != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return false
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	return err == nil && port > 0 && len(g.Code) <= 4096 && g.Code != "" &&
		ValidClientID(g.ClientID) && len(g.Verifier) >= 43 && validOpaque(g.Verifier, 128) &&
		len(g.Nonce) >= 32 && validOpaque(g.Nonce, 128)
}

type tokenResponse struct {
	Access    string `json:"access_token"`
	Refresh   string `json:"refresh_token"`
	IDToken   string `json:"id_token"`
	Scope     string `json:"scope"`
	TokenType string `json:"token_type"`
	ExpiresIn int64  `json:"expires_in"`
	Error     string `json:"error"`
}

func (o *OAuth) token(ctx context.Context, values url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer+"/api/accounts/oauth/token", strings.NewReader(values.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.client.Do(req)
	if err != nil {
		return tokenResponse{}, errors.New("ChatGPT token service unavailable")
	}
	defer func() { _ = resp.Body.Close() }()
	var result tokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&result); err != nil {
		return tokenResponse{}, errors.New("invalid ChatGPT token response")
	}
	if resp.StatusCode != http.StatusOK {
		switch result.Error {
		case "invalid_grant", "invalid_refresh_token", "token_expired", "refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused":
			if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
				return tokenResponse{}, ErrReconnect
			}
		}
		return tokenResponse{}, errors.New("ChatGPT token service rejected the request")
	}
	if !validOpaque(result.Access, 16<<10) || !strings.EqualFold(result.TokenType, "Bearer") || result.ExpiresIn < 1 || result.ExpiresIn > 31*24*3600 {
		return tokenResponse{}, errors.New("invalid ChatGPT token response")
	}
	return result, nil
}

func hasPlanScope(scope string) bool {
	required := map[string]bool{"resource.invoke": false, "chatgpt.tokens.use.direct": false}
	for value := range strings.FieldsSeq(scope) {
		if _, ok := required[value]; ok {
			required[value] = true
		}
	}
	return required["resource.invoke"] && required["chatgpt.tokens.use.direct"]
}

// Exchange redeems the code and verifies the OpenID identity and plan scope.
func (o *OAuth) Exchange(ctx context.Context, grant Grant) (Tokens, string, error) {
	result, err := o.token(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {grant.Code},
		"client_id": {grant.ClientID}, "code_verifier": {grant.Verifier}, "redirect_uri": {grant.Redirect}, "resource": {resource}})
	if err != nil {
		return Tokens{}, "", err
	}
	if result.Refresh == "" || !hasPlanScope(result.Scope) {
		return Tokens{}, "", ErrReconnect
	}
	subject, err := o.identity(ctx, result.IDToken, grant.ClientID, grant.Nonce)
	if err != nil {
		return Tokens{}, "", err
	}
	return o.tokens(result, result.Refresh), subject, nil
}

// Refresh renews a selected registration while retaining its granted scopes.
func (o *OAuth) Refresh(ctx context.Context, clientID, refresh string) (Tokens, error) {
	result, err := o.token(ctx, url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {refresh}, "resource": {resource}})
	if err != nil {
		return Tokens{}, err
	}
	if result.Scope != "" && !hasPlanScope(result.Scope) {
		return Tokens{}, ErrReconnect
	}
	if result.Refresh != "" {
		refresh = result.Refresh
	}
	return o.tokens(result, refresh), nil
}

func (o *OAuth) tokens(result tokenResponse, refresh string) Tokens {
	return Tokens{Access: result.Access, Refresh: refresh, Expires: o.now().Add(time.Duration(result.ExpiresIn) * time.Second)}
}

// Revoke ends renewable access for this registration.
func (o *OAuth) Revoke(ctx context.Context, clientID, refresh string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer+"/api/accounts/oauth/revoke", strings.NewReader(url.Values{
		"client_id": {clientID}, "token": {refresh}, "token_type_hint": {"refresh_token"},
	}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.client.Do(req)
	if err != nil {
		return errors.New("ChatGPT revocation unavailable")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return errors.New("ChatGPT revocation unavailable")
	}
	return nil
}
