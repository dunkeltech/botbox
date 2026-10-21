package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/budgets"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/notify"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestPhase6Notifications(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue, chatLimit: 80})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	sam, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "sam@example.com", "member", "Sam", "CorrectHorse123!")
	quiet, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "quiet@example.com", "member", "Quiet", "CorrectHorse123!")
	viewer, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "viewer@example.com", "viewer", "Viewer", "CorrectHorse123!")
	admin, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "admin@example.com", "admin", "Admin", "CorrectHorse123!")
	atlas := createAPIBot(t, h, base, owner.AccessToken, "Atlas", "atlas-seed")
	people := []any{
		groupBot(atlas.Id, "lead"),
		groupUser(owner.User.Id, "owner"),
		groupUser(sam.User.Id, "writer"),
		groupUser(quiet.User.Id, "quiet"),
		groupUser(viewer.User.Id, "reader"),
	}
	launch := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name: "Launch", lead: atlas.Id, botTalk: false, turnLimit: 20, members: people,
	})
	setNotify(t, h, ws, launch.Id, sam.User.Id, "mentions")
	setNotify(t, h, ws, launch.Id, quiet.User.Id, "mute")

	notesURL := base + "/notifications"
	if r := h.request(http.MethodGet, notesURL, nil, "", nil, false); r.status != http.StatusUnauthorized || r.errorCode(t) != "auth.unauthenticated" {
		t.Fatalf("list unauth=%d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, notesURL+"/read", nil, "", nil, true); r.status != http.StatusUnauthorized {
		t.Fatalf("read unauth=%d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, notesURL+"/read", nil, sam.AccessToken, nil, false); r.status != http.StatusForbidden || r.errorCode(t) != "auth.csrf_required" {
		t.Fatalf("csrf=%d %s", r.status, r.body)
	}
	for _, limit := range []string{"0", "101", "nope"} {
		if r := h.request(http.MethodGet, notesURL+"?limit="+limit, nil, sam.AccessToken, nil, false); r.status != http.StatusBadRequest || r.errorCode(t) != "validation_error" {
			t.Fatalf("limit %s=%d %s", limit, r.status, r.body)
		}
	}
	if r := h.request(http.MethodGet, notesURL+"?cursor=%%%", nil, sam.AccessToken, nil, false); r.status != http.StatusBadRequest {
		t.Fatalf("cursor=%d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, notesURL+"/not-a-uuid/read", nil, sam.AccessToken, nil, true); r.status != http.StatusBadRequest {
		t.Fatalf("bad id=%d %s", r.status, r.body)
	}

	firstID := uuid.New()
	posted := h.request(http.MethodPost, base+"/groups/"+launch.Id.String()+"/messages", map[string]any{"id": firstID, "content": "@Sam @Sam notes"}, owner.AccessToken, nil, true)
	if posted.status != http.StatusAccepted {
		t.Fatalf("mention post=%d %s", posted.status, posted.body)
	}
	replay := h.request(http.MethodPost, base+"/groups/"+launch.Id.String()+"/messages", map[string]any{"id": firstID, "content": "@Sam @Sam notes"}, owner.AccessToken, nil, true)
	if replay.status != http.StatusAccepted {
		t.Fatalf("replay=%d %s", replay.status, replay.body)
	}
	samMentions := notesOf(listNotes(t, h, notesURL, sam.AccessToken), apigen.NotificationKindMention, &launch.Id)
	if len(samMentions) != 1 || samMentions[0].ReadAt != nil || samMentions[0].ConversationId == nil || *samMentions[0].ConversationId != launch.Id {
		t.Fatalf("sam mentions=%s", mustNotes(listNotes(t, h, notesURL, sam.AccessToken)))
	}
	if strings.Contains(string(mustNotes(listNotes(t, h, notesURL, sam.AccessToken))), "notes") {
		t.Fatal("notification list included message text")
	}
	if n := len(notesOf(listNotes(t, h, notesURL, owner.AccessToken), apigen.NotificationKindMention, &launch.Id)); n != 0 {
		t.Fatalf("owner mentions=%d", n)
	}
	if n := len(listNotes(t, h, notesURL, quiet.AccessToken).Notifications); n != 0 {
		t.Fatalf("muted=%d", n)
	}
	if len(queue.signals) != 1 {
		t.Fatalf("signals=%d", len(queue.signals))
	}
	signalConv, signalIDs := assertIDSignal(t, queue.signals[0], "notes", "@Sam")
	if signalConv != launch.Id || len(signalIDs) != 1 || signalIDs[0] != samMentions[0].Id {
		t.Fatalf("signal conv=%s ids=%v", signalConv, signalIDs)
	}

	if getGroup(t, h, base, owner.AccessToken, launch.Id).Unread < 1 || getGroup(t, h, base, sam.AccessToken, launch.Id).Unread < 1 {
		t.Fatal("both humans should see an unread badge")
	}
	if r := h.request(http.MethodPost, base+"/groups/"+launch.Id.String()+"/read", nil, sam.AccessToken, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("mark group=%d %s", r.status, r.body)
	}
	if getGroup(t, h, base, sam.AccessToken, launch.Id).Unread != 0 {
		t.Fatal("sam badge stayed")
	}
	if getGroup(t, h, base, owner.AccessToken, launch.Id).Unread < 1 {
		t.Fatal("owner badge cleared with sam's cursor")
	}
	if again := notesOf(listNotes(t, h, notesURL, sam.AccessToken), apigen.NotificationKindMention, &launch.Id); len(again) != 1 || again[0].ReadAt != nil {
		t.Fatal("group read deleted or marked the notification")
	}

	postGroup(t, h, base, owner.AccessToken, launch.Id, "@Sam two")
	postGroup(t, h, base, owner.AccessToken, launch.Id, "@Sam three")
	seen := map[uuid.UUID]bool{}
	cursor := ""
	for {
		target := notesURL + "?limit=1"
		if cursor != "" {
			target += "&cursor=" + cursor
		}
		r := h.request(http.MethodGet, target, nil, sam.AccessToken, nil, false)
		if r.status != http.StatusOK {
			t.Fatalf("page=%d %s", r.status, r.body)
		}
		var page apigen.NotificationList
		r.decode(t, &page)
		if len(page.Notifications) != 1 {
			t.Fatalf("page size=%d", len(page.Notifications))
		}
		if seen[page.Notifications[0].Id] {
			t.Fatalf("duplicate page id %s", page.Notifications[0].Id)
		}
		seen[page.Notifications[0].Id] = true
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
		if len(seen) > 5 {
			t.Fatal("cursor did not end")
		}
	}
	if len(seen) != 3 {
		t.Fatalf("pages=%d", len(seen))
	}

	if r := h.request(http.MethodPost, notesURL+"/"+samMentions[0].Id.String()+"/read", nil, owner.AccessToken, nil, true); r.status != http.StatusNotFound || r.errorCode(t) != "notification.not_found" {
		t.Fatalf("other user=%d %s", r.status, r.body)
	}
	if r := h.request(http.MethodPost, notesURL+"/"+uuid.New().String()+"/read", nil, sam.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("missing=%d %s", r.status, r.body)
	}
	marked := h.request(http.MethodPost, notesURL+"/"+samMentions[0].Id.String()+"/read", nil, sam.AccessToken, nil, true)
	if marked.status != http.StatusNoContent {
		t.Fatalf("mark one=%d %s", marked.status, marked.body)
	}
	if again := h.request(http.MethodPost, notesURL+"/"+samMentions[0].Id.String()+"/read", nil, sam.AccessToken, nil, true); again.status != http.StatusNoContent {
		t.Fatalf("mark twice=%d %s", again.status, again.body)
	}
	stamped := findNote(t, listNotes(t, h, notesURL, sam.AccessToken), samMentions[0].Id)
	if stamped.ReadAt == nil {
		t.Fatal("readAt stayed empty")
	}
	if r := h.request(http.MethodPost, notesURL+"/read", nil, viewer.AccessToken, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("viewer mark all=%d %s", r.status, r.body)
	}

	capGroup := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name: "Cap", lead: atlas.Id, botTalk: false, turnLimit: 20, budget: 100,
		members: []any{groupBot(atlas.Id, "lead"), groupUser(owner.User.Id, "owner"), groupUser(sam.User.Id, "writer")},
	})
	setNotify(t, h, ws, capGroup.Id, sam.User.Id, "mentions")
	cards := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name: "Cards", lead: atlas.Id, botTalk: false, turnLimit: 20,
		ask:     []any{map[string]any{"kind": "custom", "label": "ship the release"}},
		members: people,
	})
	setNotify(t, h, ws, cards.Id, sam.User.Id, "mentions")
	setNotify(t, h, ws, cards.Id, quiet.User.Id, "mute")

	var capTurns int
	var launchReplies int
	worker := &engine.Worker{
		Pool: h.pool, Now: h.clock.Now, Enqueue: queue.Enqueue,
		Signal: func(ctx context.Context, wsID, conv uuid.UUID, ids []uuid.UUID) {
			data, err := notify.MarshalSignal(conv, ids)
			if err != nil {
				t.Errorf("signal: %v", err)
				return
			}
			if err = queue.PublishConversation(ctx, wsID, conv, data); err != nil {
				t.Errorf("publish: %v", err)
			}
		},
		Execute: func(_ context.Context, s engine.Session, emit func(engine.Event) error) error {
			switch s.ConversationID {
			case cards.Id:
				return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: "ship the release"})
			case capGroup.Id:
				capTurns++
				amount := int64(0)
				if capTurns == 1 {
					amount = 80
				}
				return emit(engine.Event{Kind: engine.EventKindUsage, CostMicros: &amount, InputTokens: 1, OutputTokens: 1})
			default:
				// Only the first stored reply mentions a human. Later replies
				// must not add mention rows while the queue is being drained.
				launchReplies++
				text := "done"
				if launchReplies == 1 {
					text = "@Owner ping"
				}
				return emit(engine.Event{Kind: engine.EventKindTextDelta, Text: text})
			}
		},
	}
	before := len(queue.signals)
	drainTurns(t, worker, queue, 4)
	ownerMentions := notesOf(listNotes(t, h, notesURL, owner.AccessToken), apigen.NotificationKindMention, &launch.Id)
	if len(ownerMentions) != 1 {
		t.Fatalf("bot mention=%d", len(ownerMentions))
	}
	if len(notesOf(listNotes(t, h, notesURL, sam.AccessToken), apigen.NotificationKindMention, &launch.Id)) != 3 {
		t.Fatal("bot reply notified sam")
	}
	foundPing := false
	for _, raw := range queue.signals[before:] {
		conv, ids := assertIDSignal(t, raw, "ping", "@Owner")
		if conv == launch.Id && len(ids) == 1 && ids[0] == ownerMentions[0].Id {
			foundPing = true
		}
	}
	if !foundPing {
		t.Fatal("bot mention did not publish an id signal")
	}

	postGroup(t, h, base, owner.AccessToken, launch.Id, "@Viewer hi")
	postGroup(t, h, base, owner.AccessToken, launch.Id, "@Quiet hello")
	viewerNotes := listNotes(t, h, notesURL, viewer.AccessToken)
	if len(viewerNotes.Notifications) != 1 || viewerNotes.Notifications[0].Kind != apigen.NotificationKindMention {
		t.Fatalf("viewer=%s", mustNotes(viewerNotes))
	}
	if r := h.request(http.MethodPost, notesURL+"/"+viewerNotes.Notifications[0].Id.String()+"/read", nil, viewer.AccessToken, nil, true); r.status != http.StatusNoContent {
		t.Fatalf("viewer read=%d %s", r.status, r.body)
	}
	if findNote(t, listNotes(t, h, notesURL, viewer.AccessToken), viewerNotes.Notifications[0].Id).ReadAt == nil {
		t.Fatal("viewer readAt empty")
	}
	if r := h.request(http.MethodPost, notesURL+"/"+samMentions[0].Id.String()+"/read", nil, viewer.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("viewer other=%d %s", r.status, r.body)
	}
	if n := len(listNotes(t, h, notesURL, quiet.AccessToken).Notifications); n != 0 {
		t.Fatalf("mute mention=%d", n)
	}
	drainTurns(t, worker, queue, 6)

	other := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other", "slug": "other-ws"}, owner.AccessToken, nil, true)
	if other.status != http.StatusCreated {
		t.Fatalf("workspace=%d %s", other.status, other.body)
	}
	var otherWS struct {
		ID uuid.UUID `json:"id"`
	}
	other.decode(t, &otherWS)
	if r := h.request(http.MethodPost, "/api/workspaces/"+otherWS.ID.String()+"/notifications/"+ownerMentions[0].Id.String()+"/read", nil, owner.AccessToken, nil, true); r.status != http.StatusNotFound {
		t.Fatalf("cross workspace=%d %s", r.status, r.body)
	}
	if r := h.request(http.MethodGet, "/api/workspaces/"+otherWS.ID.String()+"/notifications", nil, sam.AccessToken, nil, false); r.status != http.StatusNotFound || r.errorCode(t) != "workspace.not_found" {
		t.Fatalf("sam other workspace=%d %s", r.status, r.body)
	}
	if findNote(t, listNotes(t, h, notesURL, owner.AccessToken), ownerMentions[0].Id).ReadAt != nil {
		t.Fatal("cross-workspace mark changed the row")
	}

	postGroup(t, h, base, owner.AccessToken, cards.Id, "please")
	before = len(queue.signals)
	drainTurns(t, worker, queue, 2)
	if n := len(notesOf(listNotes(t, h, notesURL, owner.AccessToken), apigen.NotificationKindApproval, &cards.Id)); n != 1 {
		t.Fatalf("owner approval notices=%d", n)
	}
	if n := len(notesOf(listNotes(t, h, notesURL, admin.AccessToken), apigen.NotificationKindApproval, &cards.Id)); n != 1 {
		t.Fatalf("admin approval notices=%d", n)
	}
	for _, token := range []string{sam.AccessToken, viewer.AccessToken, quiet.AccessToken} {
		if n := len(notesOf(listNotes(t, h, notesURL, token), apigen.NotificationKindApproval, &cards.Id)); n != 0 {
			t.Fatalf("unexpected approval notices=%d", n)
		}
	}
	foundCard := false
	for _, raw := range queue.signals[before:] {
		conv, _ := assertIDSignal(t, raw, "ship the release")
		if conv == cards.Id {
			foundCard = true
		}
	}
	if !foundCard {
		t.Fatal("approval did not publish an id signal")
	}

	postGroup(t, h, base, owner.AccessToken, capGroup.Id, "first")
	drainTurns(t, worker, queue, 2)
	postGroup(t, h, base, owner.AccessToken, capGroup.Id, "second")
	drainTurns(t, worker, queue, 2)
	postGroup(t, h, base, owner.AccessToken, capGroup.Id, "third")
	drainTurns(t, worker, queue, 2)
	if capTurns != 3 {
		t.Fatalf("cap turns=%d", capTurns)
	}
	if n := len(notesOf(listNotes(t, h, notesURL, owner.AccessToken), apigen.NotificationKindBudget, &capGroup.Id)); n != 1 {
		t.Fatalf("budget notices=%d", n)
	}
	if n := len(notesOf(listNotes(t, h, notesURL, sam.AccessToken), apigen.NotificationKindBudget, &capGroup.Id)); n != 0 {
		t.Fatalf("mentions-only budget notices=%d", n)
	}
	ownerBudget := notesOf(listNotes(t, h, notesURL, owner.AccessToken), apigen.NotificationKindBudget, &capGroup.Id)
	if strings.Contains(string(mustNotes(apigen.NotificationList{Notifications: ownerBudget})), "Budget 80%") {
		t.Fatal("budget notification included the warning text")
	}

	if r := h.request(http.MethodPut, base+"/budgets", map[string]any{"period": "month", "limitMicros": 1}, owner.AccessToken, nil, true); r.status != http.StatusOK {
		t.Fatalf("budget=%d %s", r.status, r.body)
	}
	if err := budgets.Check(context.Background(), h.queries, ws, atlas.Id, time.Now()); !errors.Is(err, llmproxy.ErrBudgetExceeded) {
		t.Fatalf("check=%v", err)
	}
	if err := budgets.Check(context.Background(), h.queries, ws, atlas.Id, time.Now()); !errors.Is(err, llmproxy.ErrBudgetExceeded) {
		t.Fatalf("second check=%v", err)
	}
	if n := len(notesWithoutConversation(listNotes(t, h, notesURL, owner.AccessToken), apigen.NotificationKindApproval)); n != 1 {
		t.Fatalf("workspace approval notices=%d", n)
	}
	if n := len(notesWithoutConversation(listNotes(t, h, notesURL, admin.AccessToken), apigen.NotificationKindApproval)); n != 1 {
		t.Fatalf("admin workspace notices=%d", n)
	}
	if n := len(notesWithoutConversation(listNotes(t, h, notesURL, sam.AccessToken), apigen.NotificationKindApproval)); n != 0 {
		t.Fatalf("member workspace notices=%d", n)
	}

	queue.err = errors.New("nats down")
	failed := createGroup(t, h, base, owner.AccessToken, groupSettings{
		name: "Rollback", lead: atlas.Id, botTalk: false, turnLimit: 5, members: []any{groupBot(atlas.Id, "lead"), groupUser(sam.User.Id, "writer")},
	})
	beforeSam := len(listNotes(t, h, notesURL, sam.AccessToken).Notifications)
	rolled := h.request(http.MethodPost, base+"/groups/"+failed.Id.String()+"/messages", map[string]any{"id": uuid.New(), "content": "@Sam fail"}, owner.AccessToken, nil, true)
	if rolled.status != http.StatusServiceUnavailable {
		t.Fatalf("publish failure=%d %s", rolled.status, rolled.body)
	}
	if n := len(listNotes(t, h, notesURL, sam.AccessToken).Notifications); n != beforeSam {
		t.Fatalf("rolled back mention stored, before=%d after=%d", beforeSam, n)
	}
	var stored int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM messages WHERE workspace_id=$1 AND conversation_id=$2`, ws, failed.Id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Fatalf("rolled back message stored=%d", stored)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.queries.ListNotifications(ctx, db.ListNotificationsParams{
		WorkspaceID: pgtype.UUID{Bytes: ws, Valid: true},
		UserID:      pgtype.UUID{Bytes: owner.User.Id, Valid: true},
		RowLimit:    1,
	})
	if err == nil {
		t.Fatal("cancelled list succeeded")
	}
}

func setNotify(t *testing.T, h *testHarness, ws, group, user uuid.UUID, mode string) {
	t.Helper()
	tag, err := h.pool.Exec(context.Background(), `UPDATE conversation_members SET notify=$1 WHERE workspace_id=$2 AND conversation_id=$3 AND member_id=$4 AND member_type='user'`, mode, ws, group, user)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("notify %s rows=%d err=%v", mode, tag.RowsAffected(), err)
	}
}

func listNotes(t *testing.T, h *testHarness, url, token string) apigen.NotificationList {
	t.Helper()
	r := h.request(http.MethodGet, url, nil, token, nil, false)
	if r.status != http.StatusOK {
		t.Fatalf("list=%d %s", r.status, r.body)
	}
	var out apigen.NotificationList
	r.decode(t, &out)
	return out
}

func notesOf(list apigen.NotificationList, kind apigen.NotificationKind, conversation *uuid.UUID) []apigen.Notification {
	var out []apigen.Notification
	for _, row := range list.Notifications {
		if row.Kind != kind {
			continue
		}
		if conversation != nil && (row.ConversationId == nil || *row.ConversationId != *conversation) {
			continue
		}
		out = append(out, row)
	}
	return out
}

func notesWithoutConversation(list apigen.NotificationList, kind apigen.NotificationKind) []apigen.Notification {
	var out []apigen.Notification
	for _, row := range list.Notifications {
		if row.Kind == kind && row.ConversationId == nil {
			out = append(out, row)
		}
	}
	return out
}

func findNote(t *testing.T, list apigen.NotificationList, id uuid.UUID) apigen.Notification {
	t.Helper()
	for _, row := range list.Notifications {
		if row.Id == id {
			return row
		}
	}
	t.Fatalf("notification %s missing", id)
	return apigen.Notification{}
}

func mustNotes(list apigen.NotificationList) string {
	raw, _ := json.Marshal(list)
	return string(raw)
}

func assertIDSignal(t *testing.T, raw []byte, banned ...string) (uuid.UUID, []uuid.UUID) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("signal json: %v %s", err, raw)
	}
	if len(fields) != 2 || fields["notificationIds"] == nil || fields["conversationId"] == nil {
		t.Fatalf("signal fields=%s", raw)
	}
	text := string(raw)
	for _, word := range banned {
		if word != "" && strings.Contains(text, word) {
			t.Fatalf("signal contained %q in %s", word, text)
		}
	}
	var body struct {
		NotificationIDs []uuid.UUID `json:"notificationIds"`
		ConversationID  uuid.UUID   `json:"conversationId"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body.ConversationID, body.NotificationIDs
}
