// SPDX-License-Identifier: AGPL-3.0-only

package codexplan

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Upstream is the only inference origin for a ChatGPT plan credential.
const Upstream = "https://api.openai.com"

var (
	// ErrPairing covers expired, consumed, and invalid pairing attempts.
	ErrPairing = errors.New("ChatGPT plan pairing unavailable")
	// ErrForbidden covers an account or desktop without an active grant.
	ErrForbidden = errors.New("ChatGPT plan use is forbidden")
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

// Service stores a single user-owned registration for each bot.
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

// Status returns connection state and whether the caller owns the connection.
func (s *Service) Status(ctx context.Context, workspace, bot, user pgtype.UUID) (string, bool, error) {
	conn, err := db.New(s.pool).GetCodexPlanConnection(ctx, db.GetCodexPlanConnectionParams{WorkspaceID: workspace, BotID: bot})
	if errors.Is(err, pgx.ErrNoRows) {
		return "disconnected", false, nil
	}
	if err != nil {
		return "", false, err
	}
	status := "connected"
	if !conn.KekID.Valid {
		status = "reconnect"
	}
	return status, conn.OwnerUserID == user, nil
}

// Start creates one pending, short-lived pairing for the authorized bot manager.
func (s *Service) Start(ctx context.Context, workspace, bot, owner pgtype.UUID) (string, error) {
	q := db.New(s.pool)
	conn, err := q.GetCodexPlanConnection(ctx, db.GetCodexPlanConnectionParams{WorkspaceID: workspace, BotID: bot})
	clientID := DynamicClient
	switch {
	case err == nil:
		if conn.OwnerUserID != owner {
			return "", ErrForbidden
		}
		clientID = conn.ClientID
	case !errors.Is(err, pgx.ErrNoRows):
		return "", err
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	p := Pairing{Workspace: workspace.Bytes, Bot: bot.Bytes, Secret: base64.RawURLEncoding.EncodeToString(random), ClientID: clientID}
	now := s.now()
	rows, err := q.PutCodexPlanPairing(ctx, db.PutCodexPlanPairingParams{WorkspaceID: workspace, BotID: bot, OwnerUserID: owner,
		CodeHash: p.digest(), ExpiresAt: timestamp(now.Add(10 * time.Minute)), AsOf: timestamp(now)})
	if err != nil {
		return "", err
	}
	if rows != 1 {
		return "", ErrPairing
	}
	return p.Encode(), nil
}

// Complete consumes a paired code and installs a verified ChatGPT plan connection.
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
	confirmed, err := q.FinishCodexPlanPairing(ctx, db.FinishCodexPlanPairingParams{WorkspaceID: ws, BotID: bot, CodeHash: digest, ExpiresAt: timestamp(s.now())})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && confirmed != owner) {
		return ErrPairing
	}
	if err != nil {
		return err
	}
	if err = canManage(ctx, q, ws, bot, owner); err != nil {
		return err
	}
	revision := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	envelope, err := s.seal(ws, bot, revision, tokens)
	if err != nil {
		return err
	}
	rows, err := q.PutCodexPlanConnection(ctx, db.PutCodexPlanConnectionParams{WorkspaceID: ws, BotID: bot, OwnerUserID: owner,
		ClientID: grant.ClientID, Subject: subject, Revision: revision, KekID: keyID(envelope.KeyID),
		WrappedDek: envelope.WrappedDEK, EncryptedTokens: envelope.Ciphertext})
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrForbidden
	}
	return tx.Commit(ctx)
}

// Disconnect removes local access before attempting to revoke the renewable grant.
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
	if err = q.DeleteCodexPlanConnection(ctx, db.DeleteCodexPlanConnectionParams{WorkspaceID: ws, BotID: bot}); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	if !conn.KekID.Valid {
		return false, nil
	}
	tokens, err := s.open(conn)
	if err != nil {
		return false, nil
	}
	revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 12*time.Second)
	defer cancel()
	return s.oauth.Revoke(revokeCtx, conn.ClientID, tokens.Refresh) == nil, nil
}

// CheckDesktop checks the current holder, scope, plan revision, and explicit egress policy.
func CheckDesktop(ctx context.Context, q *db.Queries, ws, bot pgtype.UUID, revision string) error {
	conn, err := q.GetCodexPlanConnection(ctx, db.GetCodexPlanConnectionParams{WorkspaceID: ws, BotID: bot})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if !conn.KekID.Valid || (revision != "" && uuid.UUID(conn.Revision.Bytes).String() != revision) {
		return ErrForbidden
	}
	return checkDesktop(ctx, q, conn)
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
	if !conn.KekID.Valid {
		return "", "", ErrReconnect
	}
	if err = checkDesktop(ctx, q, conn); err != nil {
		return "", "", err
	}
	tokens, err := s.open(conn)
	if err != nil {
		return "", "", err
	}
	if !s.now().Add(time.Minute).Before(tokens.Expires) {
		tokens, err = s.oauth.Refresh(ctx, conn.ClientID, tokens.Refresh)
		if err != nil && !errors.Is(err, ErrReconnect) {
			return "", "", err
		}
		// A terminal OAuth error clears the session but retains the registration.
		var update db.UpdateCodexPlanTokensParams
		update.WorkspaceID, update.BotID, update.Revision = ws, bp, conn.Revision
		if err == nil {
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
	return tokens.Access, uuid.UUID(conn.Revision.Bytes).String(), err
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

func associatedData(ws, bot, revision pgtype.UUID) []byte {
	return []byte("codex-plan:" + uuid.UUID(ws.Bytes).String() + ":" + uuid.UUID(bot.Bytes).String() + ":" + uuid.UUID(revision.Bytes).String())
}

func scope(ws, bot uuid.UUID) (pgtype.UUID, pgtype.UUID) {
	return pgtype.UUID{Bytes: ws, Valid: true}, pgtype.UUID{Bytes: bot, Valid: true}
}

func timestamp(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
func keyID(id string) pgtype.Text              { return pgtype.Text{String: id, Valid: true} }
