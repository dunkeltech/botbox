package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	// ToolSave persists a new entry in the bot's knowledge base.
	ToolSave = "memory_save"
	// ToolSearch searches the bot's knowledge base using full-text and tag matching.
	ToolSearch = "memory_search"
	// ToolUpdate updates an existing entry in the bot's knowledge base.
	ToolUpdate = "memory_update"
	// ToolDelete removes an entry from the bot's knowledge base.
	ToolDelete = "memory_delete"
)

// ToolScope pins tool execution to the trusted workspace, bot, and running turn.
type ToolScope struct {
	WorkspaceID uuid.UUID
	BotID       uuid.UUID
	TurnID      uuid.UUID
}

// ToolDefinition describes one memory tool for API and MCP transport layers.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// IsMemoryTool reports whether name is one of the bot knowledge base tools
// (including CLI MCP prefixes like mcp__botbox_memory__ or botbox_memory.).
func IsMemoryTool(name string) bool {
	leaf := NormalizeToolName(name)
	switch leaf {
	case ToolSave, ToolSearch, ToolUpdate, ToolDelete:
		return true
	default:
		return false
	}
}

// NormalizeToolName strips MCP transport prefixes and normalizes dashes.
func NormalizeToolName(name string) string {
	trimmed := strings.TrimSpace(name)
	if idx := strings.LastIndex(trimmed, "__"); idx >= 0 {
		trimmed = trimmed[idx+2:]
	} else if idx := strings.LastIndex(trimmed, "."); idx >= 0 {
		trimmed = trimmed[idx+1:]
	}
	return strings.ReplaceAll(trimmed, "-", "_")
}

// ToolDefinitions returns JSON schemas for memory tools. CLI calls require turn_id.
func ToolDefinitions(cli bool) []ToolDefinition {
	buildSchema := func(required []string, props map[string]any) map[string]any {
		req := append([]string(nil), required...)
		if cli {
			req = append(req, "turn_id")
			props["turn_id"] = map[string]string{
				"type":        "string",
				"description": "The current turn_id from your turn instructions.",
			}
		}
		return map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             req,
			"additionalProperties": false,
		}
	}

	return []ToolDefinition{
		{
			Name:        ToolSave,
			Description: "Save a persistent fact, rule, or project convention into your personal bot knowledge base. Never store secrets, tokens, or passwords.",
			InputSchema: buildSchema([]string{"title", "content"}, map[string]any{
				"title": map[string]any{
					"type":        "string",
					"minLength":   1,
					"maxLength":   MaxTitleChars,
					"description": "Short descriptive title (max 160 chars).",
				},
				"content": map[string]any{
					"type":        "string",
					"minLength":   1,
					"maxLength":   MaxContentChars,
					"description": "Concise factual rule or context to remember (no secrets).",
				},
				"mode": map[string]any{
					"type":        "string",
					"enum":        []string{ModeAlways, ModeOnDemand},
					"description": "Use 'always' for core rules needed on every turn, or 'on_demand' (default) for reference notes retrieved via memory_search.",
				},
				"tags": map[string]any{
					"type":        "array",
					"maxItems":    MaxTags,
					"items":       map[string]any{"type": "string", "maxLength": MaxTagChars},
					"description": "Optional lowercase topic tags.",
				},
			}),
		},
		{
			Name:        ToolSearch,
			Description: "Search your personal bot knowledge base for saved facts, conventions, or notes.",
			InputSchema: buildSchema([]string{}, map[string]any{
				"query": map[string]any{
					"type":        "string",
					"maxLength":   200,
					"description": "Keywords or phrase to search in titles, content, and tags.",
				},
				"tag": map[string]any{
					"type":        "string",
					"maxLength":   MaxTagChars,
					"description": "Optional single tag filter.",
				},
				"mode": map[string]any{
					"type":        "string",
					"enum":        []string{ModeAlways, ModeOnDemand},
					"description": "Optional mode filter.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     20,
					"description": "Maximum entries to return (default 8).",
				},
			}),
		},
		{
			Name:        ToolUpdate,
			Description: "Update an existing entry in your personal bot knowledge base by memory_id.",
			InputSchema: buildSchema([]string{"memory_id"}, map[string]any{
				"memory_id": map[string]any{"type": "string", "description": "UUID of the memory entry to update."},
				"title":     map[string]any{"type": "string", "minLength": 1, "maxLength": MaxTitleChars},
				"content":   map[string]any{"type": "string", "minLength": 1, "maxLength": MaxContentChars},
				"mode":      map[string]any{"type": "string", "enum": []string{ModeAlways, ModeOnDemand}},
				"tags":      map[string]any{"type": "array", "maxItems": MaxTags, "items": map[string]any{"type": "string", "maxLength": MaxTagChars}},
			}),
		},
		{
			Name:        ToolDelete,
			Description: "Delete an obsolete or incorrect entry from your personal bot knowledge base by memory_id.",
			InputSchema: buildSchema([]string{"memory_id"}, map[string]any{
				"memory_id": map[string]any{"type": "string", "description": "UUID of the memory entry to delete."},
			}),
		},
	}
}

