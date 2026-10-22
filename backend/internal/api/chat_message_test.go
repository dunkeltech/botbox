package api

import (
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestChatMessageKeepsTheSpeaker(t *testing.T) {
	user := uuid.New()
	bot := uuid.New()
	human := chatMessage(db.Message{
		ID:           pgtype.UUID{Bytes: uuid.New(), Valid: true},
		SenderType:   "user",
		SenderUserID: pgtype.UUID{Bytes: user, Valid: true},
		Kind:         "text",
		Content:      "hi",
		CreatedAt:    pgtype.Timestamptz{Time: time.Unix(0, 0).UTC(), Valid: true},
	})
	if human.SenderUserId == nil || *human.SenderUserId != user || human.SenderBotId != nil {
		t.Fatalf("human=%v %v", human.SenderUserId, human.SenderBotId)
	}
	machine := chatMessage(db.Message{
		ID:          pgtype.UUID{Bytes: uuid.New(), Valid: true},
		SenderType:  "bot",
		SenderBotID: pgtype.UUID{Bytes: bot, Valid: true},
		Kind:        "text",
		CreatedAt:   pgtype.Timestamptz{Time: time.Unix(0, 0).UTC(), Valid: true},
	})
	if machine.SenderBotId == nil || *machine.SenderBotId != bot || machine.SenderUserId != nil {
		t.Fatalf("bot=%v %v", machine.SenderBotId, machine.SenderUserId)
	}
	notice := chatMessage(db.Message{
		ID:         pgtype.UUID{Bytes: uuid.New(), Valid: true},
		SenderType: "system",
		Kind:       "system",
		CreatedAt:  pgtype.Timestamptz{Time: time.Unix(0, 0).UTC(), Valid: true},
	})
	if notice.SenderUserId != nil || notice.SenderBotId != nil {
		t.Fatal("system row named a speaker")
	}
}
