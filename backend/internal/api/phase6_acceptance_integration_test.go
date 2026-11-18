// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/engine"
	"github.com/dunkeltech/botbox/backend/internal/engine/enginetest"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/google/uuid"
)

func TestPhase6GroupHandoffAcceptance(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue, chatLimit: 200})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()

	humanA, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "alice@example.com", "member", "Alice", "CorrectHorse123!")
	humanB, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "bob@example.com", "member", "Bob", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "vera@example.com", "viewer", "Vera", "CorrectHorse123!")
	outsider, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "oscar@example.com", "member", "Oscar", "CorrectHorse123!")

	atlas := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas-seed")
	nova := createPiBot(t, h, base, owner.AccessToken, "Nova", "nova-seed")
	members := []any{
		groupUser(humanA.User.Id, "Maintainer"),
		groupUser(humanB.User.Id, "Release manager"),
		groupUser(viewer.User.Id, "Observer"),
		groupBot(atlas.Id, "Coder · odoo-maintenance"),
		groupBot(nova.Id, "Reviewer · migrations"),
	}

	// Turn limit 4 leaves room for the 3-turn handoff plus the approval resume,
	// then stops on the fourth bot turn without queuing another handoff.
	odoo := createGroup(t, h, base, humanA.AccessToken, groupSettings{
		name:      "odoo-maintenance",
		goal:      "Keep odoo-maintenance migrations clean",
		lead:      nova.Id,
		botTalk:   true,
		turnLimit: 4,
		ask:       []any{map[string]any{"kind": "custom", "label": "ship the release"}},
		members:   members,
	})

	fake := enginetest.NewProvider(
		// 1. Atlas answers Human A with a mention of Nova (plus the MR card below).
		fakeSSEResponse(t, "Nova, please review MR !42.\nRequest input: [\"Nova\"]", "0.000001", func(r *http.Request) error {
			return assertProviderRequest(r, "Keep odoo-maintenance migrations clean", "Request input:", "CorrectHorse123!")
		}),
		// 2. Nova finishes the direct human mention before taking Atlas's handoff.
		fakeSSEResponse(t, "Inspecting the migration diff", "0.000001", nil),
		// 3. Nova runs on Atlas's mention, mentions Alice, and triggers ask-before.
		fakeSSEResponse(t, "@Alice migration looks ready to ship the release", "0.000001", func(r *http.Request) error {
			return assertProviderRequest(r, "Nova, please review MR !42.")
		}),
		// 4. Resumed Nova finishes Turn 4 and hits turn_limit.
		fakeSSEResponse(t, "@Atlas release checks complete", "0.000001", nil),
		// 5-6. bot_talk = false: both bots answer Human A and ignore each other's mentions.
		fakeSSEResponse(t, "@Nova first check done", "0.000001", nil),
		fakeSSEResponse(t, "@Atlas second check done", "0.000001", nil),
		// 7-11. Repeated input rounds trigger the loop guard before the turn limit.
		fakeSSEResponse(t, "Ping?\nRequest input: [\"Nova\"]", "0.000001", nil),
		fakeSSEResponse(t, "Pong.", "0.000001", nil),
		fakeSSEResponse(t, "Ping?\nRequest input: [\"Nova\"]", "0.000001", nil),
		fakeSSEResponse(t, "Pong.", "0.000001", nil),
		fakeSSEResponse(t, "Ping?\nRequest input: [\"Nova\"]", "0.000001", nil),
		// 12. 1-microdollar cap: first turn spends 1 microdollar, second never calls provider.
		fakeSSEResponse(t, "Spent the group cap", "0.000001", nil),
	)
	providerServer := httptest.NewServer(fake)
	defer providerServer.Close()

	var callSeq atomic.Int32
	worker := &engine.Worker{
		Pool:    h.pool,
		Now:     h.clock.Now,
		Enqueue: queue.Enqueue,
		Execute: func(ctx context.Context, session engine.Session, emit func(engine.Event) error) error {
			step := callSeq.Add(1)
			if err := runFakeProvider(ctx, h, providerServer, session, emit); err != nil {
				return err
			}
			if step == 1 {
				return emit(engine.Event{
					Kind:     engine.EventKindCard,
					Text:     "MR !42 · Fix odoo-maintenance migration",
					Metadata: json.RawMessage(`{"kind":"merge_request","repo":"odoo-maintenance","mr":42}`),
				})
			}
			return nil
		},
	}

	postGroup(t, h, base, humanA.AccessToken, odoo.Id, "@Atlas @Nova please review the odoo-maintenance migration")
	drainTurns(t, worker, queue, 6)

	paused := getGroup(t, h, base, humanA.AccessToken, odoo.Id)
	if !paused.Paused || paused.PauseReason != "approval" || paused.TurnsSinceHuman != 3 || len(queue.jobs) != 0 {
		t.Fatalf("after handoff=%s turns=%d jobs=%d", mustJSON(paused), paused.TurnsSinceHuman, len(queue.jobs))
	}

	history := groupHistory(t, h, base, viewer.AccessToken, odoo.Id)
	assertCardPersisted(t, h, ws, odoo.Id, atlas.Id, history, "MR !42 · Fix odoo-maintenance migration", "merge_request", "odoo-maintenance", 42)

	card := findApproval(t, listApprovals(t, h, base, humanB.AccessToken, "pending"), apigen.ApprovalKindCustom, "")
	assertGroupRBAC(t, h, base, odoo.Id, card.Id, viewer.AccessToken, outsider.AccessToken)

	// Human B (who did not open the conversation) approves; Nova resumes with
	// turns_since_human still at 3 (the approval must not reset the counter).
	if r := h.request(http.MethodPost, base+"/approvals/"+card.Id.String()+"/decision", map[string]any{"decision": "approved"}, humanB.AccessToken, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("humanB approve=%d %s", r.status, r.body)
	}
	resumed := getGroup(t, h, base, humanA.AccessToken, odoo.Id)
	if resumed.Paused || resumed.TurnsSinceHuman != 3 || len(queue.jobs) != 1 || queue.jobs[0].BotID != nova.Id {
		t.Fatalf("resumed=%s turns=%d jobs=%#v", mustJSON(resumed), resumed.TurnsSinceHuman, queue.jobs)
	}

	drainTurns(t, worker, queue, 4)
	limited := getGroup(t, h, base, humanA.AccessToken, odoo.Id)
	if !limited.Paused || limited.PauseReason != "turn_limit" || limited.TurnsSinceHuman != 4 || len(queue.jobs) != 0 {
		t.Fatalf("turn limit=%s turns=%d jobs=%d", mustJSON(limited), limited.TurnsSinceHuman, len(queue.jobs))
	}
	limitHistory := groupHistory(t, h, base, humanA.AccessToken, odoo.Id)
	if !historyHas(limitHistory, "Turn 4 of 4 · paused, waiting for you") {
		t.Fatalf("missing turn-limit notice: %+v", limitHistory.Messages)
	}

	// Both humans see unread counts until each marks read; Alice has the mention
	// notification and Bob has the approval notification.
	if getGroup(t, h, base, humanA.AccessToken, odoo.Id).Unread < 1 || getGroup(t, h, base, humanB.AccessToken, odoo.Id).Unread < 1 {
		t.Fatal("both humans must see unread counts before marking read")
	}
	if r := h.request(http.MethodPost, base+"/groups/"+odoo.Id.String()+"/read", nil, humanA.AccessToken, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("humanA mark read=%d %s", r.status, r.body)
	}
	if getGroup(t, h, base, humanA.AccessToken, odoo.Id).Unread != 0 || getGroup(t, h, base, humanB.AccessToken, odoo.Id).Unread < 1 {
		t.Fatal("humanA read cleared humanB unread or left humanA badge")
	}
	if r := h.request(http.MethodPost, base+"/groups/"+odoo.Id.String()+"/read", nil, humanB.AccessToken, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("humanB mark read=%d %s", r.status, r.body)
	}
	if getGroup(t, h, base, humanB.AccessToken, odoo.Id).Unread != 0 {
		t.Fatal("humanB unread stayed after mark read")
	}
	if n := len(notesOf(listNotes(t, h, base+"/notifications", humanA.AccessToken), apigen.NotificationKindMention, &odoo.Id)); n != 1 {
		t.Fatalf("humanA mention notifications=%d", n)
	}
	if n := len(notesOf(listNotes(t, h, base+"/notifications", humanB.AccessToken), apigen.NotificationKindApproval, &odoo.Id)); n != 1 {
		t.Fatalf("humanB approval notifications=%d", n)
	}

	// bot_talk = false: bots answer Human A only and stop.
	quiet := createGroup(t, h, base, humanA.AccessToken, groupSettings{
		name: "quiet-handoff", lead: atlas.Id, botTalk: false, turnLimit: 10,
		members: []any{groupUser(humanA.User.Id, "member"), groupBot(atlas.Id, "lead"), groupBot(nova.Id, "reviewer")},
	})
	postGroup(t, h, base, humanA.AccessToken, quiet.Id, "@Atlas @Nova check")
	drainTurns(t, worker, queue, 4)
	quietGot := getGroup(t, h, base, humanA.AccessToken, quiet.Id)
	if quietGot.Paused || quietGot.TurnsSinceHuman != 2 || len(queue.jobs) != 0 {
		t.Fatalf("bot_talk=false got=%s turns=%d jobs=%d", mustJSON(quietGot), quietGot.TurnsSinceHuman, len(queue.jobs))
	}

	// Ping-pong pauses before turn_limit.
	pingPong := createGroup(t, h, base, humanA.AccessToken, groupSettings{
		name: "ping-pong", lead: atlas.Id, botTalk: true, turnLimit: 20,
		members: []any{groupUser(humanA.User.Id, "member"), groupBot(atlas.Id, "lead"), groupBot(nova.Id, "reviewer")},
	})
	postGroup(t, h, base, humanA.AccessToken, pingPong.Id, "@Atlas start")
	drainInputRounds(t, worker, queue)
	loopGot := getGroup(t, h, base, humanA.AccessToken, pingPong.Id)
	if !loopGot.Paused || loopGot.PauseReason != "loop" || loopGot.TurnsSinceHuman >= 20 || len(queue.jobs) != 0 {
		t.Fatalf("loop got=%s turns=%d jobs=%d", mustJSON(loopGot), loopGot.TurnsSinceHuman, len(queue.jobs))
	}

	// Conversation budget of 1 microdollar pauses before a second model call.
	capped := createGroup(t, h, base, humanA.AccessToken, groupSettings{
		name: "one-microdollar", lead: atlas.Id, botTalk: false, turnLimit: 20, budget: 1,
		members: []any{groupUser(humanA.User.Id, "member"), groupBot(atlas.Id, "lead")},
	})
	postGroup(t, h, base, humanA.AccessToken, capped.Id, "first")
	drainTurns(t, worker, queue, 2)
	postGroup(t, h, base, humanA.AccessToken, capped.Id, "second")
	drainTurns(t, worker, queue, 2)
	budgetGot := getGroup(t, h, base, humanA.AccessToken, capped.Id)
	if !budgetGot.Paused || budgetGot.PauseReason != "budget" {
		t.Fatalf("budget got=%s", mustJSON(budgetGot))
	}
	if fake.Remaining() != 0 || len(fake.Errors()) != 0 {
		t.Fatalf("provider remaining=%d errors=%v", fake.Remaining(), fake.Errors())
	}

	// A bot never has two running turns when a second group mentions it concurrently.
	assertSingleRunningTurnAcrossGroups(t, h, queue, base, humanA.AccessToken, ws)
}

