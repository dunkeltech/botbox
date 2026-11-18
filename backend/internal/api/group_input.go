// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"

	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/jackc/pgx/v5/pgtype"
)

func (s *server) groupInputProgress(ctx context.Context, workspace, conversation pgtype.UUID, messages []db.Message) (map[pgtype.UUID]*apigen.GroupInputRequest, error) {
	var ids []pgtype.UUID
	for _, message := range messages {
		if message.SenderType == "bot" && message.Kind == "text" {
			ids = append(ids, message.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.queries.ListGroupInputProgress(ctx, db.ListGroupInputProgressParams{
		WorkspaceID: workspace, ConversationID: conversation, MessageIds: ids,
	})
	if err != nil {
		return nil, err
	}
	progress := make(map[pgtype.UUID]*apigen.GroupInputRequest)
	now := s.now()
	for _, row := range rows {
		request := progress[row.MessageID]
		if request == nil {
			request = &apigen.GroupInputRequest{State: apigen.GroupInputRequestState(row.State), Deadline: row.Deadline.Time, Required: row.Required}
			progress[row.MessageID] = request
		}
		state := apigen.GroupInputRecipientStateUnavailable
		switch {
		case row.Replied:
			state = apigen.GroupInputRecipientStateReplied
		case row.Active && row.State == "waiting" && now.Before(row.Deadline.Time):
			state = apigen.GroupInputRecipientStateWaiting
		}
		request.Recipients = append(request.Recipients, apigen.GroupInputRecipient{BotId: fromPGUUID(row.BotID), Name: row.BotName, State: state})
	}
	return progress, nil
}
