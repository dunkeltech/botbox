// Package api runs bounded model tool loops through the LLM egress proxy.
package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/dunkeltech/botbox/backend/internal/budgets"
	"github.com/dunkeltech/botbox/backend/internal/engine"
)

// Message carries conversation-local model context.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Tools executes validated filesystem and process tools through the sandbox agent.
type Tools interface {
	Call(context.Context, string, json.RawMessage) (string, error)
}

// Provider streams OpenAI-compatible chat responses through a configured proxy.
// Client and URL must refer to a trusted proxy, never a user-provided endpoint.
type Provider struct {
	Client         *http.Client
	URL            string
	Model          string
	BotID          string
	BeforeRequest  func(context.Context) error
	Tools          Tools
	MaxSteps       int
	MaxScreenshots int
	MaxTurnTokens  int64
	BrokerEnabled  bool
	// Computer selects anthropic, openai, or generic computer-use tools.
	Computer string
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Run streams a bounded tool loop; no automatic retries repeat tool side effects.
func (p *Provider) Run(ctx context.Context, messages []Message, emit func(engine.Event) error) error {
	if p.Client == nil || p.URL == "" || p.Model == "" || emit == nil {
		return errors.New("provider configuration is incomplete")
	}
	history := make([]any, 0, len(messages))
	for _, m := range messages {
		history = append(history, m)
	}
	steps := p.MaxSteps
	if steps <= 0 {
		steps = 10
	}
	if steps > 100 {
		return errors.New("step limit exceeds maximum")
	}
	maxShots := p.MaxScreenshots
	if p.Computer != "" && maxShots <= 0 {
		maxShots = 12
	}
	var shots int
	var turnTokens int64
	for range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if p.BeforeRequest != nil {
			if err := p.BeforeRequest(ctx); err != nil {
				return err
			}
		}
		tools := []any{}
		if p.Tools != nil {
			for name, properties := range map[string]map[string]any{
				"exec":       {"command": map[string]string{"type": "string"}},
				"read_file":  {"path": map[string]string{"type": "string"}},
				"write_file": {"path": map[string]string{"type": "string"}, "content": map[string]string{"type": "string"}},
				"list_files": {"path": map[string]string{"type": "string"}},
			} {
				required := []string{"path"}
				if name == "exec" {
					required = []string{"command"}
				}
				if name == "write_file" {
					required = append(required, "content")
				}
				tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": name, "parameters": map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}})
			}
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": "request_secret",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"kind":                 map[string]any{"type": "string", "enum": []string{"cli_egress", "browser_login"}},
							"adapter_id":           map[string]any{"type": "string"},
							"reason":               map[string]any{"type": "string"},
							"target_origin":        map[string]any{"type": "string"},
							"env_placeholder_name": map[string]any{"type": "string"},
							"username_hint":        map[string]any{"type": "string"},
						},
						"required":             []string{"kind", "adapter_id", "reason"},
						"additionalProperties": false,
					},
				},
			})
			tools = append(tools, computerTools(p.Computer)...)
			if p.BrokerEnabled {
				tools = append(tools,
					map[string]any{
						"type": "function",
						"function": map[string]any{
							"name": "broker_open",
							"parameters": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"broker_session_id": map[string]any{"type": "string"},
									"url":               map[string]any{"type": "string"},
								},
								"required":             []string{"broker_session_id", "url"},
								"additionalProperties": false,
							},
						},
					},
					map[string]any{
						"type": "function",
						"function": map[string]any{
							"name": "broker_read",
							"parameters": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"broker_session_id": map[string]any{"type": "string"},
									"selector":          map[string]any{"type": "string"},
								},
								"required":             []string{"broker_session_id", "selector"},
								"additionalProperties": false,
							},
						},
					},
					map[string]any{
						"type": "function",
						"function": map[string]any{
							"name": "broker_click",
							"parameters": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"broker_session_id": map[string]any{"type": "string"},
									"selector":          map[string]any{"type": "string"},
								},
								"required":             []string{"broker_session_id", "selector"},
								"additionalProperties": false,
							},
						},
					},
					map[string]any{
						"type": "function",
						"function": map[string]any{
							"name": "broker_screenshot",
							"parameters": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"broker_session_id": map[string]any{"type": "string"},
								},
								"required":             []string{"broker_session_id"},
								"additionalProperties": false,
							},
						},
					},
				)
			}
		}
		payload := map[string]any{"model": p.Model, "messages": history, "stream": true, "stream_options": map[string]bool{"include_usage": true}, "max_tokens": 4096}
		if len(tools) > 0 {
			payload["tools"] = tools
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, bytes.NewReader(body))
		if err != nil {
			return errors.New("invalid proxy endpoint")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer botbox-placeholder")
		req.Header.Set("X-Botbox-Bot-ID", p.BotID)
		resp, err := p.Client.Do(req)
		if err != nil {
			return errors.New("model proxy unavailable")
		}
		if resp.StatusCode != 200 {
			_ = resp.Body.Close()
			return fmt.Errorf("model proxy returned HTTP %d", resp.StatusCode)
		}
		usageSeen := false
		text, calls, err := readCompletion(resp.Body, func(event engine.Event) error {
			if event.Kind == engine.EventKindUsage {
				usageSeen = true
				turnTokens += event.InputTokens + event.OutputTokens
			}
			return emit(event)
		})
		_ = resp.Body.Close()

		if !usageSeen {
			if recordErr := emit(engine.Event{Kind: engine.EventKindUsage, IsError: true, ErrorCode: "usage_unavailable"}); recordErr != nil {
				return recordErr
			}
			if err == nil {
				err = errors.New("provider omitted usage")
			}
		}
		if err != nil {
			return err
		}

		if len(calls) == 0 {
			if p.MaxTurnTokens > 0 && turnTokens > p.MaxTurnTokens {
				return engine.ErrTurnTokenBudget
			}
			return emit(engine.Event{Kind: engine.EventKindMessage, Text: text})
		}
		if p.Tools == nil {
			return errors.New("model requested unavailable tools")
		}
		history = append(history, map[string]any{"role": "assistant", "content": text, "tool_calls": calls})
		for _, call := range calls {
			if !json.Valid([]byte(call.Function.Arguments)) {
				return errors.New("invalid tool arguments")
			}
			if err = emit(engine.Event{Kind: engine.EventKindToolCall, ToolID: call.ID, ToolName: call.Function.Name, ToolInput: json.RawMessage(call.Function.Arguments)}); err != nil {
				return err
			}
			if maxShots > 0 && screenshotCall(call.Function.Name, call.Function.Arguments) {
				shots++
				if shots > maxShots {
					return engine.ErrScreenshotLimitExceeded
				}
			}
			output, callErr := p.Tools.Call(ctx, call.Function.Name, json.RawMessage(call.Function.Arguments))
			if callErr != nil {
				output = "sandbox tool failed"
			}
			if len(output) > 65536 {
				output = output[:65536]
			}
			if err = emit(engine.Event{Kind: engine.EventKindToolResult, ToolID: call.ID, Text: output, IsError: callErr != nil}); err != nil {
				return err
			}
			history = append(history, toolResultMessage(p.Computer, call.ID, output))
		}
		if p.MaxTurnTokens > 0 && turnTokens > p.MaxTurnTokens {
			return engine.ErrTurnTokenBudget
		}
	}
	return errors.New("model step limit exceeded")
}

