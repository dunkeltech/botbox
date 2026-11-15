package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"

	"github.com/dunkeltech/botbox/backend/internal/approvals"
	"github.com/dunkeltech/botbox/backend/internal/authbroker"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/memory"
	"github.com/dunkeltech/botbox/backend/internal/secretrequests"
	"github.com/dunkeltech/botbox/backend/internal/secrets/mitmproxy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type turnPublisher func(context.Context, Session, Event) error

func newTurnPublisher(send turnPublisher) turnPublisher {
	runID := uuid.NewString()
	var sequence atomic.Uint64
	return func(ctx context.Context, session Session, event Event) error {
		if send == nil {
			return nil
		}
		event.RunID = runID
		event.BotID = session.BotID.String()
		event.TurnID = session.TurnID.String()
		event.Sequence = sequence.Add(1)
		return send(ctx, session, event)
	}
}

var errUseSavedCredential = errors.New("continue with saved GitLab credential")

type turnEvents struct {
	worker                *Worker
	queries               *db.Queries
	turn                  db.BotTurn
	session               *Session
	publish               turnPublisher
	text                  replyText
	waitingSecret         bool
	savedCredentialTried  map[string]bool
	teamToolCalls         map[string]bool
	savedCredentialPrompt string
}

func (s *turnEvents) emit(ctx context.Context, ev Event) error {
	ev.BotID = s.session.BotID.String()
	ev.TurnID = s.session.TurnID.String()

	// Provider completion is only a model step, not completion of the turn.
	if ev.Kind == EventKindStatus && (ev.Status == "idle" || ev.Status == "offline") {
		return nil
	}
	switch ev.Kind {
	case EventKindTextDelta, EventKindMessage:
		if err := s.text.Add(ev); err != nil {
			return err
		}
	case EventKindToolCall, EventKindToolResult:
		// Team tools persist their own scoped messages and can reject private text.
		if ev.Kind == EventKindToolCall && s.session.TeamTool != nil && isTeamTool(ev.ToolName) {
			s.teamToolCalls[ev.ToolID] = true
			return nil
		}
		if ev.Kind == EventKindToolResult && s.teamToolCalls[ev.ToolID] {
			delete(s.teamToolCalls, ev.ToolID)
			return nil
		}
		if ev.Kind == EventKindToolCall {
			if IsRequestSecretTool(ev.ToolName) {
				if err := s.requestSecret(ctx, ev); err != nil {
					return err
				}
			}
			ev.Text = ev.ToolName + "\n" + string(ev.ToolInput)
			// Memory entries describe policies, so approval scanning applies only to other tools.
			if !memory.IsMemoryTool(ev.ToolName) {
				if err := approvals.NoteTool(ctx, s.worker.Pool, s.turn, ev.Text, s.worker.Signal); err != nil {
					return err
				}
			}
		}
		if err := s.persistTrace(ctx, ev, "bot", "tool", ev.Text); err != nil {
			return err
		}
	case EventKindCard:
		if strings.TrimSpace(ev.Text) == "" {
			return errors.New("card text is required")
		}
		data := ev.Metadata
		if len(data) == 0 {
			data = []byte("{}")
		} else if !json.Valid(data) {
			return errors.New("invalid card metadata")
		}
		if err := s.persistMessage(ctx, "bot", "card", ev.Text, data); err != nil {
			return err
		}
	case EventKindError:
		// Keep exit-zero tool denials visible after clients reconnect.
		if err := s.persistTrace(ctx, ev, "system", "system", ev.ErrorMessage); err != nil {
			return err
		}
	case EventKindUsage:
		if err := s.recordUsage(ctx, ev); err != nil {
			return err
		}
	}
	return s.publish(ctx, *s.session, ev)
}

