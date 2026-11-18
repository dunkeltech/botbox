// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/notify"
	"github.com/dunkeltech/botbox/backend/internal/workspaces"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	notificationPageDefault = 50
	notificationPageMax     = 100
)

func (s *server) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	ws, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	limit, ok := notificationLimit(r)
	if !ok {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "limit must be 1..100")
		return
	}
	cursorAt, cursorID, ok := notificationCursor(r)
	if !ok {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid notification cursor")
		return
	}
	// One extra row tells the handler whether another page exists.
	rows, err := s.queries.ListNotifications(r.Context(), db.ListNotificationsParams{
		WorkspaceID: ws.ID,
		UserID:      toPGUUID(claims.Subject),
		CursorAt:    cursorAt,
		CursorID:    cursorID,
		RowLimit:    limit + 1, // #nosec G115 -- limit is at most notificationPageMax
	})
	if err != nil {
		s.logger.Error("list notifications", "err", err)
		s.chatFailure(w)
		return
	}
	page := rows
	var next *string
	if len(rows) > int(limit) {
		page = rows[:limit]
		last := page[len(page)-1]
		encoded := encodeNotificationCursor(last.CreatedAt.Time, fromPGUUID(last.ID))
		next = &encoded
	}
	out := apigen.NotificationList{Notifications: make([]apigen.Notification, 0, len(page)), NextCursor: next}
	for _, row := range page {
		out.Notifications = append(out.Notifications, presentNotification(row))
	}
	writeJSON(w, s.logger, http.StatusOK, out)
}

func (s *server) handleMarkNotificationRead(w http.ResponseWriter, r *http.Request) {
	ws, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "notificationID"))
	if err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "validation_error", "invalid notification identifier")
		return
	}
	// The user filter makes another person's id and a cross-workspace id both 404.
	n, err := s.queries.MarkNotificationRead(r.Context(), db.MarkNotificationReadParams{
		WorkspaceID: ws.ID,
		ID:          toPGUUID(id),
		UserID:      toPGUUID(claims.Subject),
	})
	if err != nil {
		s.logger.Error("mark notification read", "err", err)
		s.chatFailure(w)
		return
	}
	if n == 0 {
		writeError(w, s.logger, http.StatusNotFound, "notification.not_found", "notification not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleMarkNotificationsRead(w http.ResponseWriter, r *http.Request) {
	ws, claims, _, ok := s.loadWorkspaceMembership(w, r, workspaces.ActionViewWorkspace)
	if !ok {
		return
	}
	if err := s.queries.MarkAllNotificationsRead(r.Context(), db.MarkAllNotificationsReadParams{
		WorkspaceID: ws.ID,
		UserID:      toPGUUID(claims.Subject),
	}); err != nil {
		s.logger.Error("mark notifications read", "err", err)
		s.chatFailure(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// signalNotifications publishes ids on the conversation channel. A failure is
// logged. The list endpoint is the source of truth when a client misses it.
func (s *server) signalNotifications(ctx context.Context, workspaceID, conversationID uuid.UUID, ids []uuid.UUID) {
	if len(ids) == 0 || conversationID == uuid.Nil {
		return
	}
	publisher, ok := s.turnQueue.(interface {
		PublishConversation(context.Context, uuid.UUID, uuid.UUID, []byte) error
	})
	if !ok {
		return
	}
	data, err := notify.MarshalSignal(conversationID, ids)
	if err != nil {
		s.logger.Error("encode notification signal", "err", err)
		return
	}
	if err = publisher.PublishConversation(ctx, workspaceID, conversationID, data); err != nil {
		s.logger.Error("publish notification signal", "err", err)
	}
}

func presentNotification(row db.Notification) apigen.Notification {
	out := apigen.Notification{
		Id:        fromPGUUID(row.ID),
		Kind:      apigen.NotificationKind(row.Kind),
		CreatedAt: row.CreatedAt.Time,
	}
	if row.ConversationID.Valid {
		id := fromPGUUID(row.ConversationID)
		out.ConversationId = &id
	}
	if row.MessageID.Valid {
		id := fromPGUUID(row.MessageID)
		out.MessageId = &id
	}
	if row.ApprovalID.Valid {
		id := fromPGUUID(row.ApprovalID)
		out.ApprovalId = &id
	}
	if row.ReadAt.Valid {
		readAt := row.ReadAt.Time
		out.ReadAt = &readAt
	}
	return out
}

func notificationLimit(r *http.Request) (int32, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return notificationPageDefault, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > notificationPageMax {
		return 0, false
	}
	return int32(n), true // #nosec G109 -- rejected values outside 1..100 above
}

func notificationCursor(r *http.Request) (pgtype.Timestamptz, pgtype.UUID, bool) {
	// Query() drops a broken escape and would treat it as the first page.
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return pgtype.Timestamptz{}, pgtype.UUID{}, false
	}
	raw := strings.TrimSpace(values.Get("cursor"))
	if raw == "" {
		return pgtype.Timestamptz{}, pgtype.UUID{}, true
	}
	created, id, err := decodeNotificationCursor(raw)
	if err != nil {
		return pgtype.Timestamptz{}, pgtype.UUID{}, false
	}
	return pgtype.Timestamptz{Time: created, Valid: true}, toPGUUID(id), true
}

func encodeNotificationCursor(created time.Time, id uuid.UUID) string {
	raw := created.UTC().Format(time.RFC3339Nano) + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeNotificationCursor(cursor string) (time.Time, uuid.UUID, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	stamp, idText, ok := strings.Cut(string(decoded), "|")
	if !ok {
		return time.Time{}, uuid.Nil, errors.New("invalid cursor")
	}
	created, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	id, err := uuid.Parse(idText)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	return created, id, nil
}
