// SPDX-License-Identifier: AGPL-3.0-only

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
// zero-context secret request tool across CLI MCP servers.
func IsRequestSecretTool(name string) bool {
	trimmed := strings.TrimSpace(name)
	return trimmed == "request_secret" ||
		strings.HasSuffix(trimmed, "__request_secret") ||
		strings.HasSuffix(trimmed, ".request_secret")
}

// EventKind identifies normalized bot engine stream events.
type EventKind string

// Event is the unified internal representation produced from managed CLI streams.
type Event struct {
	BotID        string
	RunID        string
	TurnID       string
	Sequence     uint64
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
	return scanNDJSON(ctx, "claude", r, decodeClaudeLine)
}

func decodeClaudeLine(line []byte) ([]Event, error) {
	var env struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, err
	}
	if env.Type == "" {
		return nil, errors.New("claude event type is missing")
	}

	// Auxiliary events may use message as text. Decode only the fields
	// required by each event type so new notices cannot interrupt a turn.
	var out []Event
	switch env.Type {
	case "system":
		var system struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(line, &system); err != nil {
			return nil, err
		}
		out = append(out, Event{Kind: EventKindStatus, Status: "thinking", SessionID: system.SessionID})
	case "stream_event":
		var stream struct {
			Event struct {
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			} `json:"event"`
		}
		if err := json.Unmarshal(line, &stream); err != nil {
			return nil, err
		}
		if stream.Event.Delta.Type == "text_delta" && stream.Event.Delta.Text != "" {
			out = append(out, Event{Kind: EventKindTextDelta, Text: stream.Event.Delta.Text})
		}
	case "assistant", "user":
		var message struct {
			Message *struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &message); err != nil {
			return nil, err
		}
		if message.Message == nil {
			return nil, errors.New("claude message is missing")
		}
		content := bytes.TrimSpace(message.Message.Content)
		if len(content) == 0 || bytes.Equal(content, []byte("null")) {
			return nil, errors.New("claude message content is missing")
		}
		if env.Type == "assistant" && content[0] != '[' {
			return nil, errors.New("claude assistant content is not a block list")
		}
		blocks, err := claudeContentBlocks(content)
		if err != nil {
			return nil, err
		}
		var text strings.Builder
		for _, item := range blocks {
			if env.Type == "user" {
				if item.Type == "tool_result" {
					result, err := claudeBlockText(item.Content)
					if err != nil {
						return nil, err
					}
					out = append(out, Event{Kind: EventKindToolResult, ToolID: item.ToolUseID, Text: result, IsError: item.IsError})
				}
				continue
			}
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
	case "result":
		var result struct {
			IsError bool `json:"is_error"`
			Usage   *struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
				CacheTokens  int64 `json:"cache_read_input_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(line, &result); err != nil {
			return nil, err
		}
		if result.Usage != nil {
			out = append(out, Event{
				Kind:         EventKindUsage,
				InputTokens:  result.Usage.InputTokens,
				OutputTokens: result.Usage.OutputTokens,
				CacheTokens:  result.Usage.CacheTokens,
			})
		}
		if result.IsError {
			out = append(out, Event{Kind: EventKindError, ErrorCode: "cli_error", ErrorMessage: "claude reported error result"})
		}
		out = append(out, Event{Kind: EventKindStatus, Status: "idle"})
	}
	return out, nil
}

// ParseCodexStream decodes NDJSON output from `codex exec --json`.
func ParseCodexStream(ctx context.Context, r io.Reader) ([]Event, error) {
	return scanNDJSON(ctx, "codex", r, decodeCodexLine)
}

func decodeCodexLine(line []byte) ([]Event, error) {
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
}

func scanNDJSON(ctx context.Context, source string, r io.Reader, decode func([]byte) ([]Event, error)) ([]Event, error) {
	if r == nil {
		return nil, fmt.Errorf("%s stream reader is nil", source)
	}
	var events []Event
	err := consumeStreamLines(ctx, source, r, decode, func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return events, nil
}

func consumeStreamLines(
	ctx context.Context,
	source string,
	r io.Reader,
	decode func([]byte) ([]Event, error),
	emit func(Event) error,
) error {
	reader := bufio.NewReaderSize(r, 64*1024)
	record := 0
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%s stream canceled: %w", source, err)
		}
		line, err := readBoundedLine(reader)
		if err != nil && !errors.Is(err, io.EOF) {
			if errors.Is(err, ErrLineTooLong) {
				err = WithFailureCode(FailureStreamFormat, err)
			}
			return fmt.Errorf("%s stream line %d: %w", source, record+1, err)
		}
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 && decode != nil {
			record++
			events, decodeErr := decode(trimmed)
			if decodeErr != nil {
				if !errors.Is(decodeErr, context.Canceled) {
					decodeErr = WithFailureCode(FailureStreamFormat, decodeErr)
				}
				return fmt.Errorf("%s stream line %d: %w", source, record, decodeErr)
			}
			for _, event := range events {
				if emitErr := emit(event); emitErr != nil {
					return emitErr
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
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