func TestPhase6CardEventValidation(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue, chatLimit: 20})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	atlas := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas-seed")
	group := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name: "cards", lead: atlas.Id, botTalk: false, turnLimit: 10, members: []any{groupBot(atlas.Id, "lead")},
	})
	for _, tc := range []struct {
		name string
		ev   engine.Event
	}{
		{"empty card text", engine.Event{Kind: engine.EventKindCard, Text: "   ", Metadata: json.RawMessage(`{"ok":true}`)}},
		{"invalid card metadata", engine.Event{Kind: engine.EventKindCard, Text: "MR !1", Metadata: json.RawMessage(`{broken`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			postGroup(t, h, base, owner.AccessToken, group.Id, "run "+tc.name)
			job := queue.jobs[len(queue.jobs)-1]
			queue.jobs = queue.jobs[:len(queue.jobs)-1]
			worker := &engine.Worker{
				Pool: h.pool, Now: h.clock.Now, Enqueue: queue.Enqueue,
				Execute: func(_ context.Context, _ engine.Session, emit func(engine.Event) error) error {
					return emit(tc.ev)
				},
			}
			if err := worker.Process(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			history := groupHistory(t, h, base, owner.AccessToken, group.Id)
			for _, msg := range history.Messages {
				if msg.Kind == apigen.ChatMessageKindCard {
					t.Fatalf("invalid card persisted: %+v", msg)
				}
			}
			lastTurn := history.Turns[len(history.Turns)-1]
			if lastTurn.Status != apigen.ChatTurnStatusFailed {
				t.Fatalf("turn status=%s want=failed", lastTurn.Status)
			}
		})
	}
}

func fakeSSEResponse(t *testing.T, content, cost string, check func(*http.Request) error) enginetest.Response {
	t.Helper()
	delta, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"content": content}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	chunk := "data: " + string(delta) + "\n\n" +
		`data: {"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":` + cost + "}}\n\n" +
		"data: [DONE]\n\n"
	return enginetest.Response{
		Check:  check,
		Chunks: []enginetest.Chunk{{Data: chunk}},
	}
}

