package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/compute"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestChatToolImageIsPrivateAndPathLimited(t *testing.T) {
	t.Parallel()
	h := newTestHarness(t, nil, harnessOpts{})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Alex", "Acme Bots", "acme")
	ws := owner.ActiveWorkspace.Id
	member, _ := h.inviteAndAcceptNewUser(owner.AccessToken, ws, "mia@example.com", "member", "Mia", "CorrectHorse123!")
	otherWs := h.request(http.MethodPost, "/api/workspaces", map[string]any{"name": "Other Org", "slug": "other-org"}, owner.AccessToken, nil, true)
	if otherWs.status != http.StatusCreated {
		t.Fatalf("other workspace: %d %s", otherWs.status, otherWs.body)
	}
	var other apigen.WorkspaceSummary
	otherWs.decode(t, &other)
	eve, _ := h.inviteAndAcceptNewUser(owner.AccessToken, other.Id, "eve@other.com", "owner", "Eve", "CorrectHorse123!")

	base := "/api/workspaces/" + ws.String()
	created := h.request(http.MethodPost, base+"/bots", map[string]any{
		"name": "Atlas", "avatarSeed": "atlas-seed", "llmMode": "cli", "llmProvider": "anthropic", "llmModel": "sonnet",
	}, owner.AccessToken, nil, true)
	if created.status != http.StatusCreated {
		t.Fatalf("create bot: %d %s", created.status, created.body)
	}
	var bot apigen.Bot
	created.decode(t, &bot)
	opened := h.request(http.MethodPost, base+"/bots/"+bot.Id.String()+"/dm", nil, owner.AccessToken, nil, true)
	var conversation apigen.ChatConversation
	opened.decode(t, &conversation)

	const imagePath = "/home/bot/.claude/projects/-home-bot/fe505481-b639-48b3-95ab-6e1db702084f/tool-results/mcp-botbox-desktop-blob-1-abcd.png"
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'I', 'H', 'D', 'R'}
	secret := []byte("SECRET-KEY-MATERIAL")
	asPG := func(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }
	message, err := h.queries.CreateChatMessage(context.Background(), db.CreateChatMessageParams{
		ID: asPG(uuid.New()), WorkspaceID: asPG(ws), ConversationID: asPG(conversation.Id),
		SenderType: "bot", SenderBotID: asPG(bot.Id), Kind: "tool",
		Content: "[Image: source: " + imagePath + "]\nbrowser screenshot", Metadata: []byte("{}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	textMessage, err := h.queries.CreateChatMessage(context.Background(), db.CreateChatMessageParams{
		ID: asPG(uuid.New()), WorkspaceID: asPG(ws), ConversationID: asPG(conversation.Id),
		SenderType: "user", SenderUserID: asPG(owner.User.Id), Kind: "text",
		Content: "[Image: source: " + imagePath + "]", Metadata: []byte("{}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	escaped, err := h.queries.CreateChatMessage(context.Background(), db.CreateChatMessageParams{
		ID: asPG(uuid.New()), WorkspaceID: asPG(ws), ConversationID: asPG(conversation.Id),
		SenderType: "bot", SenderBotID: asPG(bot.Id), Kind: "tool",
		Content: "[Image: source: /home/bot/.ssh/id_rsa]", Metadata: []byte("{}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	imageURL := base + "/conversations/" + conversation.Id.String() + "/messages/" + uuid.UUID(message.ID.Bytes).String() + "/tool-image"

	if r := h.request(http.MethodGet, imageURL, nil, "", nil, false); r.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated=%d", r.status)
	}
	for _, token := range []string{member.AccessToken, eve.AccessToken} {
		if r := h.request(http.MethodGet, imageURL, nil, token, nil, false); r.status != http.StatusNotFound {
			t.Fatalf("foreign reader=%d body=%s", r.status, r.body)
		}
	}
	if r := h.request(http.MethodGet, base+"/conversations/"+conversation.Id.String()+"/messages/not-a-uuid/tool-image", nil, owner.AccessToken, nil, false); r.status != http.StatusBadRequest {
		t.Fatalf("bad id=%d", r.status)
	}
	if r := h.request(http.MethodGet, imageURL, nil, owner.AccessToken, nil, false); r.status != http.StatusConflict {
		t.Fatalf("stopped computer=%d body=%s", r.status, r.body)
	}

	started := h.request(http.MethodPost, base+"/bots/"+bot.Id.String()+"/environment/start", nil, owner.AccessToken, nil, true)
	if started.status != http.StatusOK {
		t.Fatalf("start=%d %s", started.status, started.body)
	}
	var env apigen.EnvironmentStateResponse
	started.decode(t, &env)
	if err = h.computeDrv.WriteFile(context.Background(), compute.EnvID(env.EnvId), imagePath, png); err != nil {
		t.Fatal(err)
	}
	if err = h.computeDrv.WriteFile(context.Background(), compute.EnvID(env.EnvId), "/home/bot/.ssh/id_rsa", secret); err != nil {
		t.Fatal(err)
	}
	notImage := "/home/bot/.claude/projects/-home-bot/fe505481-b639-48b3-95ab-6e1db702084f/tool-results/notes.png"
	if err = h.computeDrv.WriteFile(context.Background(), compute.EnvID(env.EnvId), notImage, []byte("not a png SECRET-KEY-MATERIAL")); err != nil {
		t.Fatal(err)
	}
	fakeImage, err := h.queries.CreateChatMessage(context.Background(), db.CreateChatMessageParams{
		ID: asPG(uuid.New()), WorkspaceID: asPG(ws), ConversationID: asPG(conversation.Id),
		SenderType: "bot", SenderBotID: asPG(bot.Id), Kind: "tool",
		Content: "[Image: source: " + notImage + "]", Metadata: []byte("{}"),
	})
	if err != nil {
		t.Fatal(err)
	}

	got := h.request(http.MethodGet, imageURL, nil, owner.AccessToken, nil, false)
	if got.status != http.StatusOK || got.headers.Get("Content-Type") != "image/png" || !strings.HasPrefix(string(got.body), "\x89PNG") {
		t.Fatalf("image=%d type=%s", got.status, got.headers.Get("Content-Type"))
	}
	if got.headers.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff")
	}
	for _, id := range []pgtype.UUID{textMessage.ID, escaped.ID, fakeImage.ID} {
		path := base + "/conversations/" + conversation.Id.String() + "/messages/" + uuid.UUID(id.Bytes).String() + "/tool-image"
		r := h.request(http.MethodGet, path, nil, owner.AccessToken, nil, false)
		if r.status != http.StatusNotFound || strings.Contains(string(r.body), "SECRET-KEY-MATERIAL") || strings.Contains(string(r.body), "id_rsa") {
			t.Fatalf("rejected image=%d body=%s", r.status, r.body)
		}
	}
	cross := "/api/workspaces/" + other.Id.String() + "/conversations/" + conversation.Id.String() + "/messages/" + uuid.UUID(message.ID.Bytes).String() + "/tool-image"
	if r := h.request(http.MethodGet, cross, nil, eve.AccessToken, nil, false); r.status != http.StatusNotFound || strings.Contains(string(r.body), "PNG") {
		t.Fatalf("cross workspace=%d", r.status)
	}
}
