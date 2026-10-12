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
)

// AnthropicClient adapts the common tool loop to native Anthropic Messages SSE.
// The wrapped transport still enforces the proxy's credential and budget checks.
func AnthropicClient(client *http.Client) *http.Client {
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	copyClient := *client
	copyClient.Transport = anthropicTransport{transport}
	return &copyClient
}

type anthropicTransport struct{ base http.RoundTripper }

func (t anthropicTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var input struct {
		Model    string `json:"model"`
		Messages []struct {
			Role       string     `json:"role"`
			Content    string     `json:"content"`
			ToolCallID string     `json:"tool_call_id"`
			ToolCalls  []toolCall `json:"tool_calls"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name       string          `json:"name"`
				Parameters json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(io.LimitReader(req.Body, 2<<20)).Decode(&input); err != nil {
		return nil, errors.New("invalid model request")
	}
	_ = req.Body.Close()
	messages := []any{}
	system := ""
	for _, m := range input.Messages {
		if m.Role == "system" {
			system += m.Content + "\n"
			continue
		}
		blocks := []any{}
		role := m.Role
		if m.Role == "tool" {
			role = "user"
			blocks = append(blocks, map[string]any{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": m.Content})
		} else {
			if m.Content != "" {
				blocks = append(blocks, map[string]string{"type": "text", "text": m.Content})
			}
			for _, call := range m.ToolCalls {
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": json.RawMessage(call.Function.Arguments)})
			}
		}
		messages = append(messages, map[string]any{"role": role, "content": blocks})
	}
	tools := []any{}
	for _, tool := range input.Tools {
		tools = append(tools, map[string]any{"name": tool.Function.Name, "input_schema": tool.Function.Parameters})
	}
	payload := map[string]any{"model": input.Model, "system": system, "messages": messages, "stream": true, "max_tokens": 4096}
	if len(tools) > 0 {
		payload["tools"] = tools
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	outgoing := req.Clone(req.Context())
	urlCopy := *req.URL
	urlCopy.Path = strings.TrimSuffix(urlCopy.Path, "/chat/completions") + "/messages"
	outgoing.URL = &urlCopy
	outgoing.Body = io.NopCloser(bytes.NewReader(data))
	outgoing.ContentLength = int64(len(data))
	outgoing.Header.Set("Anthropic-Version", "2023-06-01")
	response, err := t.base.RoundTrip(outgoing)
	if err != nil || response.StatusCode != 200 {
		return response, err
	}
	upstream := response.Body
	reader, writer := io.Pipe()
	response.Body = reader
	go func() {
		err := translateAnthropic(upstream, writer)
		_ = upstream.Close()
		_ = writer.CloseWithError(err)
	}()
	return response, nil
}
func translateAnthropic(reader io.Reader, writer io.Writer) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var input, output, cache int64
	indices := map[int]int{}
	send := func(data any) error {
		raw, err := json.Marshal(data)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(writer, "data: %s\n\n", raw)
		return err
	}
	delta := func(value any) error { return send(map[string]any{"choices": []any{map[string]any{"delta": value}}}) }
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				Usage struct {
					Input int64 `json:"input_tokens"`
					Cache int64 `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Block struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
				JSON string `json:"partial_json"`
			} `json:"delta"`
			Usage struct {
				Output int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event) != nil {
			return errors.New("invalid Anthropic stream")
		}
		switch event.Type {
		case "error":
			return errors.New("anthropic stream failed")
		case "message_start":
			input, cache = event.Message.Usage.Input, event.Message.Usage.Cache
		case "content_block_start":
			if event.Block.Type == "tool_use" {
				index := len(indices)
				indices[event.Index] = index
				if err := delta(map[string]any{"tool_calls": []any{map[string]any{"index": index, "id": event.Block.ID, "function": map[string]string{"name": event.Block.Name, "arguments": ""}}}}); err != nil {
					return err
				}
			}
		case "content_block_delta":
			if event.Delta.Type == "text_delta" {
				if err := delta(map[string]string{"content": event.Delta.Text}); err != nil {
					return err
				}
			}
			if event.Delta.Type == "input_json_delta" {
				index, ok := indices[event.Index]
				if !ok {
					return errors.New("unknown tool block")
				}
				if err := delta(map[string]any{"tool_calls": []any{map[string]any{"index": index, "function": map[string]string{"arguments": event.Delta.JSON}}}}); err != nil {
					return err
				}
			}
		case "message_delta":
			output = event.Usage.Output
		case "message_stop":
			if err := send(map[string]any{"usage": map[string]any{"prompt_tokens": input + cache, "completion_tokens": output, "prompt_tokens_details": map[string]int64{"cached_tokens": cache}}}); err != nil {
				return err
			}
			_, err := io.WriteString(writer, "data: [DONE]\n\n")
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return errors.New("incomplete Anthropic stream")
}
