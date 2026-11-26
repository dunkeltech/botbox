// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/google/uuid"
)

// ChatEvents distributes conversation events across API instances.
type ChatEvents interface {
	SubscribeConversation(uuid.UUID, uuid.UUID, func([]byte)) (func(), error)
}

func (s *server) handleChatEvents(w http.ResponseWriter, r *http.Request) {
	c, ok := s.privateConversation(w, r, false)
	if !ok {
		return
	}
	if s.chatEvents == nil {
		s.chatFailure(w)
		return
	}
	// A slow client is disconnected and reloads persisted history on reconnect.
	messages := make(chan []byte, 64)
	overflow := make(chan struct{}, 1)
	unsubscribe, err := s.chatEvents.SubscribeConversation(fromPGUUID(c.WorkspaceID), fromPGUUID(c.ID), func(data []byte) {
		select {
		case messages <- append([]byte(nil), data...):
		default:
			select {
			case overflow <- struct{}{}:
			default:
			}
		}
	})
	if err != nil {
		s.chatFailure(w)
		return
	}
	defer unsubscribe()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:   []string{"botbox.events"},
		OriginPatterns: wsOriginPatterns(s.publicBaseURL),
	})
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	claims, _ := claimsFromContext(r.Context())
	deadline := s.now().Add(10 * time.Minute)
	if claims.ExpiresAt.Before(deadline) {
		deadline = claims.ExpiresAt
	}
	ctx, cancel := context.WithTimeout(r.Context(), deadline.Sub(s.now()))
	defer cancel()
	ctx = conn.CloseRead(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-overflow:
			_ = conn.Close(websocket.StatusPolicyViolation, "reload history")
			return
		case data := <-messages:
			_, err = s.queries.GetPrivateConversation(ctx, db.GetPrivateConversationParams{WorkspaceID: c.WorkspaceID, ID: c.ID, UserID: c.UserID})
			if err != nil {
				_ = conn.Close(websocket.StatusPolicyViolation, "conversation access ended")
				return
			}
			if err = conn.Write(ctx, websocket.MessageText, data); err != nil {
				return
			}
		}
	}
}
