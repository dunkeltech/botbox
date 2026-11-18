// SPDX-License-Identifier: AGPL-3.0-only

package secretrequests

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/authbroker"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const fulfillmentCleanupTimeout = 5 * time.Second

type fulfillmentFailure struct {
	code  string
	cause error
}

func (e *fulfillmentFailure) Error() string { return e.code }
func (e *fulfillmentFailure) Unwrap() error { return e.cause }

func claimFulfillment(ctx context.Context, pool *pgxpool.Pool, workspace, request pgtype.UUID, now time.Time) (db.SecretRequest, error) {
	claimed, err := db.New(pool).ClaimSecretRequestForFulfill(ctx, db.ClaimSecretRequestForFulfillParams{
		WorkspaceID: workspace,
		ID:          request,
		OperationID: toPGUUID(uuid.New()),
		AsOf:        pgtype.Timestamptz{Time: now, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.SecretRequest{}, ErrConflict
	}
	return claimed, err
}

func rollbackFulfillment(ctx context.Context, tx pgx.Tx) {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fulfillmentCleanupTimeout)
	defer cancel()
	_ = tx.Rollback(rollbackCtx)
}

func recoverFulfillment(ctx context.Context, pool *pgxpool.Pool, broker authbroker.Client, request db.SecretRequest, code string, now time.Time) {
	// Fulfillment rolls back first. Recovery must survive cancellation of the submitting client.
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fulfillmentCleanupTimeout)
	changed, err := persistFulfillmentFailure(dbCtx, pool, request, code, now)
	cancel()
	if err != nil {
		slog.WarnContext(ctx, "secret request failure could not be persisted", "request_id", uuid.UUID(request.ID.Bytes), "code", code)
	}

	// The external submission may have succeeded before the DB write or response failed.
	// If the claim is known to have moved on, its newer state owns the broker session.
	if broker == nil || !request.BrokerSessionID.Valid || (!changed && err == nil) {
		return
	}
	brokerCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fulfillmentCleanupTimeout)
	defer cancel()
	if err := broker.RevokeSession(brokerCtx, uuid.UUID(request.WorkspaceID.Bytes), uuid.UUID(request.BrokerSessionID.Bytes)); err != nil && !errors.Is(err, authbroker.ErrSessionNotFound) {
		slog.WarnContext(ctx, "failed to revoke broker session after secret request failure", "request_id", uuid.UUID(request.ID.Bytes))
	}
}

func persistFulfillmentFailure(ctx context.Context, pool *pgxpool.Pool, request db.SecretRequest, code string, now time.Time) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackFulfillment(ctx, tx)
	q := db.New(tx)

	if request.BrokerSessionID.Valid {
		// Match the broker session and claim lock order used by browser completion.
		if _, err := q.RevokeBrokerSession(ctx, db.RevokeBrokerSessionParams{
			WorkspaceID: request.WorkspaceID,
			ID:          request.BrokerSessionID,
			RevokedAt:   pgtype.Timestamptz{Time: now, Valid: true},
		}); err != nil {
			return false, err
		}
	}
	rows, err := q.FailSecretRequestClaim(ctx, db.FailSecretRequestClaimParams{
		WorkspaceID: request.WorkspaceID, ID: request.ID, OperationID: request.OperationID, ErrorCode: code,
	})
	if err != nil || rows == 0 {
		return false, err
	}

	card := failedFulfillmentCard(request, code)
	if err := updateCardMessage(ctx, q, request.WorkspaceID, request.MessageID, request.Reason, card); err != nil {
		return false, err
	}
	// Check for other requests after serializing pause changes for this group.
	if _, err := q.GetGroupForUpdate(ctx, db.GetGroupForUpdateParams{
		WorkspaceID: request.WorkspaceID, ID: request.ConversationID,
	}); err == nil {
		if err := q.ClearSecretPauseAfterFulfillmentFailure(ctx, db.ClearSecretPauseAfterFulfillmentFailureParams{
			WorkspaceID: request.WorkspaceID,
			ID:          request.ConversationID,
			AsOf:        pgtype.Timestamptz{Time: now, Valid: true},
		}); err != nil {
			return false, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if err := q.FailWaitingSecretBotTurn(ctx, db.FailWaitingSecretBotTurnParams{
		WorkspaceID: request.WorkspaceID, ID: request.TurnID, ErrorCode: code,
	}); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func failedFulfillmentCard(request db.SecretRequest, code string) CardPayload {
	card := CardPayload{
		SecretRequestID: uuid.UUID(request.ID.Bytes).String(),
		Kind:            request.Kind,
		Status:          StatusFailed,
		AdapterID:       request.AdapterID,
		TargetOrigin:    request.TargetOrigin,
		Reason:          request.Reason,
		ErrorCode:       code,
	}
	if request.Kind == KindBrowserLogin {
		if adapter, ok := authbroker.LookupLoginAdapter(request.AdapterID); ok {
			card.AdapterLabel = adapter.Label
		}
	} else if adapter, ok := mitmproxy.LookupAdapter(request.AdapterID); ok {
		card.AdapterLabel = adapter.Label
		card.HeaderScheme = formatAdapterSchemeSummary(adapter)
	}
	return card
}
