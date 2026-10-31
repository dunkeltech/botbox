package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/dunkeltech/botbox/backend/internal/desk"
)

// OpenAIComputerClient translates the chat-completions tool loop into the
// Responses API while a computer-use tool is present. Requests without that
// tool are forwarded unchanged, so ordinary chat turns keep their transport.
func OpenAIComputerClient(client *http.Client) *http.Client {
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	copyClient := *client
	copyClient.Transport = openaiComputerTransport{transport}
	return &copyClient
}

type openaiComputerTransport struct{ base http.RoundTripper }

type openAIChatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
	ToolCalls  []toolCall      `json:"tool_calls"`
}

func (t openaiComputerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(io.LimitReader(req.Body, 12<<20))
	if err != nil {
		return nil, errors.New("invalid model request")
	}
	_ = req.Body.Close()
	var input struct {
		Model    string              `json:"model"`
		Messages []openAIChatMessage `json:"messages"`
		Tools    []openAITool        `json:"tools"`
	}
	if json.Unmarshal(body, &input) != nil {
		return nil, errors.New("invalid model request")
	}
	if !requestHasComputer(input.Tools) {
		outgoing := req.Clone(req.Context())
		outgoing.Body = io.NopCloser(bytes.NewReader(body))
		outgoing.ContentLength = int64(len(body))
		return t.base.RoundTrip(outgoing)
	}
	payload, err := responsesPayload(input.Model, input.Messages, input.Tools)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	outgoing := req.Clone(req.Context())
	urlCopy := *req.URL
	urlCopy.Path = strings.TrimSuffix(urlCopy.Path, "/chat/completions") + "/responses"
	outgoing.URL = &urlCopy
	outgoing.Body = io.NopCloser(bytes.NewReader(data))
	outgoing.ContentLength = int64(len(data))
	response, err := t.base.RoundTrip(outgoing)
	if err != nil || response.StatusCode != http.StatusOK {
		return response, err
	}
	upstream := response.Body
	reader, writer := io.Pipe()
	response.Body = reader
	go func() {
		err := translateOpenAIComputer(upstream, writer)
		_ = upstream.Close()
		_ = writer.CloseWithError(err)
	}()
	return response, nil
}

type openAITool struct {
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

func requestHasComputer(tools []openAITool) bool {
	for _, tool := range tools {
		var marker struct {
			Computer string `json:"x-botbox-computer"`
		}
		if json.Unmarshal(tool.Function.Parameters, &marker) == nil && marker.Computer == "openai" && tool.Function.Name == "computer" {
			return true
		}
	}
	return false
}

func responsesPayload(model string, messages []openAIChatMessage, tools []openAITool) (map[string]any, error) {
	var instructions strings.Builder
	input := []any{}
	calls := map[string]string{}
	for _, message := range messages {
		text := rawContentText(message.Content)
		switch message.Role {
		case "system":
			instructions.WriteString(text)
			instructions.WriteByte('\n')
		case "user":
			input = append(input, map[string]any{"role": "user", "content": []any{map[string]string{"type": "input_text", "text": text}}})
		case "assistant":
			if text != "" {
				input = append(input, map[string]any{"role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": text}}})
			}
			for _, call := range message.ToolCalls {
				calls[call.ID] = call.Function.Name
				if call.Function.Name == "computer" {
					action := json.RawMessage(call.Function.Arguments)
					if !json.Valid(action) {
						action = []byte(`{}`)
					}
					input = append(input, map[string]any{"type": "computer_call", "call_id": call.ID, "action": action})
					continue
				}
				input = append(input, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
			}
		case "tool":
			if calls[message.ToolCallID] == "computer" {
				input = append(input, computerOutput(message.ToolCallID, text))
				continue
			}
			input = append(input, map[string]any{"type": "function_call_output", "call_id": message.ToolCallID, "output": text})
		}
	}
	outTools := []any{}
	for _, tool := range tools {
		var marker struct {
			Computer string `json:"x-botbox-computer"`
		}
		if json.Unmarshal(tool.Function.Parameters, &marker) == nil && marker.Computer == "openai" && tool.Function.Name == "computer" {
			outTools = append(outTools, map[string]any{
				"type":           "computer_use_preview",
				"display_width":  desk.Width,
				"display_height": desk.Height,
				"environment":    "linux",
			})
			continue
		}
		toolDef := map[string]any{"type": "function", "name": tool.Function.Name, "parameters": tool.Function.Parameters}
		if tool.Function.Description != "" {
			toolDef["description"] = tool.Function.Description
		}
		outTools = append(outTools, toolDef)
	}
	payload := map[string]any{"model": model, "input": input, "stream": true}
	if instructions.Len() > 0 {
		payload["instructions"] = strings.TrimSpace(instructions.String())
	}
	if len(outTools) > 0 {
		payload["tools"] = outTools
	}
	return payload, nil
}