func assertProviderRequest(r *http.Request, mustContain ...string) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	text := string(body)
	for _, want := range mustContain {
		if strings.HasPrefix(want, "CorrectHorse") {
			if strings.Contains(text, want) {
				return errSecretLeaked(want)
			}
			continue
		}
		if !strings.Contains(text, want) {
			return errMissingPrompt(want)
		}
	}
	return nil
}

type errMissingPrompt string

func (e errMissingPrompt) Error() string { return "missing prompt text: " + string(e) }

type errSecretLeaked string

func (e errSecretLeaked) Error() string { return "secret leaked in prompt: " + string(e) }

func runFakeProvider(ctx context.Context, h *testHarness, server *httptest.Server, session engine.Session, emit func(engine.Event) error) error {
	var prompt strings.Builder
	prompt.WriteString(session.Preface)
	for i := len(session.Messages) - 1; i >= 0; i-- {
		prompt.WriteByte('\n')
		prompt.WriteString(session.Messages[i].Content)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(prompt.String()))
	if err != nil {
		return err
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return errors.New("fixture provider rejected request")
	}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		payload, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		if payload == "[DONE]" {
			return recordMeteredFixture(ctx, h, session, 1, true)
		}
		var response struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &response); err != nil {
			return err
		}
		for _, choice := range response.Choices {
			if err := emit(engine.Event{Kind: engine.EventKindTextDelta, Text: choice.Delta.Content}); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return errors.New("fixture provider omitted completion")
}

