package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// maxStreamLineBytes caps individual NDJSON records so malformed output cannot exhaust memory.
const maxStreamLineBytes = 1 << 20

// Normalized bot engine stream event kinds emitted by all CLI adapters.
const (
	EventKindMessage        EventKind = "message"
	EventKindApprovalNeeded EventKind = "approval_needed"
	EventKindCard           EventKind = "card"
	EventKindStatus         EventKind = "status"
	EventKindTextDelta      EventKind = "text_delta"
	EventKindToolCall       EventKind = "tool_call"
	EventKindToolResult     EventKind = "tool_result"
	EventKindUsage          EventKind = "usage"
	EventKindError          EventKind = "error"
)

// ErrLineTooLong indicates a single NDJSON line exceeded maxStreamLineBytes.
var ErrLineTooLong = errors.New("stream line exceeds maximum allowed size")

// IsRequestSecretTool reports whether a normalized tool name targets the
// zero-context secret request tool across CLI MCP servers or API mode.
func IsRequestSecretTool(name string) bool {
	trimmed := strings.TrimSpace(name)
	return trimmed == "request_secret" ||
		strings.HasSuffix(trimmed, "__request_secret") ||
		strings.HasSuffix(trimmed, ".request_secret")
}

// EventKind identifies normalized bot engine stream events.
type EventKind string

// Event is the unified internal representation produced from claude, codex, and agy streams.
type Event struct {
	TurnID       string
	CostMicros   *int64
	SessionID    string
	Kind         EventKind
	Text         string
	Metadata     json.RawMessage
	ToolID       string
	ToolName     string
	ToolInput    json.RawMessage
	IsError      bool
	Status       string
	InputTokens  int64
	OutputTokens int64
	CacheTokens  int64
	ErrorCode    string
	ErrorMessage string
}

// claudeContentBlock is one entry of a Claude stream message.
// Content is a string for plain tool output and an array of text and image
// blocks for MCP results. A desktop screenshot uses the array form. Decoding
// it as a string aborts the turn, which the chat shows as a provider failure.
type claudeContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// claudeContentBlocks accepts the two shapes Claude Code writes for message
// content: a prompt string, or a list of blocks. A string is the echoed user
// prompt and produces no blocks.
func claudeContentBlocks(raw json.RawMessage) ([]claudeContentBlock, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	switch trimmed[0] {
	case '"':
		return nil, nil
	case '[':
		var rawBlocks []json.RawMessage
		if err := json.Unmarshal(trimmed, &rawBlocks); err != nil {
			return nil, err
		}
		blocks := make([]claudeContentBlock, 0, len(rawBlocks))
		for _, rawBlock := range rawBlocks {
			item := bytes.TrimSpace(rawBlock)
			if len(item) == 0 || item[0] != '{' {
				return nil, errors.New("claude content block is not an object")
			}
			var block claudeContentBlock
			if err := json.Unmarshal(item, &block); err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		}
		return blocks, nil
	default:
		return nil, errors.New("claude message content is not a string or block list")
	}
}

// claudeBlockText reads tool_result content without copying image bytes.
// Plain tools send a string. MCP tools send text and image blocks.
func claudeBlockText(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	switch trimmed[0] {
	case '"':
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return "", err
		}
		return text, nil
	case '[':
		blocks, err := claudeContentBlocks(trimmed)
		if err != nil {
			return "", err
		}
		var text strings.Builder
		for _, block := range blocks {
			if block.Type != "text" || block.Text == "" {
				continue
			}
			if text.Len() > 0 {
				text.WriteByte('\n')
			}
			text.WriteString(block.Text)
		}
		return text.String(), nil
	default:
		return "", errors.New("claude tool result content is not a string or block list")
	}
}