func (s *turnEvents) requestSecret(ctx context.Context, ev Event) error {
	var args struct {
		Kind               string `json:"kind"`
		AdapterID          string `json:"adapter_id"`
		TargetOrigin       string `json:"target_origin"`
		Reason             string `json:"reason"`
		EnvPlaceholderName string `json:"env_placeholder_name"`
		UsernameHint       string `json:"username_hint"`
	}
	if json.Unmarshal(ev.ToolInput, &args) != nil || strings.TrimSpace(args.Reason) == "" {
		return nil
	}

	var savedAdapterID string
	switch args.Kind {
	case secretrequests.KindCLIEgress:
		adapter, ok := mitmproxy.LookupAdapter(args.AdapterID)
		if !ok || mitmproxy.ValidateEnvPlaceholderName(args.EnvPlaceholderName) != nil {
			return nil
		}
		savedAdapterID = adapter.ID
	case secretrequests.KindBrowserLogin:
		_, ok := authbroker.LookupLoginAdapter(args.AdapterID)
		if s.worker.AuthBroker == nil || !ok || strings.TrimSpace(args.EnvPlaceholderName) != "" {
			return nil
		}
	default:
		return nil
	}

	res, err := secretrequests.CreateRequest(ctx, s.worker.Pool, secretrequests.CreateInput{
		WorkspaceID:        s.session.WorkspaceID,
		BotID:              s.session.BotID,
		ConversationID:     s.session.ConversationID,
		TurnID:             s.session.TurnID,
		ToolCallID:         ev.ToolID,
		Kind:               args.Kind,
		AdapterID:          args.AdapterID,
		TargetOrigin:       args.TargetOrigin,
		Reason:             args.Reason,
		EnvPlaceholderName: args.EnvPlaceholderName,
		UsernameHint:       args.UsernameHint,
		TrySavedGitLab:     !s.savedCredentialTried[savedAdapterID],
		Broker:             s.worker.AuthBroker,
		Now:                s.worker.now(),
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		code := secretrequests.FailureCode(err)
		failure := Event{
			Kind: EventKindToolResult, TurnID: ev.TurnID, ToolID: ev.ToolID, ToolName: ev.ToolName,
			IsError: true, ErrorCode: code, Text: secretrequests.FailureMessage(code),
		}
		if persistErr := s.persistTrace(ctx, failure, "bot", "tool", failure.Text); persistErr != nil {
			return persistErr
		}
		_ = s.publish(ctx, *s.session, failure)
		return WithFailureCode(code, err)
	}
	if res.SavedCredentialPrompt != "" {
		s.savedCredentialTried[savedAdapterID] = true
		s.savedCredentialPrompt = res.SavedCredentialPrompt
		return errUseSavedCredential
	}
	s.waitingSecret = true
	_ = s.publish(ctx, *s.session, Event{Kind: EventKindCard, Text: res.Card.Reason, Metadata: res.Message.Metadata})
	return ErrTurnWaitingSecret
}

func (s *turnEvents) persistTrace(ctx context.Context, ev Event, sender, kind, content string) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return s.persistMessage(ctx, sender, kind, content, data)
}

func (s *turnEvents) persistMessage(ctx context.Context, sender, kind, content string, metadata json.RawMessage) error {
	var botID pgtype.UUID
	if sender == "bot" {
		botID = s.turn.BotID
	}
	_, err := s.queries.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ID: pgID(uuid.New()), WorkspaceID: s.turn.WorkspaceID, ConversationID: s.turn.ConversationID,
		SenderType: sender, SenderBotID: botID, Kind: kind, Content: content, Metadata: metadata,
	})
	return err
}

func (s *turnEvents) recordUsage(ctx context.Context, ev Event) error {
	metered, err := s.queries.HasProxyTurnUsage(ctx, db.HasProxyTurnUsageParams{WorkspaceID: s.turn.WorkspaceID, TurnID: s.turn.ID})
	if err != nil || metered {
		return err
	}
	if ev.InputTokens < 0 || ev.OutputTokens < 0 || ev.CacheTokens < 0 {
		return errors.New("invalid usage")
	}
	return s.queries.RecordTurnUsage(ctx, db.RecordTurnUsageParams{
		WorkspaceID: s.turn.WorkspaceID, BotID: s.turn.BotID, TurnID: s.turn.ID, RequestID: pgID(uuid.New()),
		Source: "cli_result", CreatedAt: pgtype.Timestamptz{Time: s.worker.now(), Valid: true}, Model: s.session.Bot.LlmModel,
		InputTokens: ev.InputTokens, OutputTokens: ev.OutputTokens, CacheTokens: ev.CacheTokens,
	})
}
