package llmproxy

import (
	"context"
	"testing"
)

func TestRequestTokenBound(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		bounded          bool
	}{
		{"text", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hello"}],"max_tokens":100}`, true},
		{"responses", "/v1/responses", `{"input":"hello","max_output_tokens":100}`, true},
		{"anthropic", "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"max_tokens":100}`, true},
		{"missing", "/v1/messages", `{}`, false},
		{"wrong type", "/v1/messages", `{"max_tokens":"100"}`, false},
		{"negative", "/v1/messages", `{"max_tokens":-1}`, false},
		{"fraction", "/v1/messages", `{"max_tokens":1.5}`, false},
		{"too large", "/v1/messages", `{"max_tokens":1000001}`, false},
		{"multiple completions", "/v1/chat/completions", `{"n":2,"max_tokens":100}`, false},
		{"remote context", "/v1/responses", `{"previous_response_id":"old","max_output_tokens":100}`, false},
		{"image", "/v1/messages", `{"messages":[{"content":[{"type":"image","source":{"data":"opaque"}}]}],"max_tokens":100}`, false},
		{"hosted tool", "/v1/responses", `{"tools":[{"type":"web_search"}],"max_output_tokens":100}`, false},
		{"future opaque tool", "/v1/responses", `{"tools":[{"type":"future_remote_context"}],"max_output_tokens":100}`, false},
		{"invalid json", "/v1/messages", `{`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := boundedRequest(context.Background(), "openai", tc.path, []byte(tc.body))
			input, output, ok := RequestTokenBound(ctx)
			if ok != tc.bounded {
				t.Fatalf("bounded=%v", ok)
			}
			if ok && (input < int64(len(tc.body)) || output != 100) {
				t.Fatalf("bound=%d/%d", input, output)
			}
		})
	}
}