// ToolResult sanitizes internal DB/transport errors so models only see safe messages.
func ToolResult(result string, err error) string {
	if err == nil {
		return result
	}
	for _, safe := range []error{
		ErrValidation,
		ErrSecretDetected,
		ErrAlwaysBudgetExceeded,
		ErrLimitReached,
		ErrAutoSaveDisabled,
		ErrNotFound,
		ErrToolUnavailable,
		ErrToolArguments,
	} {
		if errors.Is(err, safe) {
			return err.Error()
		}
	}
	return "memory tool temporarily unavailable"
}

// Call executes one memory tool within the verified running turn scope.
func (s Service) Call(ctx context.Context, scope ToolScope, rawName string, rawArgs json.RawMessage) (string, error) {
	if s.Pool == nil || scope.WorkspaceID == uuid.Nil || scope.BotID == uuid.Nil || scope.TurnID == uuid.Nil {
		return "", ErrToolUnavailable
	}
	name := NormalizeToolName(rawName)
	if !IsMemoryTool(name) {
		return "", ErrToolArguments
	}

	var args struct {
		TurnID   *uuid.UUID `json:"turn_id"`
		MemoryID *uuid.UUID `json:"memory_id"`
		Title    *string    `json:"title"`
		Content  *string    `json:"content"`
		Mode     *string    `json:"mode"`
		Tags     *[]string  `json:"tags"`
		Query    *string    `json:"query"`
		Tag      *string    `json:"tag"`
		Limit    *int32     `json:"limit"`
	}
	if len(rawArgs) > 16384 {
		return "", ErrToolArguments
	}
	if len(bytes.TrimSpace(rawArgs)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(rawArgs))
		dec.DisallowUnknownFields()
		if dec.Decode(&args) != nil || dec.Decode(new(any)) != io.EOF {
			return "", ErrToolArguments
		}
	}
	if args.TurnID != nil && *args.TurnID != scope.TurnID {
		return "", ErrToolArguments
	}

	q := db.New(s.Pool)
	wsPG, botPG, turnPG := pgID(scope.WorkspaceID), pgID(scope.BotID), pgID(scope.TurnID)
	turn, err := q.GetBotTurn(ctx, db.GetBotTurnParams{WorkspaceID: wsPG, ID: turnPG})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && turn.BotID != botPG) {
		return "", ErrToolUnavailable
	}
	if err != nil {
		return "", err
	}
	if turn.Status != "running" || turn.StopRequested {
		return "", ErrToolUnavailable
	}
	runtime, err := q.GetBotRuntime(ctx, db.GetBotRuntimeParams{WorkspaceID: wsPG, ID: botPG})
	if err != nil {
		return "", err
	}
	if runtime.Paused || runtime.Controlled {
		return "", ErrToolUnavailable
	}

	switch name {
	case ToolSave:
		if args.Title == nil || args.Content == nil {
			return "", ErrToolArguments
		}
		mode := ModeOnDemand
		if args.Mode != nil {
			mode = *args.Mode
		}
		var tags []string
		if args.Tags != nil {
			tags = *args.Tags
		}
		sourceLabel := conversationSourceLabel(ctx, q, wsPG, turn.ConversationID, turn.Position)
		entry, err := s.createEntry(
			ctx,
			scope.WorkspaceID,
			scope.BotID,
			SourceChat,
			sourceLabel,
			uuid.UUID(turn.ConversationID.Bytes),
			scope.TurnID,
			uuid.Nil,
			WriteInput{Title: *args.Title, Content: *args.Content, Mode: mode, Tags: tags},
		)
		if err != nil {
			return "", err
		}
		out, err := json.Marshal(map[string]any{
			"saved":          true,
			"memory_id":      uuid.UUID(entry.ID.Bytes).String(),
			"title":          entry.Title,
			"mode":           entry.Mode,
			"tags":           entry.Tags,
			"token_estimate": entry.TokenEstimate,
		})
		if err != nil {
			return "", err
		}
		return string(out), nil

	case ToolSearch:
		query := ""
		if args.Query != nil {
			query = strings.TrimSpace(*args.Query)
			if len(query) > 200 {
				return "", ErrToolArguments
			}
		}
		modeFilter := ""
		if args.Mode != nil && strings.TrimSpace(*args.Mode) != "" {
			validMode, modeErr := ValidateMode(*args.Mode)
			if modeErr != nil {
				return "", modeErr
			}
			modeFilter = validMode
		}
		tagFilter := ""
		if args.Tag != nil && strings.TrimSpace(*args.Tag) != "" {
			normalized, tagErr := NormalizeTags([]string{*args.Tag})
			if tagErr != nil || len(normalized) != 1 {
				return "", ErrToolArguments
			}
			tagFilter = normalized[0]
		}
		limit := int32(8)
		if args.Limit != nil {
			if *args.Limit < 1 || *args.Limit > 20 {
				return "", ErrToolArguments
			}
			limit = *args.Limit
		}
		rows, err := q.SearchBotMemories(ctx, db.SearchBotMemoriesParams{
			WorkspaceID: wsPG,
			BotID:       botPG,
			ModeFilter:  modeFilter,
			TagFilter:   tagFilter,
			SearchQuery: query,
			LikeQuery:   EscapeLike(query),
			ResultLimit: limit,
		})
		if err != nil {
			return "", err
		}
		entries := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			entries = append(entries, map[string]any{
				"memory_id":    uuid.UUID(r.ID.Bytes).String(),
				"title":        r.Title,
				"content":      r.Content,
				"mode":         r.Mode,
				"tags":         r.Tags,
				"source_label": r.SourceLabel,
				"updated_at":   r.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05Z"),
			})
		}
		out, err := json.Marshal(map[string]any{
			"count":   len(entries),
			"entries": entries,
		})
		if err != nil {
			return "", err
		}
		return string(out), nil

	case ToolUpdate:
		if args.MemoryID == nil || *args.MemoryID == uuid.Nil {
			return "", ErrToolArguments
		}
		entry, err := s.Update(ctx, scope.WorkspaceID, scope.BotID, *args.MemoryID, uuid.Nil, true, UpdateInput{
			Title:   args.Title,
			Content: args.Content,
			Mode:    args.Mode,
			Tags:    args.Tags,
		})
		if err != nil {
			return "", err
		}
		out, err := json.Marshal(map[string]any{
			"updated":        true,
			"memory_id":      uuid.UUID(entry.ID.Bytes).String(),
			"title":          entry.Title,
			"mode":           entry.Mode,
			"tags":           entry.Tags,
			"token_estimate": entry.TokenEstimate,
		})
		if err != nil {
			return "", err
		}
		return string(out), nil

	case ToolDelete:
		if args.MemoryID == nil || *args.MemoryID == uuid.Nil {
			return "", ErrToolArguments
		}
		if err := s.Delete(ctx, scope.WorkspaceID, scope.BotID, *args.MemoryID, uuid.Nil, true); err != nil {
			return "", err
		}
		return `{"deleted":true}`, nil

	default:
		return "", ErrToolArguments
	}
}

func conversationSourceLabel(ctx context.Context, q *db.Queries, wsPG, convPG pgtype.UUID, turnPos int64) string {
	label, err := q.GetConversationLabel(ctx, db.GetConversationLabelParams{WorkspaceID: wsPG, ID: convPG})
	if err == nil {
		if label.Type == "group" && label.Name.Valid && strings.TrimSpace(label.Name.String) != "" {
			return fmt.Sprintf("#%s · turn #%d", strings.TrimSpace(label.Name.String), turnPos)
		}
		if label.Type == "dm" {
			return fmt.Sprintf("DM · turn #%d", turnPos)
		}
	}
	return fmt.Sprintf("Chat · turn #%d", turnPos)
}
