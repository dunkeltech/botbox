// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// agyMaxDeniedNames bounds the soft-denial notice that is stored in the chat.
const agyMaxDeniedNames = 10

// agyStream decodes `agy --output-format stream-json` from agy 1.5.9 (ADR-0040).
// It keeps state for one turn because agy can exit with code 0 after a failed
// turn, for example when retries outlast --print-timeout. Only the result record
// tells whether the turn succeeded.
type agyStream struct {
	result    bool
	failed    bool
	announced map[int64]bool
}

type agyStepUpdate struct {
	StepIndex *int64 `json:"step_index"`
	State     string `json:"state"`
	StepType  string `json:"step_type"`
	TextDelta string `json:"text_delta"`
	ToolName  string `json:"tool_name"`
	ToolInfo  *struct {
		Name       string          `json:"name"`
		Parameters json.RawMessage `json:"parameters"`
		Output     string          `json:"output"`
		Error      *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"tool_info"`
}

type agyResult struct {
	Status string `json:"status"`
	Usage  *struct {
		InputTokens     int64 `json:"input_tokens"`
		OutputTokens    int64 `json:"output_tokens"`
		CacheReadTokens int64 `json:"cache_read_tokens"`
	} `json:"usage"`
	DeniedActions []struct {
		Action      string `json:"action"`
		DisplayName string `json:"display_name"`
	} `json:"denied_actions"`
}

// ParseAgyStream decodes recorded agy stdout. It returns a failure when the
// recorded result reports an error, like the live stream does.
func ParseAgyStream(ctx context.Context, stdout io.Reader) ([]Event, error) {
	var agy agyStream
	events, err := scanNDJSON(ctx, "agy", stdout, agy.parseLine)
	if err != nil {
		return nil, err
	}
	return events, agy.finish()
}

func (a *agyStream) parseLine(line []byte) ([]Event, error) {
	var env struct {
		Event          string          `json:"event"`
		ConversationID string          `json:"conversation_id"`
		StepUpdate     json.RawMessage `json:"step_update"`
		Result         json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, err
	}
	switch env.Event {
	case "":
		return nil, errors.New("agy event is missing")
	case "init":
		return []Event{{Kind: EventKindStatus, Status: "thinking", SessionID: env.ConversationID}}, nil
	case "step_update":
		var step agyStepUpdate
		if err := decodeAgyPayload(env.StepUpdate, &step); err != nil {
			return nil, fmt.Errorf("agy step_update: %w", err)
		}
		return a.step(step)
	case "result":
		var result agyResult
		if err := decodeAgyPayload(env.Result, &result); err != nil {
			return nil, fmt.Errorf("agy result: %w", err)
		}
		return a.finishTurn(result), nil
	default:
		// Later agy versions may add events. They must not abort a turn.
		return nil, nil
	}
}

func decodeAgyPayload(raw json.RawMessage, target any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("payload is not an object")
	}
	return json.Unmarshal(trimmed, target)
}

func (a *agyStream) step(step agyStepUpdate) ([]Event, error) {
	switch step.StepType {
	case "agent_response":
		// agy sends partial text on ACTIVE updates and only the rest on DONE.
		if step.TextDelta != "" {
			return []Event{{Kind: EventKindTextDelta, Text: step.TextDelta}}, nil
		}
	case "tool":
		if step.StepIndex == nil || step.ToolInfo == nil {
			return nil, errors.New("agy tool step is incomplete")
		}
		// agy has no tool call ID. The step index is unique in a conversation.
		id := fmt.Sprintf("agy-step-%d", *step.StepIndex)
		var out []Event
		if !a.announced[*step.StepIndex] {
			if a.announced == nil {
				a.announced = map[int64]bool{}
			}
			a.announced[*step.StepIndex] = true
			name, input := agyToolCall(step.ToolName, step.ToolInfo.Name, step.ToolInfo.Parameters)
			out = append(out, Event{Kind: EventKindToolCall, ToolID: id, ToolName: name, ToolInput: input, Status: "action"})
		}
		switch step.State {
		case "DONE":
			out = append(out, Event{Kind: EventKindToolResult, ToolID: id, Text: step.ToolInfo.Output})
		case "ERROR":
			text := ""
			if step.ToolInfo.Error != nil {
				text = step.ToolInfo.Error.Message
			}
			out = append(out, Event{Kind: EventKindToolResult, ToolID: id, Text: text, IsError: true})
		}
		return out, nil
	}
	return nil, nil
}

// agyToolCall names MCP calls as server.tool, so team tools and the secret
// request tool are found the same way as for the other CLIs. agy calls every
// MCP tool through its own call_mcp_tool function.
func agyToolCall(stepName, infoName string, params json.RawMessage) (string, json.RawMessage) {
	name := stepName
	if name == "" {
		name = infoName
	}
	if name != "call_mcp_tool" {
		return name, params
	}
	var call struct {
		ServerName string          `json:"ServerName"`
		ToolName   string          `json:"ToolName"`
		Arguments  json.RawMessage `json:"Arguments"`
	}
	if json.Unmarshal(params, &call) != nil || strings.TrimSpace(call.ServerName) == "" || strings.TrimSpace(call.ToolName) == "" {
		return name, params
	}
	return call.ServerName + "." + call.ToolName, call.Arguments
}

func (a *agyStream) finishTurn(result agyResult) []Event {
	a.result = true
	a.failed = result.Status != "SUCCESS"
	var out []Event
	if result.Usage != nil {
		// output_tokens already includes thinking tokens, and input_tokens
		// excludes cached tokens, so the fields map directly.
		out = append(out, Event{
			Kind:         EventKindUsage,
			InputTokens:  result.Usage.InputTokens,
			OutputTokens: result.Usage.OutputTokens,
			CacheTokens:  result.Usage.CacheReadTokens,
		})
	}
	if notice := agyDeniedNotice(result); notice != "" {
		out = append(out, Event{Kind: EventKindError, ErrorCode: "tool_soft_denied", ErrorMessage: notice})
	}
	if !a.failed {
		out = append(out, Event{Kind: EventKindStatus, Status: "idle"})
	}
	return out
}

// agyDeniedNotice reports tools that headless agy denied without a prompt.
// The turn still ends with exit code 0, so the chat must show the denial.
func agyDeniedNotice(result agyResult) string {
	var names []string
	seen := map[string]bool{}
	for _, denied := range result.DeniedActions {
		name := strings.TrimSpace(denied.DisplayName)
		if name == "" {
			name = strings.TrimSpace(denied.Action)
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if len(names) == agyMaxDeniedNames {
			names = append(names, "…")
			break
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return ""
	}
	return "agy denied tools without asking: " + strings.Join(names, ", ")
}

func (a *agyStream) finish() error {
	if !a.result {
		return WithFailureCode(FailureStreamFormat, errors.New("stream ended before agy result"))
	}
	if a.failed {
		// The result error can quote the upstream response, so it stays out of the cause.
		return WithFailureCode(FailureProvider, errors.New("agy reported a failed turn"))
	}
	return nil
}
