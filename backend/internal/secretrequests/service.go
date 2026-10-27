// Package secretrequests implements durable, zero-context secret request
// orchestration for CLI HTTPS credential adapters and gated browser capabilities.
package secretrequests

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/authbroker"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Supported secret request kinds, grant scopes, lifecycle states, and bounded limits.
const (
	KindCLIEgress    = "cli_egress"
	KindBrowserLogin = "browser_login"

	ScopeEphemeral       = "ephemeral"
	ScopeBotSecret       = "bot_secret"
	ScopeWorkspaceSecret = "workspace_secret"

	StatusPending    = "pending"
	StatusFulfilling = "fulfilling"
	StatusFulfilled  = "fulfilled"
	StatusDenied     = "denied"
	StatusExpired    = "expired"
	StatusCancelled  = "cancelled"
	StatusFailed     = "failed"

	MaxPendingPerBot               int64 = 5
	MaxRequestsPerBotPerHour       int64 = 20
	MaxRequestsPerWorkspacePerHour int64 = 60
	MaxReasonBytes                       = 500
	MaxSecretBytes                       = 16384
	DefaultRequestTTL                    = 24 * time.Hour
	DefaultEphemeralLeaseTTL             = 15 * time.Minute
)

// Domain errors returned by secretrequests lifecycle transitions and validation gates.
var (
	ErrUnsupportedCapability = errors.New("unsupported secret request capability")
	ErrInvalidInput          = errors.New("invalid secret request input")
	ErrRateLimited           = errors.New("secret request rate or pending limit reached")
	ErrNotFound              = errors.New("secret request not found")
	ErrConflict              = errors.New("secret request state conflict")
	ErrSecretRuleConflict    = errors.New("existing secret rule conflicts with request")
	ErrHostNotAllowed        = errors.New("destination host is not on bot egress allowlist")
	ErrForbidden             = errors.New("insufficient role for requested secret operation")
)

// CardPayload is the non-secret public metadata stored on a secret_request chat card.
type CardPayload struct {
	SecretRequestID    string `json:"secret_request_id"`
	Kind               string `json:"kind"`
	Status             string `json:"status"`
	AdapterID          string `json:"adapter_id"`
	AdapterLabel       string `json:"adapter_label,omitempty"`
	TargetOrigin       string `json:"target_origin"`
	HeaderScheme       string `json:"header_scheme,omitempty"`
	EnvPlaceholderName string `json:"env_placeholder_name,omitempty"`
	PlaceholderValue   string `json:"placeholder_value,omitempty"`
	Reason             string `json:"reason"`
	GrantedScope       string `json:"granted_scope,omitempty"`
	ExpiresAt          string `json:"expires_at,omitempty"`
	ErrorCode          string `json:"error_code,omitempty"`
	BrokerEnabled      bool   `json:"broker_enabled,omitempty"`
	BrokerSessionID    string `json:"broker_session_id,omitempty"`
	TakeoverActive     bool   `json:"takeover_active,omitempty"`
}

// CreateInput captures validated bot parameters for requesting a secret.
type CreateInput struct {
	WorkspaceID        uuid.UUID
	BotID              uuid.UUID
	ConversationID     uuid.UUID
	TurnID             uuid.UUID
	ToolCallID         string
	Kind               string
	AdapterID          string
	TargetOrigin       string
	Reason             string
	EnvPlaceholderName string
	UsernameHint       string
	Broker             authbroker.Client
	Now                time.Time
}

// CreateResult holds the created secret request row and chat card message.
type CreateResult struct {
	Request db.SecretRequest
	Message db.Message
	Card    CardPayload
}

// FulfillInput captures human submission parameters for a pending secret request.
type FulfillInput struct {
	WorkspaceID     uuid.UUID
	RequestID       uuid.UUID
	ActorUserID     uuid.UUID
	SecretValue     string
	Username        string
	Scope           string
	ReplaceExisting bool
	CanReplaceBot   bool
	CanWorkspace    bool
	Broker          authbroker.Client
	Now             time.Time
}

// FulfillResult holds the completed request and non-secret continuation details.
type FulfillResult struct {
	Request            db.SecretRequest
	Card               CardPayload
	ContinuationPrompt string
	TargetHost         string
	RequiresMFA        bool
}

// DenyInput captures human denial parameters for a pending secret request.
type DenyInput struct {
	WorkspaceID uuid.UUID
	RequestID   uuid.UUID
	ActorUserID uuid.UUID
	Broker      authbroker.Client
	Now         time.Time
}

// BrokerTakeoverInput identifies the user operating human broker takeover on a secret request.
type BrokerTakeoverInput struct {
	WorkspaceID uuid.UUID
	RequestID   uuid.UUID
	ActorUserID uuid.UUID
	Now         time.Time
}

