// SPDX-License-Identifier: AGPL-3.0-only

package groups

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/notify"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// TeamStatusTool asks for the state and latest progress of a group colleague.
	TeamStatusTool = "get_team_status"
	// TeamProgressTool posts a progress update during the current group turn.
	TeamProgressTool       = "post_team_progress"
	teamHeartbeatFreshness = 45 * time.Second
	teamStatusInterval     = 30 * time.Second
	teamProgressInterval   = time.Minute
	teamStatusLimit        = 12
	teamProgressLimit      = 4
	teamBlockerLimit       = 3
	teamTextLimit          = 600
)

var (
	// ErrTeamToolUnavailable covers missing, inactive, and unauthorized scopes alike.
	ErrTeamToolUnavailable = errors.New("team tools require an active group turn and an eligible member bot")
	// ErrTeamToolArguments means the call did not match its tool schema.
	ErrTeamToolArguments = errors.New("invalid team tool arguments")
	// ErrTeamUpdateLimit means a turn used its allowance for this kind of update.
	ErrTeamUpdateLimit = errors.New("team update limit reached for this turn")
)

// TeamToolScope identifies the invoking bot and turn from its trusted transport.
type TeamToolScope struct {
	WorkspaceID, BotID, TurnID uuid.UUID
}

// TeamTools handles group coordination without scheduling another bot turn.
type TeamTools struct {
	Pool   *pgxpool.Pool
	Now    func() time.Time
	Signal notify.Signal
}

// TeamToolDefinition describes one control-plane tool for both model transports.
type TeamToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// TeamToolResult keeps database and transport errors out of model context.
func TeamToolResult(result string, err error) string {
	if err == nil {
		return result
	}
	for _, safe := range []error{ErrTeamToolUnavailable, ErrTeamToolArguments, ErrTeamUpdateLimit} {
		if errors.Is(err, safe) {
			return safe.Error()
		}
	}
	return "team tool temporarily unavailable"
}

// TeamToolDefinitions returns fresh schemas. CLI calls must identify their current turn.
func TeamToolDefinitions(cli bool) []TeamToolDefinition {
	schema := func(required string, props map[string]any) map[string]any {
		fields := []string{required}
		if cli {
			fields = append(fields, "turn_id")
			props["turn_id"] = map[string]string{"type": "string", "description": "The current turn ID from your group instructions."}
		}
		return map[string]any{"type": "object", "properties": props, "required": fields, "additionalProperties": false}
	}
	return []TeamToolDefinition{
		{
			Name: TeamStatusTool, Description: "Check a colleague's current state in this group and show the checked status in chat. Returns their latest progress if available. Use when you need to know whether a colleague is still working.",
			InputSchema: schema("bot", map[string]any{"bot": map[string]string{"type": "string", "description": "Exact name of the colleague in the group roster."}}),
		},
		{
			Name: TeamProgressTool, Description: "Post a brief milestone or blocker in this group immediately and continue the current task. Keep credentials and other secrets out of the text.",
			InputSchema: schema("text", map[string]any{
				"text":    map[string]any{"type": "string", "minLength": 1, "maxLength": teamTextLimit},
				"blocker": map[string]string{"type": "boolean", "description": "True when you are blocked and need help. Defaults to false."},
			}),
		},
	}
}

type teamToolArgs struct {
	Bot     *string         `json:"bot"`
	Text    *string         `json:"text"`
	Blocker json.RawMessage `json:"blocker"`
	TurnID  *uuid.UUID      `json:"turn_id"`
}

type teamStatus struct {
	BotID        uuid.UUID  `json:"botId"`
	Name         string     `json:"name"`
	State        string     `json:"state"`
	WorkingSince *time.Time `json:"workingSince,omitempty"`
	Progress     string     `json:"progress,omitempty"`
	ProgressAt   *time.Time `json:"progressAt,omitempty"`
}

type teamUpdateMeta struct {
	TurnID  uuid.UUID   `json:"turnId"`
	Blocker bool        `json:"blocker,omitempty"`
	Status  *teamStatus `json:"status,omitempty"`
}

