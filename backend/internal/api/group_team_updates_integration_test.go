package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/groups"
	"github.com/dunkeltech/botbox/backend/internal/mcpproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestGroupTeamUpdatesWhileWorkIsRunning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "viewer@example.com", "viewer", "Viewer", "CorrectHorse123!")
	yoda := createPiBot(t, h, base, owner.AccessToken, "Yoda", "yoda")
	babu := createPiBot(t, h, base, owner.AccessToken, "Babu Frik", "babu")
	createPiBot(t, h, base, owner.AccessToken, "Other Bot", "other-bot")
	group := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name: "Snake", lead: yoda.Id, turnLimit: 20, botTalk: true,
		members: []any{groupBot(yoda.Id, ""), groupBot(babu.Id, ""), groupUser(viewer.User.Id, "")},
	})
	tools := groups.TeamTools{Pool: h.pool, Now: h.clock.Now}
	working := make(chan error, 1)
	finish := make(chan struct{})
	updates := make(chan uuid.UUID, 4)
	expectSignal := func() {
		t.Helper()
		select {
		case conversation := <-updates:
			if conversation != group.Id {
				t.Fatal("update signal left the group")
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	const progress = "The game loop is in place. I am running tests."
	const blocker = "The test suite needs a fixture."
	const discarded = "rejected-tool-text"
	var asker engine.Session
	worker := &engine.Worker{
		Pool: h.pool, Now: h.clock.Now, Enqueue: queue.Enqueue,
		Signal: func(_ context.Context, workspace, conversation uuid.UUID, ids []uuid.UUID) {
			if workspace == ws && len(ids) == 0 {
				updates <- conversation
			}
		},
		Execute: func(ctx context.Context, session engine.Session, emit func(engine.Event) error) error {
			if session.TeamTool == nil {
				return errors.New("group turn has no team tools")
			}
			if session.BotID == babu.Id {
				err := exerciseWorkingBot(ctx, session, emit, progress, blocker, discarded)
				working <- err
				if err != nil {
					return err
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-finish:
					return emit(engine.Event{Kind: engine.EventKindMessage, Text: "Ticket #4 is complete."})
				}
			}
			if session.ContinuationPrompt == "" {
				return emit(engine.Event{Kind: engine.EventKindMessage, Text: "Work on ticket #4.\nRequest required input: [\"Babu Frik\"]"})
			}
			asker = session
			for _, want := range []string{"Group status at collection: working", "avoid assigning the same work again", blocker} {
				if !strings.Contains(session.ContinuationPrompt, want) {
					return fmt.Errorf("missing continuation detail %q", want)
				}
			}
			if !strings.Contains(session.Preface, "Babu Frik (progress update): "+progress) {
				return errors.New("missing progress in the group brief")
			}
			output, err := session.TeamTool(ctx, groups.TeamStatusTool, json.RawMessage(`{"bot":"babu frik"}`))
			if err != nil || !strings.Contains(output, `"state":"working"`) || !strings.Contains(output, blocker) {
				return fmt.Errorf("model tool did not see ongoing work: %q, %v", output, err)
			}
			status, err := statusThroughTeamMCP(tools, ws, yoda.Id, session.TurnID, "Babu Frik")
			if err != nil || status.State != apigen.TeamBotStatusStateWorking || status.WorkingSince == nil || status.ProgressAt == nil {
				return fmt.Errorf("CLI tool did not see ongoing work: %+v, %v", status, err)
			}
			canceled, stop := context.WithCancel(ctx)
			stop()
			if _, err = session.TeamTool(canceled, groups.TeamStatusTool, json.RawMessage(`{"bot":"Babu Frik"}`)); !errors.Is(err, context.Canceled) {
				return fmt.Errorf("canceled lookup: %v", err)
			}
			if _, err = statusThroughTeamMCP(tools, ws, babu.Id, session.TurnID, "Yoda"); !errors.Is(err, groups.ErrTeamToolUnavailable) {
				return fmt.Errorf("another bot used the turn ID: %v", err)
			}
			for _, name := range []string{"Other Bot", "Yoda"} {
				input, _ := json.Marshal(map[string]string{"bot": name})
				if _, err = session.TeamTool(ctx, groups.TeamStatusTool, input); !errors.Is(err, groups.ErrTeamToolUnavailable) {
					return fmt.Errorf("ineligible target %s: %v", name, err)
				}
			}
			return emit(engine.Event{Kind: engine.EventKindMessage, Text: "Babu Frik is still working. I will wait for the result."})
		},
	}
	postGroup(t, h, base, owner.AccessToken, group.Id, "@Yoda assign ticket #4")
	if err := worker.Process(ctx, queue.jobs[0]); err != nil {
		t.Fatal(err)
	}
	babuJob := queue.jobs[1]
	done := make(chan error, 1)
	go func() { done <- worker.Process(ctx, babuJob) }()
	select {
	case err := <-working:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	history := groupHistory(t, h, base, viewer.AccessToken, group.Id)
	assertTeamHistory(t, history, 2, 0, progress, blocker, discarded)
	path := base + "/groups/" + group.Id.String() + "/messages"
	if r := h.request(http.MethodGet, path, nil, "", nil, true); r.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated progress history returned %d", r.status)
	}
	if r := h.request(http.MethodPost, path, map[string]any{"id": uuid.New(), "content": "Viewer update"}, viewer.AccessToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("viewer write returned %d", r.status)
	}
	if len(history.Turns) != 2 || len(queue.jobs) != 2 {
		t.Fatalf("progress scheduled work: %d turns, %d jobs", len(history.Turns), len(queue.jobs))
	}
	for range 2 {
		expectSignal()
	}

	// Advance the request deadline while keeping the live execution's heartbeat fresh.
	h.clock.Advance(5 * time.Minute)
	if err := h.queries.TouchBotTurnHeartbeat(ctx, db.TouchBotTurnHeartbeatParams{
		WorkspaceID: pgtype.UUID{Bytes: ws, Valid: true}, ID: pgtype.UUID{Bytes: babuJob.TurnID, Valid: true}, AsOf: pgtype.Timestamptz{Time: h.clock.Now(), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := worker.CollectGroupInputs(ctx); err != nil || len(queue.jobs) != 3 {
		t.Fatalf("collect input: %v, %d jobs", err, len(queue.jobs))
	}
	if err := worker.Process(ctx, queue.jobs[2]); err != nil {
		t.Fatal(err)
	}
	assertTeamHistory(t, groupHistory(t, h, base, owner.AccessToken, group.Id), 2, 1, progress, blocker, discarded)
	expectSignal()
	if len(queue.jobs) != 3 {
		t.Fatalf("status check scheduled work: %d jobs", len(queue.jobs))
	}
	if _, err := asker.TeamTool(ctx, groups.TeamProgressTool, json.RawMessage(`{"text":"Too late"}`)); !errors.Is(err, groups.ErrTeamToolUnavailable) {
		t.Fatalf("finished turn could still post: %v", err)
	}
	if _, err := statusThroughTeamMCP(tools, uuid.New(), babu.Id, babuJob.TurnID, "Yoda"); !errors.Is(err, groups.ErrTeamToolUnavailable) {
		t.Fatalf("proxy could read a turn in another workspace: %v", err)
	}
	close(finish)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	history = groupHistory(t, h, base, owner.AccessToken, group.Id)
	if history.Messages[len(history.Messages)-1].Content != "Ticket #4 is complete." || len(history.Turns) != 3 {
		t.Fatalf("original work did not finish normally: %+v", history.Turns)
	}
}

func exerciseWorkingBot(ctx context.Context, session engine.Session, emit func(engine.Event) error, progress, blocker, discarded string) error {
	for _, item := range []struct {
		text            string
		blocker, posted bool
	}{{progress, false, true}, {"Routine updates must wait", false, false}, {blocker, true, true}, {blocker, true, false}} {
		args, _ := json.Marshal(map[string]any{"text": item.text, "blocker": item.blocker})
		out, err := session.TeamTool(ctx, groups.TeamProgressTool, args)
		if err != nil || !strings.Contains(out, fmt.Sprintf(`"posted":%t`, item.posted)) {
			return fmt.Errorf("unexpected progress result: %s, %v", out, err)
		}
	}
	if err := emit(engine.Event{Kind: engine.EventKindToolCall, ToolID: "rejected", ToolName: groups.TeamProgressTool, ToolInput: json.RawMessage(`{"text":"` + discarded + `"}`)}); err != nil {
		return err
	}
	return emit(engine.Event{Kind: engine.EventKindToolResult, ToolID: "rejected", Text: discarded, IsError: true})
}

func statusThroughTeamMCP(tools groups.TeamTools, ws, bot, turn uuid.UUID, peer string) (apigen.TeamBotStatus, error) {
	params := map[string]any{"name": groups.TeamStatusTool, "arguments": map[string]string{"turn_id": turn.String(), "bot": peer}}
	request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
	recorder := httptest.NewRecorder()
	mcpproxy.TeamHandler(tools, ws, bot).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/team-mcp", strings.NewReader(string(request))))
	var response struct {
		Result struct {
			IsError bool
			Content []struct{ Text string }
		}
	}
	var status apigen.TeamBotStatus
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || len(response.Result.Content) != 1 {
		return status, fmt.Errorf("invalid team MCP response: %s", recorder.Body.String())
	}
	message := response.Result.Content[0].Text
	if response.Result.IsError {
		if message == groups.ErrTeamToolUnavailable.Error() {
			return status, groups.ErrTeamToolUnavailable
		}
		return status, errors.New(message)
	}
	err := json.Unmarshal([]byte(message), &status)
	return status, err
}

func assertTeamHistory(t *testing.T, history apigen.ChatHistory, progressCount, statusCount int, progress, blocker, discarded string) {
	t.Helper()
	var progressSeen, statusSeen int
	for _, message := range history.Messages {
		if strings.Contains(message.Content, discarded) {
			t.Fatal("discarded tool text reached group history")
		}
		switch message.Kind {
		case apigen.ChatMessageKindProgress:
			progressSeen++
			if message.Content != progress && message.Content != blocker {
				t.Fatalf("unexpected progress content %q", message.Content)
			}
			if message.Content == blocker && (message.ProgressBlocker == nil || !*message.ProgressBlocker) {
				t.Fatal("blocker badge is missing")
			}
		case apigen.ChatMessageKindTeamStatus:
			statusSeen++
			if status := message.TeamStatus; status == nil || status.State != apigen.TeamBotStatusStateWorking || status.Progress == nil || *status.Progress != blocker {
				t.Fatalf("bad persisted status: %+v", status)
			}
		}
	}
	if progressSeen != progressCount || statusSeen != statusCount {
		t.Fatalf("history has %d progress and %d status entries, want %d and %d", progressSeen, statusSeen, progressCount, statusCount)
	}
}