// BrokerTakeoverState describes non-secret takeover state synced across the broker and DB.
type BrokerTakeoverState struct {
	Request db.SecretRequest
	Session authbroker.SessionInfo
	Card    CardPayload
}

// DenyResult holds the denied request and non-secret continuation details.
type DenyResult struct {
	Request            db.SecretRequest
	Card               CardPayload
	ContinuationPrompt string
}

// CreateRequest validates a bot's secret request, enforces capability & rate gates,
// and atomically creates the request, chat card message, and waiting_secret pause state.
func CreateRequest(ctx context.Context, pool *pgxpool.Pool, in CreateInput) (CreateResult, error) {
	if pool == nil {
		return CreateResult{}, errors.New("database pool is required")
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	kind := strings.TrimSpace(strings.ToLower(in.Kind))
	if kind == KindBrowserLogin {
		return createBrowserLoginRequest(ctx, pool, in, now)
	}
	if kind != KindCLIEgress {
		return CreateResult{}, fmt.Errorf("%w: unsupported kind", ErrInvalidInput)
	}

	adapter, ok := mitmproxy.LookupAdapter(in.AdapterID)
	if !ok {
		return CreateResult{}, fmt.Errorf("%w: unsupported adapter", ErrInvalidInput)
	}

	reason := strings.TrimSpace(in.Reason)
	if reason == "" || len(reason) > MaxReasonBytes {
		return CreateResult{}, fmt.Errorf("%w: reason must be 1..%d bytes", ErrInvalidInput, MaxReasonBytes)
	}

	envName := strings.TrimSpace(in.EnvPlaceholderName)
	if envName != "" {
		if err := mitmproxy.ValidateEnvPlaceholderName(envName); err != nil {
			return CreateResult{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
	}

	targetOrigin := adapter.CanonicalOrigin()
	if strings.TrimSpace(in.TargetOrigin) != "" {
		canon, host, port, err := mitmproxy.CanonicalHTTPSOrigin(in.TargetOrigin)
		if err != nil {
			return CreateResult{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
		if host != adapter.Host || port != adapter.Port {
			return CreateResult{}, fmt.Errorf("%w: target origin does not match adapter host", ErrInvalidInput)
		}
		targetOrigin = canon
	}

	usernameHint := strings.TrimSpace(in.UsernameHint)
	if len(usernameHint) > 255 {
		return CreateResult{}, fmt.Errorf("%w: username hint too long", ErrInvalidInput)
	}

	reqID := uuid.New()
	placeholderVal := ""
	if envName != "" {
		suffix, err := randomHex(6)
		if err != nil {
			return CreateResult{}, err
		}
		placeholderVal = "botbox-placeholder-" + suffix
	}

	expiresAt := now.Add(DefaultRequestTTL)
	card := CardPayload{
		SecretRequestID:    reqID.String(),
		Kind:               kind,
		Status:             StatusPending,
		AdapterID:          adapter.ID,
		AdapterLabel:       adapter.Label,
		TargetOrigin:       targetOrigin,
		HeaderScheme:       formatAdapterSchemeSummary(adapter),
		EnvPlaceholderName: envName,
		PlaceholderValue:   placeholderVal,
		Reason:             reason,
		ExpiresAt:          expiresAt.UTC().Format(time.RFC3339),
	}
	cardJSON, err := json.Marshal(card)
	if err != nil {
		return CreateResult{}, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return CreateResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)

	wsPG := toPGUUID(in.WorkspaceID)
	botPG := toPGUUID(in.BotID)
	convPG := toPGUUID(in.ConversationID)
	turnPG := toPGUUID(in.TurnID)

	pendingCount, err := q.CountPendingSecretRequestsForBot(ctx, db.CountPendingSecretRequestsForBotParams{
		WorkspaceID: wsPG,
		BotID:       botPG,
		AsOf:        pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return CreateResult{}, err
	}
	if pendingCount >= MaxPendingPerBot {
		return CreateResult{}, ErrRateLimited
	}

	hourAgo := pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}
	botRecent, err := q.CountRecentSecretRequestsForBot(ctx, db.CountRecentSecretRequestsForBotParams{
		WorkspaceID: wsPG,
		BotID:       botPG,
		Since:       hourAgo,
	})
	if err != nil {
		return CreateResult{}, err
	}
	if botRecent >= MaxRequestsPerBotPerHour {
		return CreateResult{}, ErrRateLimited
	}

	wsRecent, err := q.CountRecentSecretRequestsForWorkspace(ctx, db.CountRecentSecretRequestsForWorkspaceParams{
		WorkspaceID: wsPG,
		Since:       hourAgo,
	})
	if err != nil {
		return CreateResult{}, err
	}
	if wsRecent >= MaxRequestsPerWorkspacePerHour {
		return CreateResult{}, ErrRateLimited
	}

	reqRow, err := q.CreateSecretRequest(ctx, db.CreateSecretRequestParams{
		ID:                 toPGUUID(reqID),
		WorkspaceID:        wsPG,
		BotID:              botPG,
		ConversationID:     convPG,
		MessageID:          pgtype.UUID{},
		TurnID:             turnPG,
		ToolCallID:         in.ToolCallID,
		Kind:               kind,
		AdapterID:          adapter.ID,
		TargetOrigin:       targetOrigin,
		Reason:             reason,
		EnvPlaceholderName: envName,
		PlaceholderValue:   placeholderVal,
		UsernameHint:       usernameHint,
		ExpiresAt:          pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		return CreateResult{}, err
	}

	msgID := uuid.New()
	msgRow, err := q.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ID:             toPGUUID(msgID),
		WorkspaceID:    wsPG,
		ConversationID: convPG,
		SenderType:     "bot",
		SenderBotID:    botPG,
		Kind:           "secret_request",
		Content:        reason,
		Metadata:       cardJSON,
	})
	if err != nil {
		return CreateResult{}, err
	}
	if err = q.AttachSecretRequestMessage(ctx, db.AttachSecretRequestMessageParams{
		WorkspaceID: wsPG,
		ID:          toPGUUID(reqID),
		MessageID:   toPGUUID(msgID),
	}); err != nil {
		return CreateResult{}, err
	}
	reqRow.MessageID = toPGUUID(msgID)

	if err = q.PauseGroupForSecretRequest(ctx, db.PauseGroupForSecretRequestParams{
		WorkspaceID: wsPG,
		ID:          convPG,
	}); err != nil {
		return CreateResult{}, err
	}
	if _, err = q.SetBotTurnWaitingSecret(ctx, db.SetBotTurnWaitingSecretParams{
		WorkspaceID: wsPG,
		ID:          turnPG,
	}); err != nil {
		return CreateResult{}, err
	}

	if err = tx.Commit(ctx); err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Request: reqRow, Message: msgRow, Card: card}, nil
}

// FulfillRequest validates RBAC, capability, host allowlist, conflicts, encrypts the secret
// outside the DB transaction after claiming the request, persists the grant, and marks the turn queued.
func FulfillRequest(ctx context.Context, pool *pgxpool.Pool, ring *secrets.KeyRing, in FulfillInput) (FulfillResult, error) {
	if pool == nil || ring == nil {
		return FulfillResult{}, errors.New("database pool and key ring are required")
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	secretBytes := []byte(in.SecretValue)
	defer clear(secretBytes)
	if len(strings.TrimSpace(in.SecretValue)) == 0 || len(secretBytes) > MaxSecretBytes {
		return FulfillResult{}, fmt.Errorf("%w: secret value must be 1..%d bytes", ErrInvalidInput, MaxSecretBytes)
	}

	switch in.Scope {
	case ScopeEphemeral, ScopeBotSecret:
	case ScopeWorkspaceSecret:
		if !in.CanWorkspace {
			return FulfillResult{}, ErrForbidden
		}
	default:
		return FulfillResult{}, fmt.Errorf("%w: invalid scope", ErrInvalidInput)
	}

	wsPG := toPGUUID(in.WorkspaceID)
	reqPG := toPGUUID(in.RequestID)
	actorPG := toPGUUID(in.ActorUserID)

	// Pre-flight read before claiming so invalid capability or missing request never mutates state.
	preRow, err := db.New(pool).GetSecretRequest(ctx, db.GetSecretRequestParams{WorkspaceID: wsPG, ID: reqPG})
	if errors.Is(err, pgx.ErrNoRows) {
		return FulfillResult{}, ErrNotFound
	}
	if err != nil {
		return FulfillResult{}, err
	}
	if preRow.Kind == KindBrowserLogin {
		return fulfillBrowserLoginRequest(ctx, pool, preRow, in, now)
	}
	if preRow.Kind != KindCLIEgress {
		return FulfillResult{}, fmt.Errorf("%w: unsupported kind", ErrInvalidInput)
	}
	adapter, ok := mitmproxy.LookupAdapter(preRow.AdapterID)
	if !ok {
		return FulfillResult{}, ErrUnsupportedCapability
	}
	if preRow.Status == StatusPending && preRow.ExpiresAt.Valid && !now.Before(preRow.ExpiresAt.Time) {
		_, _ = db.New(pool).ExpirePendingSecretRequest(ctx, db.ExpirePendingSecretRequestParams{
			WorkspaceID: wsPG,
			ID:          reqPG,
			AsOf:        pgtype.Timestamptz{Time: now, Valid: true},
		})
		return FulfillResult{}, ErrConflict
	}
	if preRow.Status != StatusPending {
		return FulfillResult{}, ErrConflict
	}

	// Verify bot egress allowlist does NOT implicitly expand on secret fulfillment.
	egressRow, err := db.New(pool).GetEgressRuleByWorkspaceAndBot(ctx, db.GetEgressRuleByWorkspaceAndBotParams{
		WorkspaceID: wsPG,
		BotID:       preRow.BotID,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return FulfillResult{}, err
	}
	if !hostAllowedByEgressRow(egressRow, err == nil, adapter.Host) {
		return FulfillResult{}, ErrHostNotAllowed
	}

	// Check existing persistent rule conflicts before claiming.
	switch in.Scope {
	case ScopeBotSecret:
		conflictID, cErr := db.New(pool).FindConflictingBotSecret(ctx, db.FindConflictingBotSecretParams{
			WorkspaceID: wsPG,
			BotID:       preRow.BotID,
			DomainMatch: adapter.Host,
			PathPrefix:  "",
			HeaderName:  adapter.HeaderName,
		})
		if cErr == nil {
			if !in.ReplaceExisting {
				return FulfillResult{}, ErrSecretRuleConflict
			}
			if !in.CanReplaceBot {
				return FulfillResult{}, ErrForbidden
			}
			_ = conflictID
		} else if !errors.Is(cErr, pgx.ErrNoRows) {
			return FulfillResult{}, cErr
		}
	case ScopeWorkspaceSecret:
		_, cErr := db.New(pool).FindConflictingWorkspaceSecret(ctx, db.FindConflictingWorkspaceSecretParams{
			WorkspaceID: wsPG,
			DomainMatch: adapter.Host,
			PathPrefix:  "",
			HeaderName:  adapter.HeaderName,
		})
		if cErr == nil && !in.ReplaceExisting {
			return FulfillResult{}, ErrSecretRuleConflict
		} else if cErr != nil && !errors.Is(cErr, pgx.ErrNoRows) {
			return FulfillResult{}, cErr
		}
	}

	opID := uuid.New()
	claimed, err := db.New(pool).ClaimSecretRequestForFulfill(ctx, db.ClaimSecretRequestForFulfillParams{
		WorkspaceID: wsPG,
		ID:          reqPG,
		OperationID: toPGUUID(opID),
		AsOf:        pgtype.Timestamptz{Time: now, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return FulfillResult{}, ErrConflict
	}
	if err != nil {
		return FulfillResult{}, err
	}

	markFailed := func(code string) {
		failCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = db.New(pool).FailSecretRequestClaim(failCtx, db.FailSecretRequestClaimParams{
			WorkspaceID: wsPG,
			ID:          reqPG,
			OperationID: toPGUUID(opID),
			ErrorCode:   code,
		})
	}

	grantID := uuid.New()
	aadScope := "lease"
	switch in.Scope {
	case ScopeBotSecret:
		aadScope = "bot"
	case ScopeWorkspaceSecret:
		aadScope = "workspace"
	}
	aad := secrets.SecretAAD(in.WorkspaceID, aadScope, grantID)
	ct, err := ring.Encrypt(secretBytes, aad)
	if err != nil {
		markFailed("secret_request.encryption_failed")
		return FulfillResult{}, err
	}

	username := strings.TrimSpace(in.Username)
	if adapter.FixedUsername != "" {
		username = adapter.FixedUsername
	}
	dbScheme := mapProxySchemeToDB(adapter.Scheme)

	tx, err := pool.Begin(ctx)
	if err != nil {
		markFailed("secret_request.db_error")
		return FulfillResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)

	switch in.Scope {
	case ScopeEphemeral:
		leaseExp := now.Add(DefaultEphemeralLeaseTTL)
		if claimed.ExpiresAt.Valid && claimed.ExpiresAt.Time.Before(leaseExp) {
			leaseExp = claimed.ExpiresAt.Time
		}
		if _, err = q.CreateSecretLease(ctx, db.CreateSecretLeaseParams{
			ID:               toPGUUID(grantID),
			WorkspaceID:      wsPG,
			BotID:            claimed.BotID,
			SecretRequestID:  reqPG,
			TurnID:           claimed.TurnID,
			ConversationID:   claimed.ConversationID,
			AdapterID:        adapter.ID,
			DomainMatch:      adapter.Host,
			Port:             adapter.Port,
			AuthScheme:       dbScheme,
			HeaderName:       adapter.HeaderName,
			Username:         username,
			PlaceholderEnv:   claimed.EnvPlaceholderName,
			PlaceholderValue: claimed.PlaceholderValue,
			KekID:            ct.KeyID,
			WrappedDek:       ct.WrappedDEK,
			EncryptedValue:   ct.Ciphertext,
			ExpiresAt:        pgtype.Timestamptz{Time: leaseExp, Valid: true},
			CreatedBy:        actorPG,
		}); err != nil {
			markFailed("secret_request.lease_insert_failed")
			return FulfillResult{}, err
		}

	case ScopeBotSecret:
		existingID, cErr := q.FindConflictingBotSecret(ctx, db.FindConflictingBotSecretParams{
			WorkspaceID: wsPG,
			BotID:       claimed.BotID,
			DomainMatch: adapter.Host,
			PathPrefix:  "",
			HeaderName:  adapter.HeaderName,
		})
		if cErr == nil {
			if !in.ReplaceExisting {
				markFailed("secret_request.conflict")
				return FulfillResult{}, ErrSecretRuleConflict
			}
			// Re-encrypt using existing secret ID so AAD stays bound to the primary key.
			existingUID := uuid.UUID(existingID.Bytes)
			repAAD := secrets.SecretAAD(in.WorkspaceID, "bot", existingUID)
			repCT, repErr := ring.Encrypt(secretBytes, repAAD)
			if repErr != nil {
				markFailed("secret_request.encryption_failed")
				return FulfillResult{}, repErr
			}
			if _, err = q.UpdateBotSecretByWorkspaceBotAndID(ctx, db.UpdateBotSecretByWorkspaceBotAndIDParams{
				WorkspaceID:     wsPG,
				BotID:           claimed.BotID,
				ID:              existingID,
				Name:            secretRuleName(adapter.ID, claimed.ID),
				DomainMatch:     adapter.Host,
				MatchSubdomains: false,
				PathPrefix:      "",
				AuthScheme:      dbScheme,
				HeaderName:      adapter.HeaderName,
				Username:        username,
				KekID:           repCT.KeyID,
				WrappedDek:      repCT.WrappedDEK,
				EncryptedValue:  repCT.Ciphertext,
				MaskedPreview:   "••••••••",
			}); err != nil {
				markFailed("secret_request.secret_update_failed")
				return FulfillResult{}, err
			}
		} else if errors.Is(cErr, pgx.ErrNoRows) {
			if _, err = q.CreateBotSecretWithAdapter(ctx, db.CreateBotSecretWithAdapterParams{
				ID:              toPGUUID(grantID),
				WorkspaceID:     wsPG,
				BotID:           claimed.BotID,
				Name:            secretRuleName(adapter.ID, claimed.ID),
				DomainMatch:     adapter.Host,
				MatchSubdomains: false,
				PathPrefix:      "",
				AuthScheme:      dbScheme,
				HeaderName:      adapter.HeaderName,
				Username:        username,
				AdapterID:       adapter.ID,
				KekID:           ct.KeyID,
				WrappedDek:      ct.WrappedDEK,
				EncryptedValue:  ct.Ciphertext,
				MaskedPreview:   "••••••••",
				CreatedBy:       actorPG,
			}); err != nil {
				markFailed("secret_request.secret_insert_failed")
				return FulfillResult{}, err
			}
		} else {
			markFailed("secret_request.db_error")
			return FulfillResult{}, cErr
		}

	case ScopeWorkspaceSecret:
		existingID, cErr := q.FindConflictingWorkspaceSecret(ctx, db.FindConflictingWorkspaceSecretParams{
			WorkspaceID: wsPG,
			DomainMatch: adapter.Host,
			PathPrefix:  "",
			HeaderName:  adapter.HeaderName,
		})
		if cErr == nil {
			if !in.ReplaceExisting {
				markFailed("secret_request.conflict")
				return FulfillResult{}, ErrSecretRuleConflict
			}
			existingUID := uuid.UUID(existingID.Bytes)
			repAAD := secrets.SecretAAD(in.WorkspaceID, "workspace", existingUID)
			repCT, repErr := ring.Encrypt(secretBytes, repAAD)
			if repErr != nil {
				markFailed("secret_request.encryption_failed")
				return FulfillResult{}, repErr
			}
			if _, err = q.UpdateWorkspaceSecretByWorkspaceAndID(ctx, db.UpdateWorkspaceSecretByWorkspaceAndIDParams{
				WorkspaceID:     wsPG,
				ID:              existingID,
				Name:            secretRuleName(adapter.ID, claimed.ID),
				DomainMatch:     adapter.Host,
				MatchSubdomains: false,
				PathPrefix:      "",
				AuthScheme:      dbScheme,
				HeaderName:      adapter.HeaderName,
				Username:        username,
				KekID:           repCT.KeyID,
				WrappedDek:      repCT.WrappedDEK,
				EncryptedValue:  repCT.Ciphertext,
				MaskedPreview:   "••••••••",
			}); err != nil {
				markFailed("secret_request.secret_update_failed")
				return FulfillResult{}, err
			}
		} else if errors.Is(cErr, pgx.ErrNoRows) {
			if _, err = q.CreateWorkspaceSecretWithAdapter(ctx, db.CreateWorkspaceSecretWithAdapterParams{
				ID:              toPGUUID(grantID),
				WorkspaceID:     wsPG,
				Name:            secretRuleName(adapter.ID, claimed.ID),
				DomainMatch:     adapter.Host,
				MatchSubdomains: false,
				PathPrefix:      "",
				AuthScheme:      dbScheme,
				HeaderName:      adapter.HeaderName,
				Username:        username,
				AdapterID:       adapter.ID,
				KekID:           ct.KeyID,
				WrappedDek:      ct.WrappedDEK,
				EncryptedValue:  ct.Ciphertext,
				MaskedPreview:   "••••••••",
				CreatedBy:       actorPG,
			}); err != nil {
				markFailed("secret_request.secret_insert_failed")
				return FulfillResult{}, err
			}
		} else {
			markFailed("secret_request.db_error")
			return FulfillResult{}, cErr
		}
	}

	completed, err := q.CompleteSecretRequestFulfill(ctx, db.CompleteSecretRequestFulfillParams{
		WorkspaceID:  wsPG,
		ID:           reqPG,
		OperationID:  toPGUUID(opID),
		GrantedScope: in.Scope,
		DecidedBy:    actorPG,
		DecidedAt:    pgtype.Timestamptz{Time: now, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return FulfillResult{}, ErrConflict
	}
	if err != nil {
		markFailed("secret_request.db_error")
		return FulfillResult{}, err
	}

	card := CardPayload{
		SecretRequestID:    uuid.UUID(completed.ID.Bytes).String(),
		Kind:               completed.Kind,
		Status:             StatusFulfilled,
		AdapterID:          adapter.ID,
		AdapterLabel:       adapter.Label,
		TargetOrigin:       completed.TargetOrigin,
		HeaderScheme:       formatAdapterSchemeSummary(adapter),
		EnvPlaceholderName: completed.EnvPlaceholderName,
		PlaceholderValue:   completed.PlaceholderValue,
		Reason:             completed.Reason,
		GrantedScope:       in.Scope,
	}
	cardJSON, err := json.Marshal(card)
	if err != nil {
		return FulfillResult{}, err
	}
	if completed.MessageID.Valid {
		if err = q.UpdateSecretRequestMessageCard(ctx, db.UpdateSecretRequestMessageCardParams{
			WorkspaceID: wsPG,
			ID:          completed.MessageID,
			Content:     completed.Reason,
			Metadata:    cardJSON,
		}); err != nil {
			return FulfillResult{}, err
		}
	}

	if _, err = q.ClearSecretRequiredPause(ctx, db.ClearSecretRequiredPauseParams{
		WorkspaceID: wsPG,
		ID:          completed.ConversationID,
	}); err != nil {
		return FulfillResult{}, err
	}

	continuation := fmt.Sprintf("Secret provided for adapter %s (%s); hidden from AI. Scope: %s.", adapter.ID, adapter.Host, in.Scope)
	if completed.EnvPlaceholderName != "" && completed.PlaceholderValue != "" {
		continuation += fmt.Sprintf(" Use environment placeholder %s=%s; the egress proxy injects the real credential outbound.", completed.EnvPlaceholderName, completed.PlaceholderValue)
	}
	if _, err = q.ResumeBotTurnFromSecret(ctx, db.ResumeBotTurnFromSecretParams{
		WorkspaceID:        wsPG,
		ID:                 completed.TurnID,
		ContinuationPrompt: continuation,
	}); err != nil {
		return FulfillResult{}, err
	}

	if err = tx.Commit(ctx); err != nil {
		return FulfillResult{}, err
	}
	return FulfillResult{
		Request:            completed,
		Card:               card,
		ContinuationPrompt: continuation,
		TargetHost:         adapter.Host,
	}, nil
}

// DenyRequest atomically marks a pending secret request as denied and queues turn continuation.
func DenyRequest(ctx context.Context, pool *pgxpool.Pool, in DenyInput) (DenyResult, error) {
	if pool == nil {
		return DenyResult{}, errors.New("database pool is required")
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	wsPG := toPGUUID(in.WorkspaceID)
	reqPG := toPGUUID(in.RequestID)
	actorPG := toPGUUID(in.ActorUserID)

	tx, err := pool.Begin(ctx)
	if err != nil {
		return DenyResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)

	existing, err := q.GetSecretRequestForUpdate(ctx, db.GetSecretRequestForUpdateParams{WorkspaceID: wsPG, ID: reqPG})
	if errors.Is(err, pgx.ErrNoRows) {
		return DenyResult{}, ErrNotFound
	}
	if err != nil {
		return DenyResult{}, err
	}
	if existing.Status == StatusPending && existing.ExpiresAt.Valid && !now.Before(existing.ExpiresAt.Time) {
		_, _ = q.ExpirePendingSecretRequest(ctx, db.ExpirePendingSecretRequestParams{
			WorkspaceID: wsPG,
			ID:          reqPG,
			AsOf:        pgtype.Timestamptz{Time: now, Valid: true},
		})
		_ = tx.Commit(ctx)
		return DenyResult{}, ErrConflict
	}
	if existing.Status != StatusPending {
		return DenyResult{}, ErrConflict
	}

	denied, err := q.DenySecretRequest(ctx, db.DenySecretRequestParams{
		WorkspaceID: wsPG,
		ID:          reqPG,
		DecidedBy:   actorPG,
		DecidedAt:   pgtype.Timestamptz{Time: now, Valid: true},
		AsOf:        pgtype.Timestamptz{Time: now, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return DenyResult{}, ErrConflict
	}
	if err != nil {
		return DenyResult{}, err
	}

	adapterLabel := denied.AdapterID
	headerScheme := ""
	brokerEnabled := false
	brokerSessionID := ""
	if denied.Kind == KindBrowserLogin {
		if la, ok := authbroker.LookupLoginAdapter(denied.AdapterID); ok {
			adapterLabel = la.Label
		}
		if denied.BrokerSessionID.Valid {
			brokerEnabled = true
			brokerSessionID = uuid.UUID(denied.BrokerSessionID.Bytes).String()
			_, _ = q.RevokeBrokerSession(ctx, db.RevokeBrokerSessionParams{
				WorkspaceID: wsPG,
				ID:          denied.BrokerSessionID,
				RevokedAt:   pgtype.Timestamptz{Time: now, Valid: true},
			})
		}
	} else if adapter, ok := mitmproxy.LookupAdapter(denied.AdapterID); ok {
		adapterLabel = adapter.Label
		headerScheme = formatAdapterSchemeSummary(adapter)
	}
	card := CardPayload{
		SecretRequestID:    uuid.UUID(denied.ID.Bytes).String(),
		Kind:               denied.Kind,
		Status:             StatusDenied,
		AdapterID:          denied.AdapterID,
		AdapterLabel:       adapterLabel,
		TargetOrigin:       denied.TargetOrigin,
		HeaderScheme:       headerScheme,
		EnvPlaceholderName: denied.EnvPlaceholderName,
		Reason:             denied.Reason,
		BrokerEnabled:      brokerEnabled,
		BrokerSessionID:    brokerSessionID,
	}
	cardJSON, err := json.Marshal(card)
	if err != nil {
		return DenyResult{}, err
	}
	if denied.MessageID.Valid {
		if err = q.UpdateSecretRequestMessageCard(ctx, db.UpdateSecretRequestMessageCardParams{
			WorkspaceID: wsPG,
			ID:          denied.MessageID,
			Content:     denied.Reason,
			Metadata:    cardJSON,
		}); err != nil {
			return DenyResult{}, err
		}
	}
	if _, err = q.ClearSecretRequiredPause(ctx, db.ClearSecretRequiredPauseParams{
		WorkspaceID: wsPG,
		ID:          denied.ConversationID,
	}); err != nil {
		return DenyResult{}, err
	}

	continuation := fmt.Sprintf("Secret request for %s was denied by a team member. Continue without this credential.", denied.AdapterID)
	if _, err = q.ResumeBotTurnFromSecret(ctx, db.ResumeBotTurnFromSecretParams{
		WorkspaceID:        wsPG,
		ID:                 denied.TurnID,
		ContinuationPrompt: continuation,
	}); err != nil {
		return DenyResult{}, err
	}

	if err = tx.Commit(ctx); err != nil {
		return DenyResult{}, err
	}
	if in.Broker != nil && denied.BrokerSessionID.Valid {
		_ = in.Broker.RevokeSession(ctx, in.WorkspaceID, uuid.UUID(denied.BrokerSessionID.Bytes))
	}
	return DenyResult{Request: denied, Card: card, ContinuationPrompt: continuation}, nil
}

// CancelForTurn cancels any pending/fulfilling secret requests bound to turnID, updates
// their card messages to cancelled, and clears any secret_required pause on their conversation.
func CancelForTurn(ctx context.Context, pool *pgxpool.Pool, workspaceID, turnID uuid.UUID) error {
	return CancelForTurnWithBroker(ctx, pool, nil, workspaceID, turnID)
}

// CancelForTurnWithBroker cancels pending requests for turnID and revokes any linked broker sessions.
func CancelForTurnWithBroker(ctx context.Context, pool *pgxpool.Pool, broker authbroker.Client, workspaceID, turnID uuid.UUID) error {
	if pool == nil {
		return nil
	}
	wsPG := toPGUUID(workspaceID)
	rows, err := db.New(pool).CancelPendingSecretRequestsForTurn(ctx, db.CancelPendingSecretRequestsForTurnParams{
		WorkspaceID: wsPG,
		TurnID:      toPGUUID(turnID),
	})
	if err != nil || len(rows) == 0 {
		return err
	}
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	for _, r := range rows {
		adapterLabel := r.AdapterID
		headerScheme := ""
		brokerEnabled := false
		brokerSessionID := ""
		if r.Kind == KindBrowserLogin {
			if la, ok := authbroker.LookupLoginAdapter(r.AdapterID); ok {
				adapterLabel = la.Label
			}
			if r.BrokerSessionID.Valid {
				brokerEnabled = true
				sessID := uuid.UUID(r.BrokerSessionID.Bytes)
				brokerSessionID = sessID.String()
				_, _ = db.New(pool).RevokeBrokerSession(ctx, db.RevokeBrokerSessionParams{
					WorkspaceID: wsPG,
					ID:          r.BrokerSessionID,
					RevokedAt:   now,
				})
				if broker != nil {
					_ = broker.RevokeSession(ctx, workspaceID, sessID)
				}
			}
		} else if ad, ok := mitmproxy.LookupAdapter(r.AdapterID); ok {
			adapterLabel = ad.Label
			headerScheme = formatAdapterSchemeSummary(ad)
		}
		card := CardPayload{
			SecretRequestID:    uuid.UUID(r.ID.Bytes).String(),
			Kind:               r.Kind,
			Status:             StatusCancelled,
			AdapterID:          r.AdapterID,
			AdapterLabel:       adapterLabel,
			TargetOrigin:       r.TargetOrigin,
			HeaderScheme:       headerScheme,
			EnvPlaceholderName: r.EnvPlaceholderName,
			Reason:             r.Reason,
			BrokerEnabled:      brokerEnabled,
			BrokerSessionID:    brokerSessionID,
		}
		if cardJSON, mErr := json.Marshal(card); mErr == nil && r.MessageID.Valid {
			_ = db.New(pool).UpdateSecretRequestMessageCard(ctx, db.UpdateSecretRequestMessageCardParams{
				WorkspaceID: wsPG,
				ID:          r.MessageID,
				Content:     r.Reason,
				Metadata:    cardJSON,
			})
		}
		_, _ = db.New(pool).ClearSecretRequiredPause(ctx, db.ClearSecretRequiredPauseParams{
			WorkspaceID: wsPG,
			ID:          r.ConversationID,
		})
	}
	return nil
}

func hostAllowedByEgressRow(row db.EgressRule, found bool, host string) bool {
	if !found {
		return false
	}
	allowed, _ := mitmproxy.EvaluateHostAccess(
		mitmproxy.EgressPolicy(row.Policy),
		row.AllowedHosts,
		row.BlockedHosts,
		host,
	)
	return allowed
}

func formatAdapterSchemeSummary(a mitmproxy.HTTPSAdapter) string {
	switch a.Scheme {
	case mitmproxy.SchemeBasic:
		return fmt.Sprintf("%s: Basic %s:<secret>", a.HeaderName, a.FixedUsername)
	case mitmproxy.SchemeRaw:
		return fmt.Sprintf("%s: <secret>", a.HeaderName)
	default:
		return fmt.Sprintf("%s: Bearer <secret>", a.HeaderName)
	}
}

func mapProxySchemeToDB(s mitmproxy.AuthScheme) string {
	switch s {
	case mitmproxy.SchemeBasic:
		return "basic"
	case mitmproxy.SchemeRaw:
		return "header"
	default:
		return "bearer"
	}
}

func secretRuleName(adapterID string, reqID pgtype.UUID) string {
	idStr := uuid.UUID(reqID.Bytes).String()
	if len(idStr) >= 8 {
		idStr = idStr[:8]
	}
	return fmt.Sprintf("%s-%s", adapterID, idStr)
}

func toPGUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: id != uuid.Nil}
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