// Call commits a team update before signaling readers. Authorization and limits
// are checked under the group and turn locks so concurrent calls cannot bypass them.
func (s TeamTools) Call(ctx context.Context, scope TeamToolScope, name string, raw json.RawMessage) (string, error) {
	args, err := parseTeamToolArgs(name, raw, scope.TurnID)
	if err != nil {
		return "", err
	}
	if s.Pool == nil || scope.WorkspaceID == uuid.Nil || scope.BotID == uuid.Nil || scope.TurnID == uuid.Nil {
		return "", ErrTeamToolUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	turn, err := q.GetBotTurn(ctx, db.GetBotTurnParams{WorkspaceID: pgID(scope.WorkspaceID), ID: pgID(scope.TurnID)})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && turn.BotID != pgID(scope.BotID)) {
		return "", ErrTeamToolUnavailable
	}
	if err != nil {
		return "", err
	}
	group, err := q.GetGroupForUpdate(ctx, db.GetGroupForUpdateParams{WorkspaceID: turn.WorkspaceID, ID: turn.ConversationID})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && name == TeamStatusTool && !group.BotTalk) {
		return "", ErrTeamToolUnavailable
	}
	if err != nil {
		return "", err
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	_, err = q.LockTeamToolTurn(ctx, db.LockTeamToolTurnParams{
		WorkspaceID: turn.WorkspaceID, ConversationID: turn.ConversationID, ID: turn.ID, BotID: turn.BotID,
		FreshSince: timestamp(now.Add(-teamHeartbeatFreshness)),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrTeamToolUnavailable
	}
	if err != nil {
		return "", err
	}
	runtime, err := q.GetBotRuntime(ctx, db.GetBotRuntimeParams{WorkspaceID: turn.WorkspaceID, ID: turn.BotID})
	if err != nil {
		return "", err
	}
	if runtime.Paused || runtime.Controlled {
		return "", ErrTeamToolUnavailable
	}
	updates, err := q.ListTeamTurnUpdates(ctx, db.ListTeamTurnUpdatesParams{
		WorkspaceID: turn.WorkspaceID, ConversationID: turn.ConversationID, TurnID: scope.TurnID.String(),
	})
	if err != nil {
		return "", err
	}
	meta := teamUpdateMeta{TurnID: scope.TurnID}
	kind, content, response := "progress", "", `{"posted":true}`
	if name == TeamStatusTool {
		status, err := lookupTeamStatus(ctx, q, turn, *args.Bot, now)
		if err != nil {
			return "", err
		}
		result, err := json.Marshal(status)
		if err != nil {
			return "", err
		}
		if !allowTeamStatus(updates, status.BotID, now) {
			return string(result), nil
		}
		meta.Status = &status
		kind, content, response = "team_status", status.Name+": "+status.State, string(result)
	} else {
		content = *args.Text
		meta.Blocker = string(args.Blocker) == "true"
		if reason, err := allowTeamProgress(updates, content, meta.Blocker, now); reason != "" || err != nil {
			return reason, err
		}
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return "", err
	}
	if err = q.CreateTeamUpdate(ctx, db.CreateTeamUpdateParams{
		WorkspaceID: turn.WorkspaceID, ConversationID: turn.ConversationID, SenderBotID: turn.BotID,
		Kind: kind, Content: content, Metadata: data, CreatedAt: timestamp(now),
	}); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	if s.Signal != nil {
		// An empty notification batch still refreshes the chat. The transcript
		// is durable, so a signal failure cannot cause the write to be retried.
		s.Signal(ctx, scope.WorkspaceID, uuid.UUID(turn.ConversationID.Bytes), nil)
	}
	return response, nil
}

func parseTeamToolArgs(name string, raw json.RawMessage, turnID uuid.UUID) (teamToolArgs, error) {
	var args teamToolArgs
	if len(raw) > 4096 {
		return args, ErrTeamToolArguments
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&args) != nil || decoder.Decode(new(any)) != io.EOF || (args.TurnID != nil && *args.TurnID != turnID) {
		return args, ErrTeamToolArguments
	}
	switch name {
	case TeamStatusTool:
		if !validTeamText(args.Bot, 255) || args.Text != nil || len(args.Blocker) != 0 {
			return args, ErrTeamToolArguments
		}
	case TeamProgressTool:
		if !validTeamText(args.Text, teamTextLimit) || args.Bot != nil || (len(args.Blocker) != 0 && string(args.Blocker) != "true" && string(args.Blocker) != "false") {
			return args, ErrTeamToolArguments
		}
	default:
		return args, ErrTeamToolArguments
	}
	return args, nil
}

func validTeamText(text *string, limit int) bool {
	if text == nil {
		return false
	}
	*text = strings.TrimSpace(*text)
	return *text != "" && utf8.RuneCountInString(*text) <= limit && !strings.ContainsRune(*text, 0)
}

func lookupTeamStatus(ctx context.Context, q *db.Queries, turn db.BotTurn, name string, now time.Time) (teamStatus, error) {
	peers, err := q.FindTeamPeer(ctx, db.FindTeamPeerParams{WorkspaceID: turn.WorkspaceID, ConversationID: turn.ConversationID, BotName: name})
	if err != nil {
		return teamStatus{}, err
	}
	if len(peers) != 1 || peers[0] == turn.BotID {
		return teamStatus{}, ErrTeamToolUnavailable
	}
	return readTeamStatus(ctx, q, turn.WorkspaceID, turn.ConversationID, peers[0], now)
}

func readTeamStatus(ctx context.Context, q *db.Queries, workspace, conversation, bot pgtype.UUID, now time.Time) (teamStatus, error) {
	activity, err := q.GetTeamPeerActivity(ctx, db.GetTeamPeerActivityParams{WorkspaceID: workspace, ConversationID: conversation, MemberID: bot})
	if err != nil {
		return teamStatus{}, err
	}
	return statusFromActivity(bot, activity, now), nil
}

func statusFromActivity(bot pgtype.UUID, activity db.GetTeamPeerActivityRow, now time.Time) teamStatus {
	status := teamStatus{BotID: uuid.UUID(bot.Bytes), Name: activity.Name, State: activity.TurnStatus}
	switch {
	case activity.TurnStatus == "running" && (!activity.HeartbeatAt.Valid || activity.HeartbeatAt.Time.Before(now.Add(-teamHeartbeatFreshness))):
		status.State = "unknown"
	case activity.TurnStatus == "running" && activity.StopRequested:
		status.State = "stopping"
	case activity.TurnStatus == "running":
		status.State = "working"
		started := activity.UpdatedAt.Time.UTC()
		status.WorkingSince = &started
	case activity.AwaitingApproval && activity.TurnStatus != "waiting_secret":
		status.State = "waiting_approval"
	}
	if activity.ProgressAt.Valid && activity.Progress != "" {
		at := activity.ProgressAt.Time.UTC()
		status.Progress, status.ProgressAt = activity.Progress, &at
	}
	return status
}

func allowTeamProgress(rows []db.ListTeamTurnUpdatesRow, text string, blocker bool, now time.Time) (string, error) {
	count, limit := 0, teamProgressLimit
	if blocker {
		limit = teamBlockerLimit
	}
	for _, row := range rows {
		if row.Kind != "progress" {
			continue
		}
		var meta teamUpdateMeta
		if json.Unmarshal(row.Metadata, &meta) != nil || meta.Blocker != blocker {
			continue
		}
		if row.Content == text {
			return `{"posted":false,"reason":"already posted"}`, nil
		}
		if !blocker && row.CreatedAt.Time.After(now.Add(-teamProgressInterval)) {
			return `{"posted":false,"reason":"wait one minute between routine updates"}`, nil
		}
		count++
	}
	if count >= limit {
		return "", ErrTeamUpdateLimit
	}
	return "", nil
}

func allowTeamStatus(rows []db.ListTeamTurnUpdatesRow, bot uuid.UUID, now time.Time) bool {
	count := 0
	for _, row := range rows {
		if row.Kind != "team_status" {
			continue
		}
		count++
		var meta teamUpdateMeta
		if row.CreatedAt.Time.After(now.Add(-teamStatusInterval)) && json.Unmarshal(row.Metadata, &meta) == nil && meta.Status != nil && meta.Status.BotID == bot {
			return false
		}
	}
	return count < teamStatusLimit
}
