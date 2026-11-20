// SPDX-License-Identifier: AGPL-3.0-only

package codexplan

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// Upstream is the only inference origin for a ChatGPT plan credential.
	Upstream = "https://api.openai.com"
	// SecretName is the managed secret entry synchronized in bot or workspace secrets.
	SecretName = "llm-openai"
	// ScopeBot stores llm-openai on the paired bot.
	ScopeBot = "bot"
	// ScopeWorkspace stores llm-openai at workspace scope for all workspace bots.
	ScopeWorkspace = "workspace"
)

var (
	// ErrPairing covers expired, consumed, and invalid pairing attempts.
	ErrPairing = errors.New("ChatGPT plan pairing unavailable")
	// ErrForbidden covers an account or desktop without an active grant.
	ErrForbidden = errors.New("ChatGPT plan use is forbidden")
	// ErrInvalidScope rejects unsupported secret storage scopes.
	ErrInvalidScope = errors.New("invalid ChatGPT plan secret scope")
)

// Pairing is a short-lived grant to complete sign-in for one bot.
type Pairing struct {
	Workspace uuid.UUID `json:"workspace"`
	Bot       uuid.UUID `json:"bot"`
	Secret    string    `json:"secret"`
	ClientID  string    `json:"clientId"`
}

// Encode returns the code the authenticated user enters in the workstation helper.
func (p Pairing) Encode() string {
	raw, _ := json.Marshal(p) //nolint:gosec // the authorized user needs this short-lived one-use pairing code
	return base64.RawURLEncoding.EncodeToString(raw)
}

