// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/engine"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestGroupMentionAnswersAfterLargePlanningHistory(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	var members []any
	var lead uuid.UUID
	for _, name := range []string{"Yoda", "Nova", "Atlas", "Mace"} {
		bot := createPiBot(t, h, base, owner.AccessToken, name, strings.ToLower(name)+"-seed")
		if lead == uuid.Nil {
			lead = bot.Id
		}
		members = append(members, groupBot(bot.Id, "reviewer"))
	}
	if _, err := h.pool.Exec(context.Background(), "UPDATE bots SET description=$2 WHERE workspace_id=$1", ws, strings.Repeat("role ", 700)); err != nil {
		t.Fatal(err)
	}
	group := createGroup(t, h, base, owner.AccessToken, groupSettings{name: "Planning", lead: lead, turnLimit: 20, members: members})
	for range 4 {
		if _, err := h.queries.CreateChatMessage(context.Background(), db.CreateChatMessageParams{ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, WorkspaceID: pgtype.UUID{Bytes: ws, Valid: true}, ConversationID: pgtype.UUID{Bytes: group.Id, Valid: true}, SenderType: "bot", SenderBotID: pgtype.UUID{Bytes: lead, Valid: true}, Kind: "text", Content: strings.Repeat("Planning detail. ", 950) + "Which country should we use?", Metadata: []byte("{}")}); err != nil {
			t.Fatal(err)
		}
	}
	const answer = "@Yoda use Germany for the plan."
	postGroup(t, h, base, owner.AccessToken, group.Id, answer)
	calls := 0
	worker := &engine.Worker{Pool: h.pool, Execute: func(_ context.Context, s engine.Session, emit func(engine.Event) error) error {
		calls++
		if len(s.Preface) > 48*1024 {
			t.Errorf("preface exceeds byte limit: %d", len(s.Preface))
		}
		if !strings.Contains(s.Preface, "Which country should we use?") {
			t.Error("lost the most recent question")
		}
		if len(s.Messages) == 0 || s.Messages[0].Content != answer {
			t.Error("lost the current human answer")
		}
		return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "Thanks, I will use Germany."})
	}}
	if err := worker.Process(context.Background(), queue.jobs[0]); err != nil {
		t.Fatal(err)
	}
	history := groupHistory(t, h, base, owner.AccessToken, group.Id)
	if calls != 1 || !historyHas(history, "Thanks, I will use Germany.") {
		t.Fatal("group follow-up produced no reply")
	}
	if len(history.Turns) != 1 || history.Turns[0].Status != "completed" {
		t.Fatalf("turns=%+v", history.Turns)
	}
}