// ParseClaudeStream decodes NDJSON output from `claude -p --output-format stream-json`.
func ParseClaudeStream(ctx context.Context, r io.Reader) ([]Event, error) {
	return scanNDJSON(ctx, "claude", r, func(line []byte) ([]Event, error) {
		var env struct {
			Type        string `json:"type"`
			SessionID   string `json:"session_id"`
			IsError     bool   `json:"is_error"`
			StreamEvent struct {
				Type  string `json:"type"`
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			} `json:"event"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			Usage *struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
				CacheTokens  int64 `json:"cache_read_input_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			return nil, err
		}

		var out []Event
		switch env.Type {
		case "system":
			out = append(out, Event{Kind: EventKindStatus, Status: "thinking", SessionID: env.SessionID})
		case "stream_event":
			if env.StreamEvent.Delta.Type == "text_delta" && env.StreamEvent.Delta.Text != "" {
				out = append(out, Event{Kind: EventKindTextDelta, Text: env.StreamEvent.Delta.Text})
			}
		case "assistant":
			blocks, err := claudeContentBlocks(env.Message.Content)
			if err != nil {
				return nil, err
			}
			var text strings.Builder
			for _, item := range blocks {
				if item.Type == "text" {
					piece := item.Text
					if piece == "" {
						piece, err = claudeBlockText(item.Content)
						if err != nil {
							return nil, err
						}
					}
					text.WriteString(piece)
				}
				if item.Type == "tool_use" {
					out = append(out, Event{
						Kind:      EventKindToolCall,
						ToolID:    item.ID,
						ToolName:  item.Name,
						ToolInput: item.Input,
						Status:    "action",
					})
				}
			}
			if text.Len() > 0 {
				out = append(out, Event{Kind: EventKindMessage, Text: text.String()})
			}
		case "user":
			blocks, err := claudeContentBlocks(env.Message.Content)
			if err != nil {
				return nil, err
			}
			for _, item := range blocks {
				if item.Type != "tool_result" {
					continue
				}
				text, err := claudeBlockText(item.Content)
				if err != nil {
					return nil, err
				}
				out = append(out, Event{
					Kind:    EventKindToolResult,
					ToolID:  item.ToolUseID,
					Text:    text,
					IsError: item.IsError,
				})
			}
		case "result":
			if env.Usage != nil {
				out = append(out, Event{
					Kind:         EventKindUsage,
					InputTokens:  env.Usage.InputTokens,
					OutputTokens: env.Usage.OutputTokens,
					CacheTokens:  env.Usage.CacheTokens,
				})
			}
			if env.IsError {
				out = append(out, Event{Kind: EventKindError, ErrorCode: "cli_error", ErrorMessage: "claude reported error result"})
			}
			out = append(out, Event{Kind: EventKindStatus, Status: "idle"})
		}
		return out, nil
	})
}

