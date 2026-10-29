package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
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

func TestChatMessageExtractsSanitizedToolMeta(t *testing.T) {
	bot := uuid.New()
	turnID := uuid.NewString()
	call := chatMessage(db.Message{
		ID:          pgtype.UUID{Bytes: uuid.New(), Valid: true},
		SenderType:  "bot",
		SenderBotID: pgtype.UUID{Bytes: bot, Valid: true},
		Kind:        "tool",
		Content:     "Bash\n{\"command\":\"echo hi\"}",
		Metadata:    []byte(`{"Kind":"tool_call","TurnID":"` + turnID + `","ToolID":"toolu_123","ToolName":"Bash","ToolInput":{"command":"SECRET_ARG_VALUE"},"SessionID":"sess-secret"}`),
		CreatedAt:   pgtype.Timestamptz{Time: time.Unix(0, 0).UTC(), Valid: true},
	})
	if call.Tool == nil {
		t.Fatal("expected tool metadata on call")
	}
	if call.Tool.Phase != apigen.Call || call.Tool.IsError {
		t.Fatalf("unexpected call meta=%+v", call.Tool)
	}
	if call.Tool.TurnId == nil || *call.Tool.TurnId != turnID {
		t.Fatalf("turnId=%v", call.Tool.TurnId)
	}
	if call.Tool.ToolCallId == nil || *call.Tool.ToolCallId != "toolu_123" {
		t.Fatalf("toolCallId=%v", call.Tool.ToolCallId)
	}
	encoded, err := json.Marshal(call.Tool)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "SECRET_ARG_VALUE") || strings.Contains(string(encoded), "sess-secret") {
		t.Fatalf("tool metadata leaked raw fields: %s", encoded)
	}

	result := chatMessage(db.Message{
		ID:          pgtype.UUID{Bytes: uuid.New(), Valid: true},
		SenderType:  "bot",
		SenderBotID: pgtype.UUID{Bytes: bot, Valid: true},
		Kind:        "tool",
		Content:     "blocked",
		Metadata:    []byte(`{"Kind":"tool_result","TurnID":"` + turnID + `","ToolID":"toolu_123","IsError":true,"ErrorCode":"egress.blocked"}`),
		CreatedAt:   pgtype.Timestamptz{Time: time.Unix(1, 0).UTC(), Valid: true},
	})
	if result.Tool == nil || result.Tool.Phase != apigen.Result || !result.Tool.IsError {
		t.Fatalf("unexpected result meta=%+v", result.Tool)
	}
	if result.Tool.ErrorCode == nil || *result.Tool.ErrorCode != "egress.blocked" {
		t.Fatalf("errorCode=%v", result.Tool.ErrorCode)
	}

	ignored := chatMessage(db.Message{
		ID:         pgtype.UUID{Bytes: uuid.New(), Valid: true},
		SenderType: "bot",
		Kind:       "tool",
		Content:    "note",
		Metadata:   []byte(`{"Kind":"unknown"}`),
		CreatedAt:  pgtype.Timestamptz{Time: time.Unix(2, 0).UTC(), Valid: true},
	})
	if ignored.Tool != nil {
		t.Fatalf("expected nil tool meta for unknown kind, got %+v", ignored.Tool)
	}
}