func assertCardPersisted(t *testing.T, h *testHarness, ws, conv, bot uuid.UUID, history apigen.ChatHistory, wantText, wantKind, wantRepo string, wantMR int) {
	t.Helper()
	var cardID uuid.UUID
	for _, msg := range history.Messages {
		if msg.Kind == apigen.ChatMessageKindCard {
			if msg.SenderType != apigen.ChatMessageSenderTypeBot || msg.SenderBotId == nil || *msg.SenderBotId != bot || msg.Content != wantText {
				t.Fatalf("unexpected card message=%+v", msg)
			}
			cardID = msg.Id
			break
		}
	}
	if cardID == uuid.Nil {
		t.Fatalf("missing card message in history: %+v", history.Messages)
	}
	var rawMeta []byte
	if err := h.pool.QueryRow(context.Background(), `SELECT metadata FROM messages WHERE workspace_id=$1 AND conversation_id=$2 AND id=$3`, ws, conv, cardID).Scan(&rawMeta); err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Kind string `json:"kind"`
		Repo string `json:"repo"`
		MR   int    `json:"mr"`
	}
	if err := json.Unmarshal(rawMeta, &meta); err != nil {
		t.Fatalf("card metadata=%s err=%v", rawMeta, err)
	}
	if meta.Kind != wantKind || meta.Repo != wantRepo || meta.MR != wantMR {
		t.Fatalf("card metadata=%+v want=(%s,%s,%d)", meta, wantKind, wantRepo, wantMR)
	}
}

