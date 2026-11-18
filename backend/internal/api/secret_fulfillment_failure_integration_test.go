// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/authbroker"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/secretrequests"
	"github.com/jackc/pgx/v5/pgtype"
)

type cancelAfterSecretSubmit struct {
	authbroker.Client
	cancel context.CancelFunc
}

func (b *cancelAfterSecretSubmit) SubmitCredentials(ctx context.Context, in authbroker.SubmitCredentialsInput) (authbroker.SubmitCredentialsResult, error) {
	result, err := b.Client.SubmitCredentials(ctx, in)
	if err == nil && b.cancel != nil {
		b.cancel()
	}
	return result, err
}

func TestSecretFulfillmentRollsBackAndClosesClaim(t *testing.T) {
	for _, tc := range []struct {
		name       string
		kind       apigen.SecretRequestKind
		call       string
		cancel     bool
		keepPaused bool
	}{
		{"cli_egress", apigen.CliEgress, `{"kind":"cli_egress","adapter_id":"github_api","reason":"Read releases","env_placeholder_name":"GH_TOKEN"}`, false, false},
		{"browser_login", apigen.BrowserLogin, `{"kind":"browser_login","adapter_id":"gitlab_web","reason":"Sign in to GitLab"}`, false, false},
		{"browser_client_canceled", apigen.BrowserLogin, `{"kind":"browser_login","adapter_id":"gitlab_web","reason":"Sign in to GitLab"}`, true, false},
		{"another_request_is_pending", apigen.CliEgress, `{"kind":"cli_egress","adapter_id":"github_api","reason":"Read releases","env_placeholder_name":"GH_TOKEN"}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			queue := &fakeTurnQueue{}
			broker := authbroker.NewService(authbroker.NewMemoryBrowserEngine())
			submitter := &cancelAfterSecretSubmit{Client: broker}
			h := newTestHarness(t, nil, harnessOpts{turnQueue: queue, authBroker: submitter})
			broker.SetNow(h.clock.Now)
			owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
			ws := owner.ActiveWorkspace.Id
			base := "/api/workspaces/" + ws.String()
			bot := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas")
			members := []any{groupBot(bot.Id, "lead")}
			var peer apigen.Bot
			if tc.keepPaused {
				peer = createPiBot(t, h, base, owner.AccessToken, "Nova", "nova")
				members = append(members, groupBot(peer.Id, "peer"))
			}
			group := askGroup(t, h, base, owner.AccessToken, "Ops", bot.Id, false, 20, []any{}, members)
			if _, err := h.queries.UpsertEgressRule(ctx, db.UpsertEgressRuleParams{
				BotID: pgUUID(bot.Id), WorkspaceID: pgUUID(ws), Policy: "allowlist",
				AllowedHosts: []string{"api.github.com"}, BlockedHosts: []string{}, Revision: 1,
			}); err != nil {
				t.Fatal(err)
			}
			worker := &engine.Worker{
				Pool: h.pool, Now: h.clock.Now, AuthBroker: broker, Enqueue: queue.Enqueue,
				Execute: func(_ context.Context, _ engine.Session, emit func(engine.Event) error) error {
					return emit(engine.Event{Kind: engine.EventKindToolCall, ToolID: "request", ToolName: "request_secret", ToolInput: []byte(tc.call)})
				},
			}
			postGroup(t, h, base, owner.AccessToken, group.Id, "@Atlas request a secret")
			drainTurns(t, worker, queue, 2)

			before := groupHistory(t, h, base, owner.AccessToken, group.Id)
			var summary *apigen.SecretRequestSummary
			var inputID pgtype.UUID
			for _, message := range before.Messages {
				if message.SecretRequest != nil {
					summary = message.SecretRequest
				}
				if message.Kind == apigen.ChatMessageKindText {
					inputID = pgUUID(message.Id)
				}
			}
			if summary == nil || summary.Kind != tc.kind || summary.Status != apigen.SecretRequestStatusPending || len(before.Turns) != 1 {
				t.Fatal("expected a pending request and one waiting turn")
			}
			if tc.keepPaused {
				turn, err := h.queries.CreateBotTurn(ctx, db.CreateBotTurnParams{
					WorkspaceID: pgUUID(ws), ConversationID: pgUUID(group.Id), BotID: pgUUID(peer.Id), MessageID: inputID,
				})
				if err != nil {
					t.Fatal(err)
				}
				now := h.clock.Now()
				if err := h.queries.StartBotTurn(ctx, db.StartBotTurnParams{
					WorkspaceID: pgUUID(ws), ID: turn.ID, AsOf: pgtype.Timestamptz{Time: now, Valid: true},
					RunDeadlineAt: pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true},
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := secretrequests.CreateRequest(ctx, h.pool, secretrequests.CreateInput{
					WorkspaceID: ws, BotID: peer.Id, ConversationID: group.Id, TurnID: uuidFromPG(turn.ID),
					Kind: secretrequests.KindCLIEgress, AdapterID: "github_api", Reason: "Read a separate release", Now: now,
				}); err != nil {
					t.Fatal(err)
				}
			}

			// Reject the final card write after the request and any grant have changed in the transaction.
			if !tc.cancel {
				if _, err := h.pool.Exec(ctx, `
				CREATE FUNCTION reject_fulfilled_card() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN
					IF NEW.kind = 'secret_request' AND NEW.metadata->>'status' = 'fulfilled' THEN
						RAISE EXCEPTION 'injected card write failure';
					END IF;
					RETURN NEW;
				END;
				$$;
				CREATE TRIGGER reject_fulfilled_card BEFORE UPDATE ON messages
				FOR EACH ROW EXECUTE FUNCTION reject_fulfilled_card();
				`); err != nil {
					t.Fatal(err)
				}
			}

			const credential = "test-only-credential-never-leak"
			path := base + "/secret-requests/" + summary.SecretRequestId.String() + "/fulfill"
			body := map[string]string{"secretValue": credential, "username": "alice", "scope": "ephemeral"}
			var status int
			var responseBody string
			if tc.cancel {
				callCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				submitter.cancel = cancel
				recorder := httptest.NewRecorder()
				encoded, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequestWithContext(callCtx, http.MethodPost, path, strings.NewReader(string(encoded)))
				req.Header.Set("Authorization", "Bearer "+owner.AccessToken)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-BotBox-CSRF", "1")
				h.handler.ServeHTTP(recorder, req)
				status, responseBody = recorder.Code, recorder.Body.String()
				if !errors.Is(callCtx.Err(), context.Canceled) {
					t.Fatal("expected cancellation after broker accepted credentials")
				}
			} else {
				reply := h.request(http.MethodPost, path, body, owner.AccessToken, nil, true)
				status, responseBody = reply.status, string(reply.body)
			}
			if status != http.StatusServiceUnavailable {
				t.Fatalf("fulfillment status = %d, want 503", status)
			}
			if strings.Contains(responseBody, credential) || strings.Contains(h.logs.String(), credential) {
				t.Fatal("credential was exposed on failure")
			}
			row, err := h.queries.GetSecretRequest(ctx, db.GetSecretRequestParams{WorkspaceID: pgUUID(ws), ID: pgUUID(summary.SecretRequestId)})
			if err != nil || row.Status != secretrequests.StatusFailed {
				t.Fatalf("request status = %q, error = %v, want failed", row.Status, err)
			}

			after := groupHistory(t, h, base, owner.AccessToken, group.Id)
			var failedCard bool
			for _, message := range after.Messages {
				if message.SecretRequest != nil && message.SecretRequest.SecretRequestId == summary.SecretRequestId {
					failedCard = message.SecretRequest.Status == apigen.SecretRequestStatusFailed
				}
			}
			var failedTurn bool
			for _, turn := range after.Turns {
				if turn.Id == before.Turns[0].Id {
					failedTurn = turn.Status == apigen.ChatTurnStatusFailed
				}
			}
			if !failedCard || !failedTurn {
				t.Fatal("failed card and failed turn must be visible in history")
			}
			if getGroup(t, h, base, owner.AccessToken, group.Id).Paused != tc.keepPaused || len(queue.jobs) != 0 {
				t.Fatal("group pause must follow the remaining request without queuing a continuation")
			}
			var leases int
			if err := h.pool.QueryRow(ctx, "SELECT count(*) FROM secret_leases WHERE workspace_id = $1 AND secret_request_id = $2", ws, summary.SecretRequestId).Scan(&leases); err != nil || leases != 0 {
				t.Fatalf("leases = %d, error = %v", leases, err)
			}
			if summary.BrokerSessionId != nil {
				session, err := h.queries.GetBrokerSession(ctx, db.GetBrokerSessionParams{WorkspaceID: pgUUID(ws), ID: pgUUID(*summary.BrokerSessionId)})
				if err != nil || session.Status != authbroker.StatusRevoked {
					t.Fatalf("stored broker session status = %q, error = %v", session.Status, err)
				}
				if _, err := broker.GetSession(ctx, ws, *summary.BrokerSessionId); !errors.Is(err, authbroker.ErrSessionNotFound) {
					t.Fatalf("expected revoked browser session, got %v", err)
				}
			}
			if reply := h.request(http.MethodPost, path, body, owner.AccessToken, nil, true); reply.status != http.StatusConflict {
				t.Fatalf("retry status = %d, want 409", reply.status)
			}
		})
	}
}

func TestSecretRequestFailureUsesTurnEventSequence(t *testing.T) {
	queue := &fakeTurnQueue{}
	h := newTestHarness(t, nil, harnessOpts{turnQueue: queue})
	owner, _ := h.completeSetup("owner@example.com", "CorrectHorse123!", "Owner", "Workspace", "workspace")
	ws := owner.ActiveWorkspace.Id
	base := "/api/workspaces/" + ws.String()
	bot := createPiBot(t, h, base, owner.AccessToken, "Atlas", "atlas")
	group := askGroup(t, h, base, owner.AccessToken, "Ops", bot.Id, false, 20, []any{}, []any{groupBot(bot.Id, "lead")})

	var events []engine.Event
	worker := &engine.Worker{
		Pool: h.pool, Now: h.clock.Now, Enqueue: queue.Enqueue,
		Publish: func(_ context.Context, _ engine.Session, event engine.Event) error {
			events = append(events, event)
			return nil
		},
		Execute: func(_ context.Context, _ engine.Session, emit func(engine.Event) error) error {
			input := `{"kind":"cli_egress","adapter_id":"github_api","reason":"` + strings.Repeat("x", secretrequests.MaxReasonBytes+1) + `","env_placeholder_name":"GH_TOKEN"}`
			return emit(engine.Event{Kind: engine.EventKindToolCall, ToolID: "request", ToolName: "request_secret", ToolInput: []byte(input)})
		},
	}
	postGroup(t, h, base, owner.AccessToken, group.Id, "@Atlas request a secret")
	if len(queue.jobs) != 1 {
		t.Fatal("expected one turn")
	}
	turnID := queue.jobs[0].TurnID
	drainTurns(t, worker, queue, 1)

	if len(events) != 3 || events[1].Kind != engine.EventKindToolResult || !events[1].IsError || events[1].ErrorCode != "secret_request.invalid_input" {
		t.Fatalf("expected thinking, safe request failure, idle: %+v", events)
	}
	runID := events[0].RunID
	for i, event := range events {
		if event.RunID != runID || runID == "" || event.TurnID != turnID.String() || event.BotID != bot.Id.String() || event.Sequence != uint64(i+1) {
			t.Fatalf("incorrect run envelope: %+v", event)
		}
	}
	turn, err := h.queries.GetBotTurn(t.Context(), db.GetBotTurnParams{WorkspaceID: pgUUID(ws), ID: pgUUID(turnID)})
	if err != nil || turn.Status != "failed" {
		t.Fatalf("turn %s status = %q, error = %v", turnID, turn.Status, err)
	}
}
