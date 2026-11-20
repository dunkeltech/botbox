// SPDX-License-Identifier: AGPL-3.0-only

package secrets

import (
	"encoding/json"
	"errors"
	"time"
)

const (
	// SecretAuthKindPlain marks standard static secret values.
	SecretAuthKindPlain = ""
	// SecretAuthKindOpenAIPlan marks a bot/workspace secret storing ChatGPT plan OAuth tokens.
	SecretAuthKindOpenAIPlan = "openai_plan"
	// SecretAuthKindOpenAIPlanReconnect marks a ChatGPT plan secret whose refresh failed and requires reconnection.
	//nolint:gosec // auth kind identifier constant, not a credential
	SecretAuthKindOpenAIPlanReconnect = "openai_plan_reconnect"
)

// OpenAIPlanTokens stores ChatGPT plan OAuth credentials inside an encrypted
// bot_secrets or workspace_secrets row so access/refresh rotation shares one lock.
type OpenAIPlanTokens struct {
	Access   string    `json:"access"`
	Refresh  string    `json:"refresh"`
	Expires  time.Time `json:"expires"`
	ClientID string    `json:"clientId"`
	Subject  string    `json:"subject,omitempty"`
}

// MarshalOpenAIPlanTokens serializes ChatGPT plan OAuth credentials for envelope encryption.
func MarshalOpenAIPlanTokens(tokens OpenAIPlanTokens) ([]byte, error) {
	if tokens.Access == "" || tokens.Refresh == "" || tokens.Expires.IsZero() || tokens.ClientID == "" {
		return nil, errors.New("incomplete ChatGPT plan token payload")
	}
	return json.Marshal(tokens)
}

// UnmarshalOpenAIPlanTokens parses decrypted ChatGPT plan OAuth credentials.
func UnmarshalOpenAIPlanTokens(raw []byte) (OpenAIPlanTokens, error) {
	var tokens OpenAIPlanTokens
	if err := json.Unmarshal(raw, &tokens); err != nil {
		return OpenAIPlanTokens{}, err
	}
	if tokens.Access == "" || tokens.Refresh == "" || tokens.Expires.IsZero() {
		return OpenAIPlanTokens{}, errors.New("invalid ChatGPT plan token payload")
	}
	return tokens, nil
}

// ExtractSecretMatchValues returns the sensitive token strings that guardrails
// and memory leak checks must protect for a decrypted secret row.
func ExtractSecretMatchValues(authKind string, plaintext []byte) []string {
	if len(plaintext) == 0 {
		return nil
	}
	if authKind == SecretAuthKindOpenAIPlan || authKind == SecretAuthKindOpenAIPlanReconnect {
		var tokens OpenAIPlanTokens
		if json.Unmarshal(plaintext, &tokens) == nil {
			var values []string
			if tokens.Access != "" {
				values = append(values, tokens.Access)
			}
			if tokens.Refresh != "" {
				values = append(values, tokens.Refresh)
			}
			if len(values) > 0 {
				return values
			}
		}
	}
	return []string{string(plaintext)}
}