func toolResultMessage(profile, id, output string) any {
	if profile == ComputerGeneric {
		if image, ok := decodeImageResult(output); ok {
			return map[string]any{"role": "tool", "tool_call_id": id, "content": []any{
				map[string]string{"type": "text", "text": image.Text},
				map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:" + image.Mime + ";base64," + image.BotboxImage}},
			}}
		}
	}
	return map[string]string{"role": "tool", "tool_call_id": id, "content": output}
}

func readCompletion(r io.Reader, emit func(engine.Event) error) (string, []toolCall, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var text strings.Builder
	calls := map[int]*toolCall{}
	complete := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			complete = true
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				Input   int64        `json:"prompt_tokens"`
				Output  int64        `json:"completion_tokens"`
				Cost    *json.Number `json:"cost"`
				Details struct {
					Cached int64 `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			return "", nil, errors.New("invalid provider stream")
		}
		if len(chunk.Error) > 0 {
			return "", nil, errors.New("provider stream failed")
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				if text.Len()+len(choice.Delta.Content) > 1<<20 {
					return "", nil, errors.New("model output exceeds limit")
				}
				text.WriteString(choice.Delta.Content)
				if err := emit(engine.Event{Kind: engine.EventKindTextDelta, Text: choice.Delta.Content}); err != nil {
					return "", nil, err
				}
			}
			for _, delta := range choice.Delta.ToolCalls {
				if delta.Index < 0 || delta.Index >= 32 {
					return "", nil, errors.New("too many tool calls")
				}
				c := calls[delta.Index]
				if c == nil {
					c = &toolCall{Type: "function"}
					calls[delta.Index] = c
				}
				if delta.ID != "" {
					c.ID = delta.ID
				}
				c.Function.Name += delta.Function.Name
				c.Function.Arguments += delta.Function.Arguments
				if len(c.Function.Arguments) > 65536 || len(c.Function.Name) > 100 || len(c.ID) > 255 {
					return "", nil, errors.New("tool call exceeds limit")
				}
			}
		}
		if chunk.Usage != nil {
			u := chunk.Usage
			if u.Input < 0 || u.Output < 0 || u.Details.Cached < 0 || u.Details.Cached > u.Input {
				return "", nil, errors.New("invalid provider usage")
			}
			var reported *int64
			if u.Cost != nil {
				micros, err := budgets.ReportedMicros(string(*u.Cost))
				if err != nil {
					return "", nil, err
				}
				reported = &micros
			}
			if err := emit(engine.Event{Kind: engine.EventKindUsage, InputTokens: u.Input, OutputTokens: u.Output, CacheTokens: u.Details.Cached, CostMicros: reported}); err != nil {
				return "", nil, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", nil, errors.New("provider stream interrupted")
	}
	if !complete {
		return "", nil, errors.New("incomplete provider stream")
	}
	out := make([]toolCall, 0, len(calls))
	for i := 0; i < len(calls); i++ {
		c := calls[i]
		if c == nil || c.ID == "" || c.Function.Name == "" {
			return "", nil, errors.New("incomplete tool call")
		}
		out = append(out, *c)
	}
	return text.String(), out, nil
}