// ParseCodexStream decodes NDJSON output from `codex exec --json`.
func ParseCodexStream(ctx context.Context, r io.Reader) ([]Event, error) {
	return scanNDJSON(ctx, "codex", r, func(line []byte) ([]Event, error) {
		var env struct {
			Type      string `json:"type"`
			SessionID string `json:"thread_id"`
			Item      *struct {
				ID               string `json:"id"`
				Type             string `json:"type"`
				Command          string `json:"command"`
				AggregatedOutput string `json:"aggregated_output"`
				ExitCode         int    `json:"exit_code"`
				Text             string `json:"text"`
			} `json:"item"`
			Usage *struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
				CacheTokens  int64 `json:"cached_input_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			return nil, err
		}

		var out []Event
		switch env.Type {
		case "thread.started", "turn.started":
			out = append(out, Event{Kind: EventKindStatus, Status: "thinking", SessionID: env.SessionID})
		case "item.started":
			if env.Item != nil && env.Item.Type == "command_execution" {
				inputJSON, _ := json.Marshal(map[string]string{"command": env.Item.Command})
				out = append(out, Event{
					Kind:      EventKindToolCall,
					ToolID:    env.Item.ID,
					ToolName:  "shell",
					ToolInput: inputJSON,
					Status:    "action",
				})
			}
		case "item.completed":
			if env.Item != nil {
				switch env.Item.Type {
				case "command_execution":
					out = append(out, Event{
						Kind:    EventKindToolResult,
						ToolID:  env.Item.ID,
						Text:    env.Item.AggregatedOutput,
						IsError: env.Item.ExitCode != 0,
					})
				case "agent_message":
					if env.Item.Text != "" {
						out = append(out, Event{Kind: EventKindTextDelta, Text: env.Item.Text})
					}
				}
			}
		case "turn.completed":
			if env.Usage != nil {
				out = append(out, Event{
					Kind:         EventKindUsage,
					InputTokens:  env.Usage.InputTokens,
					OutputTokens: env.Usage.OutputTokens,
					CacheTokens:  env.Usage.CacheTokens,
				})
			}
			out = append(out, Event{Kind: EventKindStatus, Status: "idle"})
		}
		return out, nil
	})
}

// ParseAgyStream decodes NDJSON stdout from `agy --output-format stream-json`
// and captures stderr soft-denial notices (since agy exits 0 when tools are soft-denied).
func ParseAgyStream(ctx context.Context, stdout, stderr io.Reader) ([]Event, error) {
	events, err := scanNDJSON(ctx, "agy", stdout, func(line []byte) ([]Event, error) {
		var env struct {
			Type      string `json:"type"`
			SessionID string `json:"conversation_id"`
			TextDelta *struct {
				Text string `json:"text"`
			} `json:"text_delta"`
			ToolInfo *struct {
				ID      string          `json:"id"`
				Name    string          `json:"name"`
				Status  string          `json:"status"`
				Input   json.RawMessage `json:"input"`
				Output  string          `json:"output"`
				IsError bool            `json:"is_error"`
			} `json:"tool_info"`
			SubagentInfo *struct {
				Role   string `json:"role"`
				Status string `json:"status"`
			} `json:"subagent_info"`
			Usage *struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
				CacheTokens  int64 `json:"cache_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			return nil, err
		}

		var out []Event
		switch env.Type {
		case "init":
			out = append(out, Event{Kind: EventKindStatus, Status: "thinking", SessionID: env.SessionID})
		case "step_update":
			if env.TextDelta != nil && env.TextDelta.Text != "" {
				out = append(out, Event{Kind: EventKindTextDelta, Text: env.TextDelta.Text})
			}
			if env.ToolInfo != nil {
				switch env.ToolInfo.Status {
				case "running", "pending":
					out = append(out, Event{
						Kind:      EventKindToolCall,
						ToolID:    env.ToolInfo.ID,
						ToolName:  env.ToolInfo.Name,
						ToolInput: env.ToolInfo.Input,
						Status:    "action",
					})
				case "completed", "failed":
					out = append(out, Event{
						Kind:    EventKindToolResult,
						ToolID:  env.ToolInfo.ID,
						Text:    env.ToolInfo.Output,
						IsError: env.ToolInfo.IsError || env.ToolInfo.Status == "failed",
					})
				}
			}
			if env.SubagentInfo != nil {
				out = append(out, Event{Kind: EventKindStatus, Status: "thinking"})
			}
		case "result":
			if env.Usage != nil {
				out = append(out, Event{
					Kind:         EventKindUsage,
					InputTokens:  env.Usage.InputTokens,
					OutputTokens: env.Usage.OutputTokens,
					CacheTokens:  env.Usage.CacheTokens,
				})
			}
			out = append(out, Event{Kind: EventKindStatus, Status: "idle"})
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}

	if stderr != nil {
		stderrEvents, err := parseAgyStderr(ctx, stderr)
		if err != nil {
			return nil, err
		}
		events = append(events, stderrEvents...)
	}
	return events, nil
}

func parseAgyStderr(ctx context.Context, r io.Reader) ([]Event, error) {
	reader := bufio.NewReaderSize(r, 64*1024)
	var out []Event
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("agy stderr canceled: %w", err)
		}
		line, err := readBoundedLine(reader)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("agy stderr: %w", err)
		}
		trimmed := strings.TrimSpace(string(line))
		if trimmed != "" {
			lower := strings.ToLower(trimmed)
			if strings.Contains(lower, "permission denied") || strings.Contains(lower, "denied") {
				out = append(out, Event{
					Kind:         EventKindError,
					ErrorCode:    "tool_soft_denied",
					ErrorMessage: trimmed,
				})
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	return out, nil
}

func scanNDJSON(
	ctx context.Context,
	source string,
	r io.Reader,
	parseLine func([]byte) ([]Event, error),
) ([]Event, error) {
	if r == nil {
		return nil, fmt.Errorf("%s stream reader is nil", source)
	}
	reader := bufio.NewReaderSize(r, 64*1024)
	var all []Event
	lineNum := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%s stream canceled: %w", source, err)
		}
		line, err := readBoundedLine(reader)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s stream line %d: %w", source, lineNum+1, err)
		}
		trimmed := strings.TrimSpace(string(line))
		if trimmed != "" {
			lineNum++
			parsed, parseErr := parseLine([]byte(trimmed))
			if parseErr != nil {
				return nil, fmt.Errorf("%s stream line %d: %w", source, lineNum, parseErr)
			}
			all = append(all, parsed...)
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	return all, nil
}

func readBoundedLine(reader *bufio.Reader) ([]byte, error) {
	var full []byte
	for {
		fragment, isPrefix, err := reader.ReadLine()
		if len(fragment) > 0 {
			if len(full)+len(fragment) > maxStreamLineBytes {
				return nil, ErrLineTooLong
			}
			full = append(full, fragment...)
		}
		if err != nil {
			return full, err
		}
		if !isPrefix {
			return full, nil
		}
	}
}