func assertGroupRBAC(t *testing.T, h *testHarness, base string, groupID, approvalID uuid.UUID, viewerToken, outsiderToken string) {
	t.Helper()
	groupPath := base + "/groups/" + groupID.String()
	decidePath := base + "/approvals/" + approvalID.String() + "/decision"

	// Non-member workspace user gets 404 across group, messages, and approval.
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, groupPath, nil},
		{http.MethodGet, groupPath + "/messages", nil},
		{http.MethodPost, decidePath, map[string]any{"decision": "approved"}},
	} {
		r := h.request(tc.method, tc.path, tc.body, outsiderToken, nil, tc.method == http.MethodPost)
		if r.status != http.StatusNotFound {
			t.Fatalf("outsider %s %s=%d %s", tc.method, tc.path, r.status, r.body)
		}
	}

	// Viewer member can read, and gets 403 on post and decide.
	if r := h.request(http.MethodGet, groupPath+"/messages", nil, viewerToken, nil, false); r.status != http.StatusOK {
		t.Fatalf("viewer history=%d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, groupPath+"/messages", map[string]any{"id": uuid.New(), "content": "nope"}, viewerToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("viewer post=%d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, decidePath, map[string]any{"decision": "approved"}, viewerToken, nil, true); r.status != http.StatusForbidden {
		t.Fatalf("viewer decide=%d %s", r.status, r.body)
	}
}

func assertSingleRunningTurnAcrossGroups(t *testing.T, h *testHarness, queue *fakeTurnQueue, base, token string, ws uuid.UUID) {
	t.Helper()
	queue.jobs = nil
	shared := createPiBot(t, h, base, token, "Shared", "shared-seed")
	if r := h.request(http.MethodPatch, base+"/bots/"+shared.Id.String(), map[string]any{"llmMode": "cli", "llmProvider": "anthropic", "llmModel": "sonnet"}, token, nil, true); r.status != http.StatusOK {
		t.Fatalf("cli patch=%d %s", r.status, r.body)
	}
	firstGroup := createGroup(t, h, base, token, groupSettings{
		name: "concurrent-1", lead: shared.Id, botTalk: false, turnLimit: 5, members: []any{groupBot(shared.Id, "only")},
	})
	secondGroup := createGroup(t, h, base, token, groupSettings{
		name: "concurrent-2", lead: shared.Id, botTalk: false, turnLimit: 5, members: []any{groupBot(shared.Id, "only")},
	})
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	worker := &engine.Worker{
		Pool:    h.pool,
		Now:     h.clock.Now,
		Enqueue: queue.Enqueue,
		Execute: func(_ context.Context, s engine.Session, emit func(engine.Event) error) error {
			if s.ConversationID == firstGroup.Id {
				once.Do(func() { close(started) })
				<-release
			}
			return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "ok"})
		},
	}
	postGroup(t, h, base, token, firstGroup.Id, "@Shared run first")
	job1 := queue.jobs[0]
	queue.jobs = queue.jobs[1:]
	done := make(chan error, 1)
	go func() { done <- worker.Process(context.Background(), job1) }()
	<-started

	postGroup(t, h, base, token, secondGroup.Id, "@Shared run second")
	job2 := queue.jobs[len(queue.jobs)-1]
	queue.jobs = queue.jobs[:len(queue.jobs)-1]
	if err := worker.Process(context.Background(), job2); err == nil {
		t.Fatal("expected second concurrent group turn for the same bot to fail claim")
	}
	var running, queued int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM bot_turns WHERE workspace_id=$1 AND bot_id=$2 AND status='running'`, ws, shared.Id).Scan(&running); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM bot_turns WHERE workspace_id=$1 AND conversation_id=$2 AND status='queued'`, ws, secondGroup.Id).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if running != 1 || queued != 1 {
		t.Fatalf("running=%d queued=%d want=(1,1)", running, queued)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(context.Background(), job2); err != nil {
		t.Fatalf("queued turn after release: %v", err)
	}
}
