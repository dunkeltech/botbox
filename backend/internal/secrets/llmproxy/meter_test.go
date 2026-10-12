package llmproxy

import (
	"strings"
	"testing"
)

func TestUsageCollectorProviderFormats(t *testing.T) {
	for _, tc := range []struct {
		name, body, content  string
		input, output, cache int64
		known                bool
	}{
		{"chat", `{"usage":{"prompt_tokens":12,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":4},"cost":0.25}}`, "application/json", 12, 3, 4, true},
		{"responses", "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":12,\"output_tokens\":3,\"input_tokens_details\":{\"cached_tokens\":4}}}}\n\n", "text/event-stream", 12, 3, 4, true},
		{"anthropic", "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":8,\"cache_read_input_tokens\":4,\"cache_creation_input_tokens\":2}}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3}}\n\ndata: {\"type\":\"message_stop\"}\n\n", "text/event-stream", 14, 3, 4, true},
		{"truncated", "data: {\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3}}\n\n", "text/event-stream", 12, 3, 0, false},
		{"missing output", `{"usage":{"prompt_tokens":1}}`, "application/json", 1, 0, 0, false},
		{"missing input", `{"usage":{"completion_tokens":1}}`, "application/json", 0, 1, 0, false},
		{"missing", `{"text":"private response"}`, "application/json", 0, 0, 0, false},
		{"negative", `{"usage":{"prompt_tokens":-1,"completion_tokens":3}}`, "application/json", -1, 3, 0, false},
		{"wrong type", `{"usage":{"prompt_tokens":"12"}}`, "application/json", 0, 0, 0, false},
		{"invalid cache", `{"usage":{"prompt_tokens":2,"prompt_tokens_details":{"cached_tokens":3}}}`, "application/json", 2, 0, 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newUsageCollector(tc.content)
			// Split every byte to exercise network boundaries inside JSON and SSE lines.
			for _, b := range []byte(tc.body) {
				_, _ = c.Write([]byte{b})
			}
			got := c.finish(true)
			if got.Known != tc.known || got.InputTokens != tc.input || got.OutputTokens != tc.output || got.CacheTokens != tc.cache {
				t.Fatalf("usage=%+v", got)
			}
			if c.buffer != nil {
				t.Fatal("response data retained")
			}
		})
	}
	c := newUsageCollector("text/event-stream")
	_, _ = c.Write([]byte(strings.Repeat("x", (1<<20)+1)))
	if c.finish(true).Known || c.buffer != nil {
		t.Fatal("oversize response retained or priced")
	}
	c = newUsageCollector("application/json")
	_, _ = c.Write([]byte(`{"usage":{"prompt_tokens":1}}`))
	if c.finish(false).Known {
		t.Fatal("interrupted response priced as complete")
	}
}
