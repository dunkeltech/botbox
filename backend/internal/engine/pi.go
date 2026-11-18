// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

type piStream struct {
	settled bool
	failed  bool
}

func (p *piStream) parseLine(line []byte) ([]Event, error) {
	var record struct {
		Type       string          `json:"type"`
		ID         string          `json:"id"`
		ToolCallID string          `json:"toolCallId"`
		ToolName   string          `json:"toolName"`
		Args       json.RawMessage `json:"args"`
		IsError    bool            `json:"isError"`
		Aborted    bool            `json:"aborted"`
		Update     struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		} `json:"assistantMessageEvent"`
		Message struct {
			Role       string          `json:"role"`
			StopReason string          `json:"stopReason"`
			Content    json.RawMessage `json:"content"`
		} `json:"message"`
		Result struct {
			Content json.RawMessage `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(line, &record); err != nil || record.Type == "" {
		return nil, WithFailureCode(FailureStreamFormat, errors.New("invalid Pi event"))
	}
	switch record.Type {
	case "session":
		return []Event{{Kind: EventKindStatus, Status: "thinking", SessionID: record.ID}}, nil
	case "message_update":
		if record.Update.Type == "text_delta" && record.Update.Delta != "" {
			return []Event{{Kind: EventKindTextDelta, Text: record.Update.Delta}}, nil
		}
	case "message_end":
		if record.Message.Role == "assistant" {
			p.failed = record.Message.StopReason == "error" || record.Message.StopReason == "aborted"
			if !p.failed {
				return piTextEvent(Event{Kind: EventKindMessage}, record.Message.Content)
			}
		}
	case "tool_execution_start":
		return []Event{{Kind: EventKindToolCall, ToolID: record.ToolCallID, ToolName: record.ToolName, ToolInput: record.Args, Status: "action"}}, nil
	case "tool_execution_end":
		return piTextEvent(Event{Kind: EventKindToolResult, ToolID: record.ToolCallID, ToolName: record.ToolName, IsError: record.IsError}, record.Result.Content)
	case "agent_settled":
		if record.Aborted {
			return nil, context.Canceled
		}
		p.settled = true
	}
	return nil, nil
}

func (p *piStream) finish() error {
	if !p.settled {
		return WithFailureCode(FailureStreamFormat, errors.New("stream ended before Pi settled"))
	}
	if p.failed {
		return WithFailureCode(FailureProvider, errors.New("model request failed in Pi"))
	}
	return nil
}

func piTextEvent(event Event, raw json.RawMessage) ([]Event, error) {
	var content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &content); err != nil {
			return nil, WithFailureCode(FailureStreamFormat, errors.New("invalid Pi content"))
		}
	}
	var text strings.Builder
	for _, block := range content {
		if block.Type == "text" {
			if text.Len() > 0 && event.Kind == EventKindToolResult {
				text.WriteByte('\n')
			}
			text.WriteString(block.Text)
		}
	}
	event.Text = text.String()
	if event.Text == "" && event.Kind == EventKindMessage {
		return nil, nil
	}
	return []Event{event}, nil
}
