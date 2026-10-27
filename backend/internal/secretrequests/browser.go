package secretrequests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/authbroker"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func createBrowserLoginRequest(ctx context.Context, pool *pgxpool.Pool, in CreateInput, now time.Time) (CreateResult, error) {
	if in.Broker == nil {
		return CreateResult{}, ErrUnsupportedCapability
	}
	loginAdapter, ok := authbroker.LookupLoginAdapter(in.AdapterID)
	if !ok {
		return CreateResult{}, fmt.Errorf("%w: unsupported adapter", ErrInvalidInput)
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" || len(reason) > MaxReasonBytes {
		return CreateResult{}, fmt.Errorf("%w: reason must be 1..%d bytes", ErrInvalidInput, MaxReasonBytes)
	}
	if strings.TrimSpace(in.EnvPlaceholderName) != "" {
		return CreateResult{}, fmt.Errorf("%w: env_placeholder_name is not supported for browser_login", ErrInvalidInput)
	}
	targetOrigin := loginAdapter.Origin
	if strings.TrimSpace(in.TargetOrigin) != "" {
		canon, _, _, err := mitmproxy.CanonicalHTTPSOrigin(in.TargetOrigin)
		if err != nil {
			return CreateResult{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
		if canon != loginAdapter.Origin {
			return CreateResult{}, fmt.Errorf("%w: target origin does not match adapter origin", ErrInvalidInput)
		}
		targetOrigin = canon
	}
	usernameHint := strings.TrimSpace(in.UsernameHint)
	if len(usernameHint) > 255 {
		return CreateResult{}, fmt.Errorf("%w: username hint too long", ErrInvalidInput)
	}

	wsPG := toPGUUID(in.WorkspaceID)
	botPG := toPGUUID(in.BotID)
	convPG := toPGUUID(in.ConversationID)
	turnPG := toPGUUID(in.TurnID)

	if err := checkRateLimits(ctx, db.New(pool), wsPG, botPG, now); err != nil {
		return CreateResult{}, err
	}

	brokerSess, err := in.Broker.OpenSession(ctx, authbroker.OpenSessionInput{
		WorkspaceID: in.WorkspaceID,
		BotID:       in.BotID,
		AdapterID:   loginAdapter.ID,
	})
	if err != nil {
		return CreateResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			cleanCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = in.Broker.RevokeSession(cleanCtx, in.WorkspaceID, brokerSess.SessionID)
		}
	}()

	reqID := uuid.New()
	expiresAt := now.Add(DefaultRequestTTL)
	if !brokerSess.ExpiresAt.IsZero() && brokerSess.ExpiresAt.Before(expiresAt) {
		expiresAt = brokerSess.ExpiresAt
	}
	card := CardPayload{
		SecretRequestID: reqID.String(),
		Kind:            KindBrowserLogin,
		Status:          StatusPending,
		AdapterID:       loginAdapter.ID,
		AdapterLabel:    loginAdapter.Label,
		TargetOrigin:    targetOrigin,
		Reason:          reason,
		ExpiresAt:       expiresAt.UTC().Format(time.RFC3339),
		BrokerEnabled:   true,
		BrokerSessionID: brokerSess.SessionID.String(),
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

	if err := checkRateLimits(ctx, q, wsPG, botPG, now); err != nil {
		return CreateResult{}, err
	}

	reqRow, err := q.CreateSecretRequest(ctx, db.CreateSecretRequestParams{
		ID:                 toPGUUID(reqID),
		WorkspaceID:        wsPG,
		BotID:              botPG,
		ConversationID:     convPG,
		MessageID:          pgtype.UUID{},
		TurnID:             turnPG,
		ToolCallID:         in.ToolCallID,
		Kind:               KindBrowserLogin,
		AdapterID:          loginAdapter.ID,
		TargetOrigin:       targetOrigin,
		Reason:             reason,
		EnvPlaceholderName: "",
		PlaceholderValue:   "",
		UsernameHint:       usernameHint,
		ExpiresAt:          pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		return CreateResult{}, err
	}

	if _, err = q.CreateBrokerSession(ctx, db.CreateBrokerSessionParams{
		ID:                toPGUUID(brokerSess.SessionID),
		WorkspaceID:       wsPG,
		BotID:             botPG,
		ConversationID:    convPG,
		TurnID:            turnPG,
		AdapterID:         loginAdapter.ID,
		Origin:            loginAdapter.Origin,
		TargetID:          brokerSess.Binding.TargetID,
		FrameID:           brokerSess.Binding.FrameID,
		DocumentID:        brokerSess.Binding.DocumentID,
		FormAction:        brokerSess.Binding.FormAction,
		Status:            brokerSess.Status,
		CaptureSuppressed: true,
		ExpiresAt:         pgtype.Timestamptz{Time: brokerSess.ExpiresAt, Valid: true},
	}); err != nil {
		return CreateResult{}, err
	}

	reqRow, err = q.AttachBrokerBindingToSecretRequest(ctx, db.AttachBrokerBindingToSecretRequestParams{
		WorkspaceID:      wsPG,
		ID:               toPGUUID(reqID),
		BrokerSessionID:  toPGUUID(brokerSess.SessionID),
		BrokerTargetID:   brokerSess.Binding.TargetID,
		BrokerFrameID:    brokerSess.Binding.FrameID,
		BrokerDocumentID: brokerSess.Binding.DocumentID,
		BrokerFormAction: brokerSess.Binding.FormAction,
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
	committed = true
	return CreateResult{Request: reqRow, Message: msgRow, Card: card}, nil
}

func checkRateLimits(ctx context.Context, q *db.Queries, wsPG, botPG pgtype.UUID, now time.Time) error {
	pendingCount, err := q.CountPendingSecretRequestsForBot(ctx, db.CountPendingSecretRequestsForBotParams{
		WorkspaceID: wsPG,
		BotID:       botPG,
		AsOf:        pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return err
	}
	if pendingCount >= MaxPendingPerBot {
		return ErrRateLimited
	}
	hourAgo := pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}
	botRecent, err := q.CountRecentSecretRequestsForBot(ctx, db.CountRecentSecretRequestsForBotParams{
		WorkspaceID: wsPG,
		BotID:       botPG,
		Since:       hourAgo,
	})
	if err != nil {
		return err
	}
	if botRecent >= MaxRequestsPerBotPerHour {
		return ErrRateLimited
	}
	wsRecent, err := q.CountRecentSecretRequestsForWorkspace(ctx, db.CountRecentSecretRequestsForWorkspaceParams{
		WorkspaceID: wsPG,
		Since:       hourAgo,
	})
	if err != nil {
		return err
	}
	if wsRecent >= MaxRequestsPerWorkspacePerHour {
		return ErrRateLimited
	}
	return nil
}

func fulfillBrowserLoginRequest(
	ctx context.Context,
	pool *pgxpool.Pool,
	preRow db.SecretRequest,
	in FulfillInput,
	now time.Time,
) (FulfillResult, error) {
	if in.Broker == nil || !preRow.BrokerSessionID.Valid {
		return FulfillResult{}, ErrUnsupportedCapability
	}
	if in.Scope != ScopeEphemeral {
		return FulfillResult{}, fmt.Errorf("%w: browser_login requires ephemeral broker session scope", ErrInvalidInput)
	}
	loginAdapter, ok := authbroker.LookupLoginAdapter(preRow.AdapterID)
	if !ok {
		return FulfillResult{}, ErrUnsupportedCapability
	}
	wsPG := toPGUUID(in.WorkspaceID)
	reqPG := toPGUUID(in.RequestID)
	actorPG := toPGUUID(in.ActorUserID)

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

	sessID := uuid.UUID(claimed.BrokerSessionID.Bytes)
	botID := uuid.UUID(claimed.BotID.Bytes)
	subRes, err := in.Broker.SubmitCredentials(ctx, authbroker.SubmitCredentialsInput{
		WorkspaceID: in.WorkspaceID,
		BotID:       botID,
		SessionID:   sessID,
		AdapterID:   loginAdapter.ID,
		Expected: authbroker.TargetBinding{
			TargetID:   claimed.BrokerTargetID,
			FrameID:    claimed.BrokerFrameID,
			DocumentID: claimed.BrokerDocumentID,
			Origin:     claimed.TargetOrigin,
			FormAction: claimed.BrokerFormAction,
		},
		Username: strings.TrimSpace(in.Username),
		Secret:   in.SecretValue,
	})
	if err != nil {
		code := "secret_request.broker_error"
		if errors.Is(err, authbroker.ErrBindingMismatch) {
			code = "secret_request.binding_mismatch"
		} else if errors.Is(err, authbroker.ErrSessionExpired) {
			code = "secret_request.session_expired"
		} else if errors.Is(err, authbroker.ErrTakeoverActive) {
			code = "secret_request.takeover_active"
		}
		markFailed(code)
		_, _ = db.New(pool).RevokeBrokerSession(ctx, db.RevokeBrokerSessionParams{
			WorkspaceID: wsPG,
			ID:          claimed.BrokerSessionID,
			RevokedAt:   pgtype.Timestamptz{Time: now, Valid: true},
		})
		_ = in.Broker.RevokeSession(context.WithoutCancel(ctx), in.WorkspaceID, sessID)
		return FulfillResult{}, ErrConflict
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		markFailed("secret_request.db_error")
		return FulfillResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)

	if _, err = q.UpdateBrokerSessionState(ctx, db.UpdateBrokerSessionStateParams{
		WorkspaceID:       wsPG,
		ID:                claimed.BrokerSessionID,
		Status:            subRes.Session.Status,
		TargetID:          subRes.Session.Binding.TargetID,
		FrameID:           subRes.Session.Binding.FrameID,
		DocumentID:        subRes.Session.Binding.DocumentID,
		FormAction:        subRes.Session.Binding.FormAction,
		CaptureSuppressed: subRes.Session.CaptureSuppressed,
		TakeoverUserID:    pgtype.UUID{},
	}); err != nil {
		return FulfillResult{}, err
	}

	if subRes.Outcome.RequiresMFA {
		resetReq, rErr := q.ResetSecretRequestForMFA(ctx, db.ResetSecretRequestForMFAParams{
			WorkspaceID:      wsPG,
			ID:               reqPG,
			OperationID:      toPGUUID(opID),
			BrokerTargetID:   subRes.Session.Binding.TargetID,
			BrokerFrameID:    subRes.Session.Binding.FrameID,
			BrokerDocumentID: subRes.Session.Binding.DocumentID,
			BrokerFormAction: subRes.Session.Binding.FormAction,
			DecidedBy:        actorPG,
		})
		if errors.Is(rErr, pgx.ErrNoRows) {
			return FulfillResult{}, ErrConflict
		}
		if rErr != nil {
			return FulfillResult{}, rErr
		}
		card := CardPayload{
			SecretRequestID: uuid.UUID(resetReq.ID.Bytes).String(),
			Kind:            resetReq.Kind,
			Status:          StatusPending,
			AdapterID:       loginAdapter.ID,
			AdapterLabel:    loginAdapter.Label,
			TargetOrigin:    resetReq.TargetOrigin,
			Reason:          resetReq.Reason,
			ErrorCode:       "mfa_required",
			BrokerEnabled:   true,
			BrokerSessionID: sessID.String(),
		}
		if err := updateCardMessage(ctx, q, wsPG, resetReq.MessageID, resetReq.Reason, card); err != nil {
			return FulfillResult{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return FulfillResult{}, err
		}
		return FulfillResult{
			Request:     resetReq,
			Card:        card,
			TargetHost:  loginAdapter.Origin,
			RequiresMFA: true,
		}, nil
	}

	completed, err := q.CompleteSecretRequestFulfill(ctx, db.CompleteSecretRequestFulfillParams{
		WorkspaceID:  wsPG,
		ID:           reqPG,
		OperationID:  toPGUUID(opID),
		GrantedScope: ScopeEphemeral,
		DecidedBy:    actorPG,
		DecidedAt:    pgtype.Timestamptz{Time: now, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return FulfillResult{}, ErrConflict
	}
	if err != nil {
		return FulfillResult{}, err
	}

	card := CardPayload{
		SecretRequestID: uuid.UUID(completed.ID.Bytes).String(),
		Kind:            completed.Kind,
		Status:          StatusFulfilled,
		AdapterID:       loginAdapter.ID,
		AdapterLabel:    loginAdapter.Label,
		TargetOrigin:    completed.TargetOrigin,
		Reason:          completed.Reason,
		GrantedScope:    ScopeEphemeral,
		BrokerEnabled:   true,
		BrokerSessionID: sessID.String(),
	}
	if err := updateCardMessage(ctx, q, wsPG, completed.MessageID, completed.Reason, card); err != nil {
		return FulfillResult{}, err
	}
	if _, err = q.ClearSecretRequiredPause(ctx, db.ClearSecretRequiredPauseParams{
		WorkspaceID: wsPG,
		ID:          completed.ConversationID,
	}); err != nil {
		return FulfillResult{}, err
	}

	continuation := fmt.Sprintf(
		"Authenticated isolated broker session ready for adapter %s (%s); credentials remain hidden from AI. Continue using broker_session_id=%s.",
		loginAdapter.ID, loginAdapter.Origin, sessID.String(),
	)
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
		TargetHost:         loginAdapter.Origin,
	}, nil
}

// StartBrokerTakeover initiates a human-only takeover session on the isolated broker,
// locking out all bot browser calls and screenshots until CompleteBrokerTakeover finishes.
func StartBrokerTakeover(
	ctx context.Context,
	pool *pgxpool.Pool,
	broker authbroker.Client,
	in BrokerTakeoverInput,
) (BrokerTakeoverState, error) {
	if pool == nil {
		return BrokerTakeoverState{}, errors.New("database pool is required")
	}
	if broker == nil {
		return BrokerTakeoverState{}, ErrUnsupportedCapability
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	wsPG := toPGUUID(in.WorkspaceID)
	reqPG := toPGUUID(in.RequestID)
	reqRow, err := db.New(pool).GetSecretRequest(ctx, db.GetSecretRequestParams{WorkspaceID: wsPG, ID: reqPG})
	if errors.Is(err, pgx.ErrNoRows) {
		return BrokerTakeoverState{}, ErrNotFound
	}
	if err != nil {
		return BrokerTakeoverState{}, err
	}
	if reqRow.Kind != KindBrowserLogin || !reqRow.BrokerSessionID.Valid {
		return BrokerTakeoverState{}, ErrUnsupportedCapability
	}
	if reqRow.Status == StatusPending && reqRow.ExpiresAt.Valid && !now.Before(reqRow.ExpiresAt.Time) {
		return BrokerTakeoverState{}, ErrConflict
	}
	if reqRow.Status != StatusPending {
		return BrokerTakeoverState{}, ErrConflict
	}

	sessID := uuid.UUID(reqRow.BrokerSessionID.Bytes)
	sessInfo, err := broker.StartTakeover(ctx, in.WorkspaceID, sessID, in.ActorUserID)
	if err != nil {
		if errors.Is(err, authbroker.ErrTakeoverForbidden) {
			return BrokerTakeoverState{}, ErrForbidden
		}
		if errors.Is(err, authbroker.ErrSessionNotFound) {
			return BrokerTakeoverState{}, ErrNotFound
		}
		return BrokerTakeoverState{}, ErrConflict
	}

	label := reqRow.AdapterID
	if la, ok := authbroker.LookupLoginAdapter(reqRow.AdapterID); ok {
		label = la.Label
	}
	card := CardPayload{
		SecretRequestID: uuid.UUID(reqRow.ID.Bytes).String(),
		Kind:            reqRow.Kind,
		Status:          reqRow.Status,
		AdapterID:       reqRow.AdapterID,
		AdapterLabel:    label,
		TargetOrigin:    reqRow.TargetOrigin,
		Reason:          reqRow.Reason,
		ErrorCode:       reqRow.ErrorCode,
		BrokerEnabled:   true,
		BrokerSessionID: sessID.String(),
		TakeoverActive:  true,
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return BrokerTakeoverState{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)

	if _, err = q.UpdateBrokerSessionState(ctx, db.UpdateBrokerSessionStateParams{
		WorkspaceID:       wsPG,
		ID:                reqRow.BrokerSessionID,
		Status:            authbroker.StatusTakeover,
		TargetID:          sessInfo.Binding.TargetID,
		FrameID:           sessInfo.Binding.FrameID,
		DocumentID:        sessInfo.Binding.DocumentID,
		FormAction:        sessInfo.Binding.FormAction,
		CaptureSuppressed: true,
		TakeoverUserID:    toPGUUID(in.ActorUserID),
	}); err != nil {
		return BrokerTakeoverState{}, err
	}
	if err = updateCardMessage(ctx, q, wsPG, reqRow.MessageID, reqRow.Reason, card); err != nil {
		return BrokerTakeoverState{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return BrokerTakeoverState{}, err
	}

	return BrokerTakeoverState{Request: reqRow, Session: sessInfo, Card: card}, nil
}

// CompleteBrokerTakeover finishes manual MFA/CAPTCHA login inside the broker, marks the
// secret request fulfilled, clears conversation pause, and wakes the waiting bot turn.
func CompleteBrokerTakeover(
	ctx context.Context,
	pool *pgxpool.Pool,
	broker authbroker.Client,
	in BrokerTakeoverInput,
) (FulfillResult, error) {
	if pool == nil {
		return FulfillResult{}, errors.New("database pool is required")
	}
	if broker == nil {
		return FulfillResult{}, ErrUnsupportedCapability
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	wsPG := toPGUUID(in.WorkspaceID)
	reqPG := toPGUUID(in.RequestID)
	reqRow, err := db.New(pool).GetSecretRequest(ctx, db.GetSecretRequestParams{WorkspaceID: wsPG, ID: reqPG})
	if errors.Is(err, pgx.ErrNoRows) {
		return FulfillResult{}, ErrNotFound
	}
	if err != nil {
		return FulfillResult{}, err
	}
	if reqRow.Kind != KindBrowserLogin || !reqRow.BrokerSessionID.Valid {
		return FulfillResult{}, ErrUnsupportedCapability
	}
	if reqRow.Status != StatusPending && reqRow.Status != StatusFulfilling {
		return FulfillResult{}, ErrConflict
	}

	sessID := uuid.UUID(reqRow.BrokerSessionID.Bytes)
	sessInfo, err := broker.CompleteTakeover(ctx, in.WorkspaceID, sessID, in.ActorUserID)
	if err != nil {
		if errors.Is(err, authbroker.ErrTakeoverForbidden) {
			return FulfillResult{}, ErrForbidden
		}
		if errors.Is(err, authbroker.ErrSessionNotFound) {
			return FulfillResult{}, ErrNotFound
		}
		return FulfillResult{}, ErrConflict
	}

	label := reqRow.AdapterID
	if la, ok := authbroker.LookupLoginAdapter(reqRow.AdapterID); ok {
		label = la.Label
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return FulfillResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)

	if _, err = q.UpdateBrokerSessionState(ctx, db.UpdateBrokerSessionStateParams{
		WorkspaceID:       wsPG,
		ID:                reqRow.BrokerSessionID,
		Status:            authbroker.StatusAuthenticated,
		TargetID:          sessInfo.Binding.TargetID,
		FrameID:           sessInfo.Binding.FrameID,
		DocumentID:        sessInfo.Binding.DocumentID,
		FormAction:        sessInfo.Binding.FormAction,
		CaptureSuppressed: false,
		TakeoverUserID:    pgtype.UUID{},
	}); err != nil {
		return FulfillResult{}, err
	}

	completed, err := q.SetSecretRequestStatusFulfilledFromTakeover(ctx, db.SetSecretRequestStatusFulfilledFromTakeoverParams{
		WorkspaceID: wsPG,
		ID:          reqPG,
		DecidedBy:   toPGUUID(in.ActorUserID),
		DecidedAt:   pgtype.Timestamptz{Time: now, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return FulfillResult{}, ErrConflict
	}
	if err != nil {
		return FulfillResult{}, err
	}

	card := CardPayload{
		SecretRequestID: uuid.UUID(completed.ID.Bytes).String(),
		Kind:            completed.Kind,
		Status:          StatusFulfilled,
		AdapterID:       completed.AdapterID,
		AdapterLabel:    label,
		TargetOrigin:    completed.TargetOrigin,
		Reason:          completed.Reason,
		GrantedScope:    ScopeEphemeral,
		BrokerEnabled:   true,
		BrokerSessionID: sessID.String(),
		TakeoverActive:  false,
	}
	if err = updateCardMessage(ctx, q, wsPG, completed.MessageID, completed.Reason, card); err != nil {
		return FulfillResult{}, err
	}
	if _, err = q.ClearSecretRequiredPause(ctx, db.ClearSecretRequiredPauseParams{
		WorkspaceID: wsPG,
		ID:          completed.ConversationID,
	}); err != nil {
		return FulfillResult{}, err
	}

	continuation := fmt.Sprintf(
		"Authenticated isolated broker session ready after human verification for adapter %s (%s); credentials remain hidden from AI. Continue using broker_session_id=%s.",
		completed.AdapterID, completed.TargetOrigin, sessID.String(),
	)
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
		TargetHost:         completed.TargetOrigin,
	}, nil
}

func updateCardMessage(ctx context.Context, q *db.Queries, wsPG, msgPG pgtype.UUID, content string, card CardPayload) error {
	if !msgPG.Valid {
		return nil
	}
	cardJSON, err := json.Marshal(card)
	if err != nil {
		return err
	}
	return q.UpdateSecretRequestMessageCard(ctx, db.UpdateSecretRequestMessageCardParams{
		WorkspaceID: wsPG,
		ID:          msgPG,
		Content:     content,
		Metadata:    cardJSON,
	})
}
