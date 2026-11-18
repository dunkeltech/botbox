// SPDX-License-Identifier: AGPL-3.0-only

package secrets

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// CLIProviderResolver selects the encrypted credential for the scoped bot,
// preferring a bot secret over the inherited workspace secret. Claude may use
// a subscription token. Other CLI provider routes use API keys.
func CLIProviderResolver(q *db.Queries, ring *KeyRing, workspaceID, botID uuid.UUID) llmproxy.SecretResolver {
	return func(ctx context.Context, requestedBot, provider string) (*llmproxy.SecretConfig, error) {
		if q == nil || ring == nil || requestedBot != botID.String() {
			return nil, llmproxy.ErrSecretNotFound
		}
		target, auth := "", llmproxy.AuthKindBearer
		switch provider {
		case "openai":
			target = "https://api.openai.com"
		case "openrouter":
			target = "https://openrouter.ai/api"
		case "anthropic":
			target = "https://api.anthropic.com"
			auth = llmproxy.AuthKindAnthropicKey
		default:
			return nil, llmproxy.ErrSecretNotFound
		}
		ws := pgtype.UUID{Bytes: workspaceID, Valid: true}
		bot := pgtype.UUID{Bytes: botID, Valid: true}
		if _, err := q.GetBotByWorkspaceAndID(ctx, db.GetBotByWorkspaceAndIDParams{WorkspaceID: ws, ID: bot}); err != nil {
			return nil, llmproxy.ErrSecretNotFound
		}
		bots, err := q.ListBotSecretsByWorkspaceAndBot(ctx, db.ListBotSecretsByWorkspaceAndBotParams{WorkspaceID: ws, BotID: bot})
		if err != nil {
			return nil, errors.New("provider credentials unavailable")
		}
		decrypt := func(scope string, id pgtype.UUID, key string, wrapped, value []byte, domain string, subdomains bool, path string) (*llmproxy.SecretConfig, error) {
			upstream, _ := url.Parse(target)
			domain = strings.ToLower(domain)
			if domain != upstream.Hostname() && (!subdomains || !strings.HasSuffix(upstream.Hostname(), "."+domain)) {
				return nil, llmproxy.ErrSecretNotFound
			}
			aad := fmt.Appendf(nil, "workspace:%s/scope:%s/id:%s", workspaceID, scope, uuid.UUID(id.Bytes))
			plaintext, err := ring.Decrypt(EnvelopeCiphertext{KeyID: key, WrappedDEK: wrapped, Ciphertext: value}, aad)
			if err != nil {
				return nil, errors.New("provider credential cannot be decrypted")
			}
			return &llmproxy.SecretConfig{UpstreamURL: target, AuthKind: auth, SecretValue: string(plaintext), PathPrefix: path}, nil
		}
		names := []string{"llm-" + provider}
		if provider == "anthropic" {
			names = []string{"llm-anthropic-oauth", "llm-anthropic"}
		}
		for _, name := range names {
			for _, s := range bots {
				if s.Name == name {
					if name == "llm-anthropic-oauth" {
						auth = llmproxy.AuthKindClaudeOAuth
					}
					return decrypt("bot", s.ID, s.KekID, s.WrappedDek, s.EncryptedValue, s.DomainMatch, s.MatchSubdomains, s.PathPrefix)
				}
			}
		}
		inherited, err := q.ListWorkspaceSecretsByWorkspace(ctx, ws)
		if err != nil {
			return nil, errors.New("provider credentials unavailable")
		}
		for _, name := range names {
			for _, s := range inherited {
				if s.Name == name {
					if name == "llm-anthropic-oauth" {
						auth = llmproxy.AuthKindClaudeOAuth
					}
					return decrypt("workspace", s.ID, s.KekID, s.WrappedDek, s.EncryptedValue, s.DomainMatch, s.MatchSubdomains, s.PathPrefix)
				}
			}
		}
		return nil, llmproxy.ErrSecretNotFound
	}
}
