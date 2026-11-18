// SPDX-License-Identifier: AGPL-3.0-only

package llmproxy

import (
	"context"
	"encoding/json"
)

type tokenBound struct{ input, output int64 }
type tokenBoundKey struct{}

// RequestTokenBound returns a conservative bound for text-only, output-limited requests.
// Opaque inputs and provider-managed tools use serialized admission instead.
func RequestTokenBound(ctx context.Context) (input, output int64, ok bool) {
	value, ok := ctx.Value(tokenBoundKey{}).(tokenBound)
	return value.input, value.output, ok
}

func boundedRequest(ctx context.Context, provider, path string, body []byte) context.Context {
	var value map[string]json.RawMessage
	if json.Unmarshal(body, &value) != nil {
		return ctx
	}
	// Unknown extensions can add remote context or extra charges (for example
	// hosted search). Only the text request shape has a token-cost bound.
	for field := range value {
		switch field {
		case "model", "messages", "system", "input", "instructions", "stream", "stream_options", "max_tokens", "max_completion_tokens", "max_output_tokens", "temperature", "top_p", "top_k", "frequency_penalty", "presence_penalty", "stop", "stop_sequences", "seed", "n", "tools", "tool_choice", "parallel_tool_calls", "response_format", "logprobs", "top_logprobs", "user", "store", "metadata", "thinking", "reasoning", "text", "truncation", "include", "safety_identifier":
		default:
			return ctx
		}
	}
	var fields []string
	switch path {
	case "/v1/chat/completions":
		fields = []string{"max_completion_tokens", "max_tokens"}
	case "/v1/responses":
		fields = []string{"max_output_tokens"}
	case "/v1/messages":
		fields = []string{"max_tokens"}
	default:
		return ctx
	}
	var output int64
	for _, field := range fields {
		if raw, exists := value[field]; exists {
			if json.Unmarshal(raw, &output) != nil || output < 1 || output > 1000000 {
				return ctx
			}
			break
		}
	}
	if output == 0 {
		return ctx
	}
	if raw, exists := value["n"]; exists {
		var n int64
		if json.Unmarshal(raw, &n) != nil || n != 1 {
			return ctx
		}
	}
	// Each text token consumes bytes. JSON size plus per-field framing slack
	// overestimates text and chat framing; it cannot bound image or remote inputs.
	var decoded any
	if json.Unmarshal(body, &decoded) != nil || !textOnly(decoded) {
		return ctx
	}
	return context.WithValue(ctx, tokenBoundKey{}, tokenBound{input: int64(len(body))*4 + 4096, output: output})
}

func textOnly(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			switch key {
			case "image", "image_url", "audio", "input_audio", "file", "file_id", "file_data", "video", "previous_response_id", "prompt_cache_key":
				return false
			}
			if key == "type" {
				if kind, ok := child.(string); ok {
					switch kind {
					case "function", "custom", "text", "input_text", "output_text", "message", "function_call", "function_call_output", "object", "array", "string", "number", "integer", "boolean", "null":
					default:
						return false
					}
				}
			}
			if !textOnly(child) {
				return false
			}
		}
	case []any:
		for _, child := range v {
			if !textOnly(child) {
				return false
			}
		}
	}
	return true
}