// ParsePairing validates the code format and its high-entropy secret.
func ParsePairing(code string) (Pairing, bool) {
	var p Pairing
	if len(code) > 1024 {
		return p, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil || json.Unmarshal(raw, &p) != nil || p.Workspace == uuid.Nil || p.Bot == uuid.Nil ||
		(p.ClientID != DynamicClient && !ValidClientID(p.ClientID)) {
		return p, false
	}
	secret, err := base64.RawURLEncoding.DecodeString(p.Secret)
	return p, err == nil && len(secret) == 32
}

// HostID identifies the provisioned bot host across restarts.
func (p Pairing) HostID() string { return "urn:uuid:" + p.Bot.String() }

func (p Pairing) digest() []byte {
	digest := sha256.Sum256([]byte(p.Encode()))
	return digest[:]
}

// Service stores a single user-owned registration for each bot and synchronizes
// the encrypted OAuth token bundle into the target bot or workspace llm-openai secret.
type Service struct {
	pool  *pgxpool.Pool
	ring  *secrets.KeyRing
	oauth *OAuth
	now   func() time.Time
}

// NewService builds the control-plane connection service.
func NewService(pool *pgxpool.Pool, ring *secrets.KeyRing, oauth *OAuth, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	if oauth == nil {
		oauth = NewOAuth(nil, now)
	}
	return &Service{pool: pool, ring: ring, oauth: oauth, now: now}
}

// Status returns connection state, whether the caller owns it, and its secret scope.
func (s *Service) Status(ctx context.Context, workspace, bot, user pgtype.UUID) (string, bool, string, error) {
	q := db.New(s.pool)
	conn, err := q.GetCodexPlanConnection(ctx, db.GetCodexPlanConnectionParams{WorkspaceID: workspace, BotID: bot})
	if errors.Is(err, pgx.ErrNoRows) {
		return "disconnected", false, "", nil
	}
	if err != nil {
		return "", false, "", err
	}
	if conn.BotSecretID.Valid {
		bs, bsErr := q.GetBotSecretByWorkspaceBotAndID(ctx, db.GetBotSecretByWorkspaceBotAndIDParams{
			WorkspaceID: workspace, BotID: bot, ID: conn.BotSecretID,
		})
		if errors.Is(bsErr, pgx.ErrNoRows) {
			return "disconnected", false, "", nil
		}
		if bsErr != nil {
			return "", false, "", bsErr
		}
		return statusFromAuthKind(bs.AuthKind), conn.OwnerUserID == user, ScopeBot, nil
	}
	if conn.WorkspaceSecretID.Valid {
		wsSecret, wsErr := q.GetWorkspaceSecretByWorkspaceAndID(ctx, db.GetWorkspaceSecretByWorkspaceAndIDParams{
			WorkspaceID: workspace, ID: conn.WorkspaceSecretID,
		})
		if errors.Is(wsErr, pgx.ErrNoRows) {
			return "disconnected", false, "", nil
		}
		if wsErr != nil {
			return "", false, "", wsErr
		}
		return statusFromAuthKind(wsSecret.AuthKind), conn.OwnerUserID == user, ScopeWorkspace, nil
	}
	status := "connected"
	if !conn.KekID.Valid {
		status = "reconnect"
	}
	scopeName := conn.SecretScope
	if scopeName == "" {
		scopeName = ScopeBot
	}
	return status, conn.OwnerUserID == user, scopeName, nil
}

func statusFromAuthKind(authKind string) string {
	switch authKind {
	case secrets.SecretAuthKindOpenAIPlan:
		return "connected"
	case secrets.SecretAuthKindOpenAIPlanReconnect:
		return "reconnect"
	default:
		return "disconnected"
	}
}

// Start creates one pending, short-lived pairing for the authorized bot manager.
func (s *Service) Start(ctx context.Context, workspace, bot, owner pgtype.UUID, secretScope string) (string, error) {
	if secretScope == "" {
		secretScope = ScopeBot
	}
	if secretScope != ScopeBot && secretScope != ScopeWorkspace {
		return "", ErrInvalidScope
	}
	q := db.New(s.pool)
	if secretScope == ScopeWorkspace {
		if err := canManageWorkspaceSecrets(ctx, q, workspace, owner); err != nil {
			return "", err
		}
	}
	conn, err := q.GetCodexPlanConnection(ctx, db.GetCodexPlanConnectionParams{WorkspaceID: workspace, BotID: bot})
	clientID := DynamicClient
	switch {
	case err == nil:
		active, activeErr := isConnectionActive(ctx, q, conn)
		if activeErr != nil {
			return "", activeErr
		}
		if active {
			if conn.OwnerUserID != owner {
				return "", ErrForbidden
			}
			clientID = conn.ClientID
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return "", err
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	p := Pairing{Workspace: workspace.Bytes, Bot: bot.Bytes, Secret: base64.RawURLEncoding.EncodeToString(random), ClientID: clientID}
	now := s.now()
	rows, err := q.PutCodexPlanPairing(ctx, db.PutCodexPlanPairingParams{
		WorkspaceID: workspace,
		BotID:       bot,
		OwnerUserID: owner,
		CodeHash:    p.digest(),
		ExpiresAt:   timestamp(now.Add(10 * time.Minute)),
		SecretScope: secretScope,
		AsOf:        timestamp(now),
	})
	if err != nil {
		return "", err
	}
	if rows != 1 {
		return "", ErrPairing
	}
	return p.Encode(), nil
}

func isConnectionActive(ctx context.Context, q *db.Queries, conn db.CodexPlanConnection) (bool, error) {
	if conn.BotSecretID.Valid {
		bs, err := q.GetBotSecretByWorkspaceBotAndID(ctx, db.GetBotSecretByWorkspaceBotAndIDParams{
			WorkspaceID: conn.WorkspaceID, BotID: conn.BotID, ID: conn.BotSecretID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return bs.AuthKind == secrets.SecretAuthKindOpenAIPlan || bs.AuthKind == secrets.SecretAuthKindOpenAIPlanReconnect, nil
	}
	if conn.WorkspaceSecretID.Valid {
		wsSecret, err := q.GetWorkspaceSecretByWorkspaceAndID(ctx, db.GetWorkspaceSecretByWorkspaceAndIDParams{
			WorkspaceID: conn.WorkspaceID, ID: conn.WorkspaceSecretID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return wsSecret.AuthKind == secrets.SecretAuthKindOpenAIPlan || wsSecret.AuthKind == secrets.SecretAuthKindOpenAIPlanReconnect, nil
	}
	return true, nil
}

// Complete consumes a paired code, installs a verified ChatGPT plan connection,
// and writes or updates llm-openai in the selected bot or workspace secrets scope.
func (s *Service) Complete(ctx context.Context, p Pairing, grant Grant) error {
	if !grant.Valid() || (p.ClientID != DynamicClient && p.ClientID != grant.ClientID) {
		return ErrPairing
	}
	ws, bot := scope(p.Workspace, p.Bot)
	q := db.New(s.pool)
	digest := p.digest()
	owner, err := q.ClaimCodexPlanPairing(ctx, db.ClaimCodexPlanPairingParams{WorkspaceID: ws, BotID: bot, CodeHash: digest, ExpiresAt: timestamp(s.now())})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrPairing
	}
	if err != nil {
		return err
	}
	tokens, subject, err := s.oauth.Exchange(ctx, grant)
	if err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q = q.WithTx(tx)
	finished, err := q.FinishCodexPlanPairing(ctx, db.FinishCodexPlanPairingParams{WorkspaceID: ws, BotID: bot, CodeHash: digest, ExpiresAt: timestamp(s.now())})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && finished.OwnerUserID != owner) {
		return ErrPairing
	}
	if err != nil {
		return err
	}
	if err = canManage(ctx, q, ws, bot, owner); err != nil {
		return err
	}
	secretScope := finished.SecretScope
	if secretScope == "" {
		secretScope = ScopeBot
	}
	if secretScope == ScopeWorkspace {
		if err = canManageWorkspaceSecrets(ctx, q, ws, owner); err != nil {
			return err
		}
	}

	prevConn, prevErr := q.LockCodexPlanConnection(ctx, db.LockCodexPlanConnectionParams{WorkspaceID: ws, BotID: bot})
	if prevErr != nil && !errors.Is(prevErr, pgx.ErrNoRows) {
		return prevErr
	}
	if prevErr == nil {
		active, activeErr := isConnectionActive(ctx, q, prevConn)
		if activeErr != nil {
			return activeErr
		}
		if !active {
			if err = q.DeleteCodexPlanConnection(ctx, db.DeleteCodexPlanConnectionParams{WorkspaceID: ws, BotID: bot}); err != nil {
				return err
			}
		} else if prevConn.OwnerUserID != owner || prevConn.Subject != subject || prevConn.ClientID != grant.ClientID {
			return ErrForbidden
		}
	}

	planTokens := secrets.OpenAIPlanTokens{
		Access:   tokens.Access,
		Refresh:  tokens.Refresh,
		Expires:  tokens.Expires,
		ClientID: grant.ClientID,
		Subject:  subject,
	}
	masked := secrets.MaskedPreview(tokens.Access)

	var botSecretID, wsSecretID pgtype.UUID
	switch secretScope {
	case ScopeBot:
		secretID := uuid.New()
		existing, getErr := q.GetBotSecretByWorkspaceBotAndName(ctx, db.GetBotSecretByWorkspaceBotAndNameParams{
			WorkspaceID: ws, BotID: bot, Name: SecretName,
		})
		if getErr == nil {
			secretID = uuid.UUID(existing.ID.Bytes)
		} else if !errors.Is(getErr, pgx.ErrNoRows) {
			return getErr
		}
		env, sealErr := s.sealSecret(p.Workspace, ScopeBot, secretID, planTokens)
		if sealErr != nil {
			return sealErr
		}
		row, upsertErr := q.UpsertCodexPlanBotSecret(ctx, db.UpsertCodexPlanBotSecretParams{
			ID:             pgtype.UUID{Bytes: secretID, Valid: true},
			WorkspaceID:    ws,
			BotID:          bot,
			KekID:          env.KeyID,
			WrappedDek:     env.WrappedDEK,
			EncryptedValue: env.Ciphertext,
			MaskedPreview:  masked,
			CreatedBy:      owner,
		})
		if upsertErr != nil {
			return upsertErr
		}
		botSecretID = row.ID
		if prevErr == nil && prevConn.WorkspaceSecretID.Valid {
			if _, delErr := q.DeleteOpenAIPlanWorkspaceSecret(ctx, db.DeleteOpenAIPlanWorkspaceSecretParams{
				WorkspaceID: ws, ID: prevConn.WorkspaceSecretID,
			}); delErr != nil {
				return delErr
			}
		}
	case ScopeWorkspace:
		secretID := uuid.New()
		existing, getErr := q.GetWorkspaceSecretByWorkspaceAndName(ctx, db.GetWorkspaceSecretByWorkspaceAndNameParams{
			WorkspaceID: ws, Name: SecretName,
		})
		if getErr == nil {
			secretID = uuid.UUID(existing.ID.Bytes)
		} else if !errors.Is(getErr, pgx.ErrNoRows) {
			return getErr
		}
		env, sealErr := s.sealSecret(p.Workspace, ScopeWorkspace, secretID, planTokens)
		if sealErr != nil {
			return sealErr
		}
		row, upsertErr := q.UpsertCodexPlanWorkspaceSecret(ctx, db.UpsertCodexPlanWorkspaceSecretParams{
			ID:             pgtype.UUID{Bytes: secretID, Valid: true},
			WorkspaceID:    ws,
			KekID:          env.KeyID,
			WrappedDek:     env.WrappedDEK,
			EncryptedValue: env.Ciphertext,
			MaskedPreview:  masked,
			CreatedBy:      owner,
		})
		if upsertErr != nil {
			return upsertErr
		}
		wsSecretID = row.ID
		if prevErr == nil && prevConn.BotSecretID.Valid {
			if _, delErr := q.DeleteOpenAIPlanBotSecret(ctx, db.DeleteOpenAIPlanBotSecretParams{
				WorkspaceID: ws, BotID: bot, ID: prevConn.BotSecretID,
			}); delErr != nil {
				return delErr
			}
		}
	default:
		return ErrInvalidScope
	}

	revision := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	rows, err := q.PutCodexPlanConnection(ctx, db.PutCodexPlanConnectionParams{
		WorkspaceID:       ws,
		BotID:             bot,
		OwnerUserID:       owner,
		ClientID:          grant.ClientID,
		Subject:           subject,
		Revision:          revision,
		SecretScope:       secretScope,
		BotSecretID:       botSecretID,
		WorkspaceSecretID: wsSecretID,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrForbidden
	}
	return tx.Commit(ctx)
}

// Disconnect removes local access and any managed llm-openai secret before
// attempting to revoke the renewable grant at OpenAI.
func (s *Service) Disconnect(ctx context.Context, ws, bot pgtype.UUID) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if err = q.DeleteCodexPlanPairing(ctx, db.DeleteCodexPlanPairingParams{WorkspaceID: ws, BotID: bot}); err != nil {
		return false, err
	}
	conn, err := q.LockCodexPlanConnection(ctx, db.LockCodexPlanConnectionParams{WorkspaceID: ws, BotID: bot})
	if errors.Is(err, pgx.ErrNoRows) {
		return true, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}

	var clientID, refreshToken string
	var canRevoke bool
	switch {
	case conn.BotSecretID.Valid:
		bs, bsErr := q.LockBotSecretByWorkspaceBotAndID(ctx, db.LockBotSecretByWorkspaceBotAndIDParams{
			WorkspaceID: ws, BotID: bot, ID: conn.BotSecretID,
		})
		if bsErr == nil {
			if bs.AuthKind == secrets.SecretAuthKindOpenAIPlan {
				if tok, openErr := s.openSecret(uuid.UUID(ws.Bytes), ScopeBot, uuid.UUID(bs.ID.Bytes), bs.KekID, bs.WrappedDek, bs.EncryptedValue); openErr == nil {
					clientID, refreshToken = tok.ClientID, tok.Refresh
					if clientID == "" {
						clientID = conn.ClientID
					}
					canRevoke = refreshToken != ""
				}
			}
			if _, delErr := q.DeleteOpenAIPlanBotSecret(ctx, db.DeleteOpenAIPlanBotSecretParams{
				WorkspaceID: ws, BotID: bot, ID: conn.BotSecretID,
			}); delErr != nil {
				return false, delErr
			}
		} else if !errors.Is(bsErr, pgx.ErrNoRows) {
			return false, bsErr
		}
	case conn.WorkspaceSecretID.Valid:
		wsSecret, wsErr := q.LockWorkspaceSecretByWorkspaceAndID(ctx, db.LockWorkspaceSecretByWorkspaceAndIDParams{
			WorkspaceID: ws, ID: conn.WorkspaceSecretID,
		})
		if wsErr == nil {
			if wsSecret.AuthKind == secrets.SecretAuthKindOpenAIPlan {
				if tok, openErr := s.openSecret(uuid.UUID(ws.Bytes), ScopeWorkspace, uuid.UUID(wsSecret.ID.Bytes), wsSecret.KekID, wsSecret.WrappedDek, wsSecret.EncryptedValue); openErr == nil {
					clientID, refreshToken = tok.ClientID, tok.Refresh
					if clientID == "" {
						clientID = conn.ClientID
					}
					canRevoke = refreshToken != ""
				}
			}
			if _, delErr := q.DeleteOpenAIPlanWorkspaceSecret(ctx, db.DeleteOpenAIPlanWorkspaceSecretParams{
				WorkspaceID: ws, ID: conn.WorkspaceSecretID,
			}); delErr != nil {
				return false, delErr
			}
		} else if !errors.Is(wsErr, pgx.ErrNoRows) {
			return false, wsErr
		}
	case conn.KekID.Valid:
		if tokens, openErr := s.open(conn); openErr == nil {
			clientID, refreshToken = conn.ClientID, tokens.Refresh
			canRevoke = true
		}
	}

	if err = q.DeleteCodexPlanConnection(ctx, db.DeleteCodexPlanConnectionParams{WorkspaceID: ws, BotID: bot}); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	if !canRevoke {
		return false, nil
	}
	revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 12*time.Second)
	defer cancel()
	return s.oauth.Revoke(revokeCtx, clientID, refreshToken) == nil, nil
}

// CheckDesktop checks the current holder, scope, plan revision, and explicit egress policy
// for a bot's ChatGPT plan connection.
func CheckDesktop(ctx context.Context, q *db.Queries, ws, bot pgtype.UUID, revision string) error {
	conn, err := q.GetCodexPlanConnection(ctx, db.GetCodexPlanConnectionParams{WorkspaceID: ws, BotID: bot})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	active, err := isConnectionActive(ctx, q, conn)
	if err != nil {
		return err
	}
	if !active || (!conn.BotSecretID.Valid && !conn.WorkspaceSecretID.Valid && !conn.KekID.Valid) ||
		(revision != "" && uuid.UUID(conn.Revision.Bytes).String() != revision) {
		return ErrForbidden
	}
	if conn.BotSecretID.Valid {
		bs, bsErr := q.GetBotSecretByWorkspaceBotAndID(ctx, db.GetBotSecretByWorkspaceBotAndIDParams{
			WorkspaceID: ws, BotID: bot, ID: conn.BotSecretID,
		})
		if bsErr != nil || bs.AuthKind != secrets.SecretAuthKindOpenAIPlan {
			return ErrForbidden
		}
	} else if conn.WorkspaceSecretID.Valid {
		wsSecret, wsErr := q.GetWorkspaceSecretByWorkspaceAndID(ctx, db.GetWorkspaceSecretByWorkspaceAndIDParams{
			WorkspaceID: ws, ID: conn.WorkspaceSecretID,
		})
		if wsErr != nil || wsSecret.AuthKind != secrets.SecretAuthKindOpenAIPlan {
			return ErrForbidden
		}
	}
	return checkDesktop(ctx, q, conn)
}

// CheckDesktopOpenAI verifies desktop readiness for /llm/openai/ready and
// desktop-initiated /llm/openai/* requests, supporting bot connections,
// inherited workspace OAuth secrets, and static API keys.
func CheckDesktopOpenAI(ctx context.Context, q *db.Queries, ws, bot pgtype.UUID, revision string) error {
	rule, err := q.GetEgressRuleByWorkspaceAndBot(ctx, db.GetEgressRuleByWorkspaceAndBotParams{WorkspaceID: ws, BotID: bot})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, blocked := mitmproxy.MatchingHostPattern(rule.BlockedHosts, "api.openai.com"); blocked {
		return ErrForbidden
	}

	bs, bsErr := q.GetBotSecretByWorkspaceBotAndName(ctx, db.GetBotSecretByWorkspaceBotAndNameParams{
		WorkspaceID: ws, BotID: bot, Name: SecretName,
	})
	if bsErr == nil {
		if !domainMatchesOpenAI(bs.DomainMatch, bs.MatchSubdomains) {
			return ErrForbidden
		}
		switch bs.AuthKind {
		case secrets.SecretAuthKindPlain:
			return nil
		case secrets.SecretAuthKindOpenAIPlanReconnect:
			return ErrReconnect
		case secrets.SecretAuthKindOpenAIPlan:
			return CheckDesktop(ctx, q, ws, bot, revision)
		default:
			return ErrForbidden
		}
	}
	if !errors.Is(bsErr, pgx.ErrNoRows) {
		return bsErr
	}

	wsSecret, wsErr := q.GetWorkspaceSecretByWorkspaceAndName(ctx, db.GetWorkspaceSecretByWorkspaceAndNameParams{
		WorkspaceID: ws, Name: SecretName,
	})
	if wsErr == nil {
		if !domainMatchesOpenAI(wsSecret.DomainMatch, wsSecret.MatchSubdomains) {
			return ErrForbidden
		}
		switch wsSecret.AuthKind {
		case secrets.SecretAuthKindPlain:
			return nil
		case secrets.SecretAuthKindOpenAIPlanReconnect:
			return ErrReconnect
		case secrets.SecretAuthKindOpenAIPlan:
			conn, connErr := q.GetCodexPlanConnection(ctx, db.GetCodexPlanConnectionParams{WorkspaceID: ws, BotID: bot})
			if connErr == nil && conn.WorkspaceSecretID == wsSecret.ID {
				if revision != "" && uuid.UUID(conn.Revision.Bytes).String() != revision {
					return ErrForbidden
				}
				return checkDesktop(ctx, q, conn)
			}
			if connErr != nil && !errors.Is(connErr, pgx.ErrNoRows) {
				return connErr
			}
			if revision != "" && uuid.UUID(wsSecret.ID.Bytes).String() != revision {
				return ErrForbidden
			}
			return checkInheritedDesktop(ctx, q, ws, bot)
		default:
			return ErrForbidden
		}
	}
	if !errors.Is(wsErr, pgx.ErrNoRows) {
		return wsErr
	}

	return CheckDesktop(ctx, q, ws, bot, revision)
}

func checkInheritedDesktop(ctx context.Context, q *db.Queries, ws, bot pgtype.UUID) error {
	env, err := q.GetEnvironmentByWorkspaceAndBot(ctx, db.GetEnvironmentByWorkspaceAndBotParams{WorkspaceID: ws, ID: bot})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if !env.ControlHolderUserID.Valid || env.Status != "running" || env.Driver != "docker" {
		return ErrForbidden
	}
	if err = canManage(ctx, q, ws, bot, env.ControlHolderUserID); err != nil {
		return err
	}
	rule, err := q.GetEgressRuleByWorkspaceAndBot(ctx, db.GetEgressRuleByWorkspaceAndBotParams{WorkspaceID: ws, BotID: bot})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, blocked := mitmproxy.MatchingHostPattern(rule.BlockedHosts, "api.openai.com"); blocked {
		return ErrForbidden
	}
	return nil
}

func checkDesktop(ctx context.Context, q *db.Queries, conn db.CodexPlanConnection) error {
	ws, bot := conn.WorkspaceID, conn.BotID
	env, err := q.GetEnvironmentByWorkspaceAndBot(ctx, db.GetEnvironmentByWorkspaceAndBotParams{WorkspaceID: ws, ID: bot})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if env.ControlHolderUserID != conn.OwnerUserID || env.Status != "running" || env.Driver != "docker" {
		return ErrForbidden
	}
	if err = canManage(ctx, q, ws, bot, conn.OwnerUserID); err != nil {
		return err
	}
	rule, err := q.GetEgressRuleByWorkspaceAndBot(ctx, db.GetEgressRuleByWorkspaceAndBotParams{WorkspaceID: ws, BotID: bot})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, blocked := mitmproxy.MatchingHostPattern(rule.BlockedHosts, "api.openai.com"); blocked {
		return ErrForbidden
	}
	return nil
}

// Access resolves an active desktop's access token and serializes token rotation.
func (s *Service) Access(ctx context.Context, workspace, bot uuid.UUID) (string, string, error) {
	ws, bp := scope(workspace, bot)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	conn, err := q.LockCodexPlanConnection(ctx, db.LockCodexPlanConnectionParams{WorkspaceID: ws, BotID: bp})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrForbidden
	}
	if err != nil {
		return "", "", err
	}
	if err = checkDesktop(ctx, q, conn); err != nil {
		return "", "", err
	}
	revision := uuid.UUID(conn.Revision.Bytes).String()
	switch {
	case conn.BotSecretID.Valid:
		access, opErr := s.accessBotSecretInTx(ctx, q, workspace, ws, bp, conn.BotSecretID, conn.ClientID)
		if opErr != nil && !errors.Is(opErr, ErrReconnect) {
			return "", "", opErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return "", "", commitErr
		}
		return access, revision, opErr
	case conn.WorkspaceSecretID.Valid:
		access, opErr := s.accessWorkspaceSecretInTx(ctx, q, workspace, ws, conn.WorkspaceSecretID, conn.ClientID)
		if opErr != nil && !errors.Is(opErr, ErrReconnect) {
			return "", "", opErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return "", "", commitErr
		}
		return access, revision, opErr
	default:
		if !conn.KekID.Valid {
			return "", "", ErrReconnect
		}
		tokens, openErr := s.open(conn)
		if openErr != nil {
			return "", "", openErr
		}
		var refreshErr error
		if !s.now().Add(time.Minute).Before(tokens.Expires) {
			tokens, refreshErr = s.oauth.Refresh(ctx, conn.ClientID, tokens.Refresh)
			if refreshErr != nil && !errors.Is(refreshErr, ErrReconnect) {
				return "", "", refreshErr
			}
			// A terminal OAuth error clears the session but retains the registration.
			var update db.UpdateCodexPlanTokensParams
			update.WorkspaceID, update.BotID, update.Revision = ws, bp, conn.Revision
			if refreshErr == nil {
				envelope, sealErr := s.seal(ws, bp, conn.Revision, tokens)
				if sealErr != nil {
					return "", "", sealErr
				}
				update.KekID, update.WrappedDek, update.EncryptedTokens = keyID(envelope.KeyID), envelope.WrappedDEK, envelope.Ciphertext
			}
			rows, updateErr := q.UpdateCodexPlanTokens(ctx, update)
			if updateErr != nil {
				return "", "", updateErr
			}
			if rows != 1 {
				return "", "", ErrForbidden
			}
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return "", "", commitErr
		}
		return tokens.Access, revision, refreshErr
	}
}

// ResolveOpenAISecret resolves the bot's effective llm-openai secret (bot scope first,
// then inherited workspace scope) and atomically refreshes ChatGPT plan OAuth tokens
// under row lock when needed.
func (s *Service) ResolveOpenAISecret(ctx context.Context, workspace, bot uuid.UUID) (*llmproxy.SecretConfig, error) {
	ws, bp := scope(workspace, bot)
	q := db.New(s.pool)
	if _, err := q.GetBotByWorkspaceAndID(ctx, db.GetBotByWorkspaceAndIDParams{WorkspaceID: ws, ID: bp}); err != nil {
		return nil, llmproxy.ErrSecretNotFound
	}

	bs, bsErr := q.GetBotSecretByWorkspaceBotAndName(ctx, db.GetBotSecretByWorkspaceBotAndNameParams{
		WorkspaceID: ws, BotID: bp, Name: SecretName,
	})
	if bsErr == nil {
		if !domainMatchesOpenAI(bs.DomainMatch, bs.MatchSubdomains) {
			return nil, llmproxy.ErrSecretNotFound
		}
		return s.resolveBotSecret(ctx, workspace, ws, bp, bs)
	}
	if !errors.Is(bsErr, pgx.ErrNoRows) {
		return nil, errors.New("provider credentials unavailable")
	}

	wsSecret, wsErr := q.GetWorkspaceSecretByWorkspaceAndName(ctx, db.GetWorkspaceSecretByWorkspaceAndNameParams{
		WorkspaceID: ws, Name: SecretName,
	})
	if wsErr == nil {
		if !domainMatchesOpenAI(wsSecret.DomainMatch, wsSecret.MatchSubdomains) {
			return nil, llmproxy.ErrSecretNotFound
		}
		return s.resolveWorkspaceSecret(ctx, workspace, ws, bp, wsSecret)
	}
	if !errors.Is(wsErr, pgx.ErrNoRows) {
		return nil, errors.New("provider credentials unavailable")
	}

	// Fallback for legacy connections created prior to secret synchronization.
	access, rev, err := s.Access(ctx, workspace, bot)
	switch {
	case errors.Is(err, ErrForbidden):
		return nil, llmproxy.ErrSecretNotFound
	case errors.Is(err, ErrReconnect):
		return nil, llmproxy.ErrPlanReconnect
	case err != nil:
		return nil, err
	default:
		return &llmproxy.SecretConfig{
			UpstreamURL:  Upstream,
			PathPrefix:   "/v1/responses",
			AuthKind:     llmproxy.AuthKindOpenAIPlan,
			SecretValue:  access,
			PlanRevision: rev,
		}, nil
	}
}

func (s *Service) resolveBotSecret(ctx context.Context, workspace uuid.UUID, ws, bp pgtype.UUID, rowIDSource db.BotSecret) (*llmproxy.SecretConfig, error) {
	switch rowIDSource.AuthKind {
	case secrets.SecretAuthKindPlain:
		plain, err := s.ring.Decrypt(secrets.EnvelopeCiphertext{
			KeyID:      rowIDSource.KekID,
			WrappedDEK: rowIDSource.WrappedDek,
			Ciphertext: rowIDSource.EncryptedValue,
		}, secrets.SecretAAD(workspace, ScopeBot, uuid.UUID(rowIDSource.ID.Bytes)))
		if err != nil {
			return nil, errors.New("provider credential cannot be decrypted")
		}
		return &llmproxy.SecretConfig{
			UpstreamURL: Upstream,
			PathPrefix:  rowIDSource.PathPrefix,
			AuthKind:    llmproxy.AuthKindBearer,
			SecretValue: string(plain),
		}, nil
	case secrets.SecretAuthKindOpenAIPlanReconnect:
		return nil, llmproxy.ErrPlanReconnect
	case secrets.SecretAuthKindOpenAIPlan:
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		q := db.New(tx)
		access, opErr := s.accessBotSecretInTx(ctx, q, workspace, ws, bp, rowIDSource.ID, "")
		if opErr != nil && !errors.Is(opErr, ErrReconnect) {
			return nil, opErr
		}
		revision := uuid.UUID(rowIDSource.ID.Bytes).String()
		if conn, connErr := q.GetCodexPlanConnection(ctx, db.GetCodexPlanConnectionParams{WorkspaceID: ws, BotID: bp}); connErr == nil && conn.BotSecretID == rowIDSource.ID {
			revision = uuid.UUID(conn.Revision.Bytes).String()
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return nil, commitErr
		}
		if errors.Is(opErr, ErrReconnect) {
			return nil, llmproxy.ErrPlanReconnect
		}
		return &llmproxy.SecretConfig{
			UpstreamURL:  Upstream,
			PathPrefix:   rowIDSource.PathPrefix,
			AuthKind:     llmproxy.AuthKindOpenAIPlan,
			SecretValue:  access,
			PlanRevision: revision,
		}, nil
	default:
		return nil, llmproxy.ErrSecretNotFound
	}
}

func (s *Service) resolveWorkspaceSecret(ctx context.Context, workspace uuid.UUID, ws, bp pgtype.UUID, row db.WorkspaceSecret) (*llmproxy.SecretConfig, error) {
	switch row.AuthKind {
	case secrets.SecretAuthKindPlain:
		plain, err := s.ring.Decrypt(secrets.EnvelopeCiphertext{
			KeyID:      row.KekID,
			WrappedDEK: row.WrappedDek,
			Ciphertext: row.EncryptedValue,
		}, secrets.SecretAAD(workspace, ScopeWorkspace, uuid.UUID(row.ID.Bytes)))
		if err != nil {
			return nil, errors.New("provider credential cannot be decrypted")
		}
		return &llmproxy.SecretConfig{
			UpstreamURL: Upstream,
			PathPrefix:  row.PathPrefix,
			AuthKind:    llmproxy.AuthKindBearer,
			SecretValue: string(plain),
		}, nil
	case secrets.SecretAuthKindOpenAIPlanReconnect:
		return nil, llmproxy.ErrPlanReconnect
	case secrets.SecretAuthKindOpenAIPlan:
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		q := db.New(tx)
		access, opErr := s.accessWorkspaceSecretInTx(ctx, q, workspace, ws, row.ID, "")
		if opErr != nil && !errors.Is(opErr, ErrReconnect) {
			return nil, opErr
		}
		revision := uuid.UUID(row.ID.Bytes).String()
		if conn, connErr := q.GetCodexPlanConnection(ctx, db.GetCodexPlanConnectionParams{WorkspaceID: ws, BotID: bp}); connErr == nil && conn.WorkspaceSecretID == row.ID {
			revision = uuid.UUID(conn.Revision.Bytes).String()
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return nil, commitErr
		}
		if errors.Is(opErr, ErrReconnect) {
			return nil, llmproxy.ErrPlanReconnect
		}
		return &llmproxy.SecretConfig{
			UpstreamURL:  Upstream,
			PathPrefix:   row.PathPrefix,
			AuthKind:     llmproxy.AuthKindOpenAIPlan,
			SecretValue:  access,
			PlanRevision: revision,
		}, nil
	default:
		return nil, llmproxy.ErrSecretNotFound
	}
}

func (s *Service) accessBotSecretInTx(ctx context.Context, q *db.Queries, workspace uuid.UUID, ws, bp, secretID pgtype.UUID, fallbackClientID string) (string, error) {
	bs, err := q.LockBotSecretByWorkspaceBotAndID(ctx, db.LockBotSecretByWorkspaceBotAndIDParams{
		WorkspaceID: ws, BotID: bp, ID: secretID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrForbidden
	}
	if err != nil {
		return "", err
	}
	if bs.AuthKind == secrets.SecretAuthKindOpenAIPlanReconnect {
		return "", ErrReconnect
	}
	if bs.AuthKind != secrets.SecretAuthKindOpenAIPlan {
		return "", ErrForbidden
	}
	tokens, err := s.openSecret(workspace, ScopeBot, uuid.UUID(bs.ID.Bytes), bs.KekID, bs.WrappedDek, bs.EncryptedValue)
	if err != nil {
		return "", err
	}
	clientID := tokens.ClientID
	if clientID == "" {
		clientID = fallbackClientID
	}
	if !s.now().Add(time.Minute).Before(tokens.Expires) {
		refreshed, refreshErr := s.oauth.Refresh(ctx, clientID, tokens.Refresh)
		if refreshErr != nil && !errors.Is(refreshErr, ErrReconnect) {
			return "", refreshErr
		}
		nextAuthKind := secrets.SecretAuthKindOpenAIPlan
		nextTokens := tokens
		nextPreview := bs.MaskedPreview
		if refreshErr == nil {
			nextTokens.Access = refreshed.Access
			nextTokens.Refresh = refreshed.Refresh
			nextTokens.Expires = refreshed.Expires
			nextTokens.ClientID = clientID
			nextPreview = secrets.MaskedPreview(refreshed.Access)
		} else {
			nextAuthKind = secrets.SecretAuthKindOpenAIPlanReconnect
			nextTokens.Access = "reconnect"
			nextTokens.Refresh = "reconnect"
			nextTokens.Expires = s.now()
		}
		env, sealErr := s.sealSecret(workspace, ScopeBot, uuid.UUID(bs.ID.Bytes), nextTokens)
		if sealErr != nil {
			return "", sealErr
		}
		rows, updErr := q.UpdateBotSecretOpenAIPlanTokens(ctx, db.UpdateBotSecretOpenAIPlanTokensParams{
			WorkspaceID:    ws,
			BotID:          bp,
			ID:             bs.ID,
			AuthKind:       nextAuthKind,
			KekID:          env.KeyID,
			WrappedDek:     env.WrappedDEK,
			EncryptedValue: env.Ciphertext,
			MaskedPreview:  nextPreview,
		})
		if updErr != nil {
			return "", updErr
		}
		if rows != 1 {
			return "", ErrForbidden
		}
		if refreshErr != nil {
			return "", refreshErr
		}
		tokens = nextTokens
	}
	return tokens.Access, nil
}

func (s *Service) accessWorkspaceSecretInTx(ctx context.Context, q *db.Queries, workspace uuid.UUID, ws, secretID pgtype.UUID, fallbackClientID string) (string, error) {
	wsSecret, err := q.LockWorkspaceSecretByWorkspaceAndID(ctx, db.LockWorkspaceSecretByWorkspaceAndIDParams{
		WorkspaceID: ws, ID: secretID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrForbidden
	}
	if err != nil {
		return "", err
	}
	if wsSecret.AuthKind == secrets.SecretAuthKindOpenAIPlanReconnect {
		return "", ErrReconnect
	}
	if wsSecret.AuthKind != secrets.SecretAuthKindOpenAIPlan {
		return "", ErrForbidden
	}
	tokens, err := s.openSecret(workspace, ScopeWorkspace, uuid.UUID(wsSecret.ID.Bytes), wsSecret.KekID, wsSecret.WrappedDek, wsSecret.EncryptedValue)
	if err != nil {
		return "", err
	}
	clientID := tokens.ClientID
	if clientID == "" {
		clientID = fallbackClientID
	}
	if !s.now().Add(time.Minute).Before(tokens.Expires) {
		refreshed, refreshErr := s.oauth.Refresh(ctx, clientID, tokens.Refresh)
		if refreshErr != nil && !errors.Is(refreshErr, ErrReconnect) {
			return "", refreshErr
		}
		nextAuthKind := secrets.SecretAuthKindOpenAIPlan
		nextTokens := tokens
		nextPreview := wsSecret.MaskedPreview
		if refreshErr == nil {
			nextTokens.Access = refreshed.Access
			nextTokens.Refresh = refreshed.Refresh
			nextTokens.Expires = refreshed.Expires
			nextTokens.ClientID = clientID
			nextPreview = secrets.MaskedPreview(refreshed.Access)
		} else {
			nextAuthKind = secrets.SecretAuthKindOpenAIPlanReconnect
			nextTokens.Access = "reconnect"
			nextTokens.Refresh = "reconnect"
			nextTokens.Expires = s.now()
		}
		env, sealErr := s.sealSecret(workspace, ScopeWorkspace, uuid.UUID(wsSecret.ID.Bytes), nextTokens)
		if sealErr != nil {
			return "", sealErr
		}
		rows, updErr := q.UpdateWorkspaceSecretOpenAIPlanTokens(ctx, db.UpdateWorkspaceSecretOpenAIPlanTokensParams{
			WorkspaceID:    ws,
			ID:             wsSecret.ID,
			AuthKind:       nextAuthKind,
			KekID:          env.KeyID,
			WrappedDek:     env.WrappedDEK,
			EncryptedValue: env.Ciphertext,
			MaskedPreview:  nextPreview,
		})
		if updErr != nil {
			return "", updErr
		}
		if rows != 1 {
			return "", ErrForbidden
		}
		if refreshErr != nil {
			return "", refreshErr
		}
		tokens = nextTokens
	}
	return tokens.Access, nil
}

func domainMatchesOpenAI(domain string, matchSubdomains bool) bool {
	upstream, _ := url.Parse(Upstream)
	domain = strings.ToLower(strings.TrimSpace(domain))
	host := upstream.Hostname()
	return domain == host || (matchSubdomains && strings.HasSuffix(host, "."+domain))
}

func canManageWorkspaceSecrets(ctx context.Context, q *db.Queries, ws, actor pgtype.UUID) error {
	member, err := q.GetWorkspaceMember(ctx, db.GetWorkspaceMemberParams{WorkspaceID: ws, UserID: actor})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if !workspaces.Can(workspaces.Role(member.Role), workspaces.ActionManageMembers, false) {
		return ErrForbidden
	}
	return nil
}

func canManage(ctx context.Context, q *db.Queries, ws, bot, actor pgtype.UUID) error {
	member, err := q.GetWorkspaceMember(ctx, db.GetWorkspaceMemberParams{WorkspaceID: ws, UserID: actor})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	cfg, err := q.GetBotByWorkspaceAndID(ctx, db.GetBotByWorkspaceAndIDParams{WorkspaceID: ws, ID: bot})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if !workspaces.Can(workspaces.Role(member.Role), workspaces.ActionManageBotSecrets, cfg.OwnerUserID == actor) {
		return ErrForbidden
	}
	return nil
}

func (s *Service) seal(ws, bot, revision pgtype.UUID, tokens Tokens) (secrets.EnvelopeCiphertext, error) {
	raw, err := json.Marshal(tokens)
	if err != nil {
		return secrets.EnvelopeCiphertext{}, err
	}
	return s.ring.Encrypt(raw, associatedData(ws, bot, revision))
}

func (s *Service) open(conn db.CodexPlanConnection) (Tokens, error) {
	var tokens Tokens
	raw, err := s.ring.Decrypt(secrets.EnvelopeCiphertext{KeyID: conn.KekID.String, WrappedDEK: conn.WrappedDek, Ciphertext: conn.EncryptedTokens},
		associatedData(conn.WorkspaceID, conn.BotID, conn.Revision))
	if err != nil {
		return tokens, err
	}
	if json.Unmarshal(raw, &tokens) != nil || tokens.Access == "" || tokens.Refresh == "" || tokens.Expires.IsZero() {
		return Tokens{}, ErrReconnect
	}
	return tokens, nil
}

func (s *Service) sealSecret(wsID uuid.UUID, secretScope string, secretID uuid.UUID, tokens secrets.OpenAIPlanTokens) (secrets.EnvelopeCiphertext, error) {
	raw, err := secrets.MarshalOpenAIPlanTokens(tokens)
	if err != nil {
		return secrets.EnvelopeCiphertext{}, err
	}
	return s.ring.Encrypt(raw, secrets.SecretAAD(wsID, secretScope, secretID))
}

func (s *Service) openSecret(wsID uuid.UUID, secretScope string, secretID uuid.UUID, keyID string, wrapped, ciphertext []byte) (secrets.OpenAIPlanTokens, error) {
	raw, err := s.ring.Decrypt(secrets.EnvelopeCiphertext{
		KeyID: keyID, WrappedDEK: wrapped, Ciphertext: ciphertext,
	}, secrets.SecretAAD(wsID, secretScope, secretID))
	if err != nil {
		return secrets.OpenAIPlanTokens{}, err
	}
	tokens, err := secrets.UnmarshalOpenAIPlanTokens(raw)
	if err != nil {
		return secrets.OpenAIPlanTokens{}, ErrReconnect
	}
	return tokens, nil
}

func associatedData(ws, bot, revision pgtype.UUID) []byte {
	return []byte("codex-plan:" + uuid.UUID(ws.Bytes).String() + ":" + uuid.UUID(bot.Bytes).String() + ":" + uuid.UUID(revision.Bytes).String())
}

func scope(ws, bot uuid.UUID) (pgtype.UUID, pgtype.UUID) {
	return pgtype.UUID{Bytes: ws, Valid: true}, pgtype.UUID{Bytes: bot, Valid: true}
}

func timestamp(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
func keyID(id string) pgtype.Text              { return pgtype.Text{String: id, Valid: true} }