func computerOutput(id, text string) map[string]any {
	if image, ok := decodeImageResult(text); ok {
		return map[string]any{"type": "computer_call_output", "call_id": id, "output": map[string]string{
			"type":      "input_image",
			"image_url": "data:" + image.Mime + ";base64," + image.BotboxImage,
		}}
	}
	return map[string]any{"type": "computer_call_output", "call_id": id, "output": text}
}

func rawContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return ""
}

func translateOpenAIComputer(reader io.Reader, writer io.Writer) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	index := 0
	send := func(data any) error {
		raw, err := json.Marshal(data)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(writer, "data: %s\n\n", raw)
		return err
	}
	delta := func(value any) error {
		return send(map[string]any{"choices": []any{map[string]any{"delta": value}}})
	}
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
			Item  struct {
				Type      string            `json:"type"`
				CallID    string            `json:"call_id"`
				Name      string            `json:"name"`
				Arguments string            `json:"arguments"`
				Action    json.RawMessage   `json:"action"`
				Actions   []json.RawMessage `json:"actions"`
			} `json:"item"`
			Response struct {
				Usage struct {
					Input  int64 `json:"input_tokens"`
					Output int64 `json:"output_tokens"`
				} `json:"usage"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(data), &event) != nil {
			return errors.New("invalid OpenAI stream")
		}
		switch event.Type {
		case "error":
			return errors.New("openai stream failed")
		case "response.output_text.delta":
			if err := delta(map[string]string{"content": event.Delta}); err != nil {
				return err
			}
		case "response.output_item.added":
			switch event.Item.Type {
			case "function_call":
				if err := delta(map[string]any{"tool_calls": []any{map[string]any{"index": index, "id": event.Item.CallID, "function": map[string]string{"name": event.Item.Name, "arguments": event.Item.Arguments}}}}); err != nil {
					return err
				}
				index++
			case "computer_call":
				actions := event.Item.Actions
				if len(actions) == 0 && len(event.Item.Action) > 0 {
					actions = []json.RawMessage{event.Item.Action}
				}
				if len(actions) == 0 {
					actions = []json.RawMessage{[]byte(`{}`)}
				}
				for _, action := range actions {
					if err := delta(map[string]any{"tool_calls": []any{map[string]any{"index": index, "id": event.Item.CallID, "function": map[string]string{"name": "computer", "arguments": string(action)}}}}); err != nil {
						return err
					}
					index++
				}
			}
		case "response.function_call_arguments.delta":
			if index == 0 {
				return errors.New("unknown tool block")
			}
			if err := delta(map[string]any{"tool_calls": []any{map[string]any{"index": index - 1, "function": map[string]string{"arguments": event.Delta}}}}); err != nil {
				return err
			}
		case "response.completed":
			if err := send(map[string]any{"usage": map[string]any{"prompt_tokens": event.Response.Usage.Input, "completion_tokens": event.Response.Usage.Output}}); err != nil {
				return err
			}
			_, err := io.WriteString(writer, "data: [DONE]\n\n")
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return errors.New("incomplete OpenAI stream")
}
