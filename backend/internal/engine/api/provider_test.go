package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/engine"
	modelapi "github.com/dunkeltech/botbox/backend/internal/engine/api"
	"github.com/dunkeltech/botbox/backend/internal/engine/enginetest"
)

type fakeTools struct{ calls int }

func (f *fakeTools) Call(_ context.Context, name string, args json.RawMessage) (string, error) {
	f.calls++
	if name != "exec" || !strings.Contains(string(args), "test") {
		return "", errors.New("unexpected tool")
	}
	return "tests passed", nil
}
func TestProviderToolLoop(t *testing.T) {
	script := enginetest.NewProvider(
		enginetest.Response{Chunks: []enginetest.Chunk{{Data: `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool-1","function":{"name":"exec","arguments":"{\"command\":\"test\"}"}}]}}]}

data: {"usage":{"prompt_tokens":10,"completion_tokens":2}}

data: [DONE]

`}}},
		enginetest.Response{Chunks: []enginetest.Chunk{{Data: `data: {"choices":[{"delta":{"content":"All passed"}}]}

data: {"usage":{"prompt_tokens":10,"completion_tokens":3}}

data: [DONE]

`}}},
	)
	server := httptest.NewServer(script)
	defer server.Close()
	tools := &fakeTools{}
	p := &modelapi.Provider{Client: server.Client(), URL: server.URL, Model: "fixture", Tools: tools}
	var events []engine.Event
	if err := p.Run(context.Background(), []modelapi.Message{{Role: "user", Content: "Run tests"}}, func(ev engine.Event) error { events = append(events, ev); return nil }); err != nil {
		t.Fatal(err)
	}
	if script.Remaining() != 0 || len(script.Errors()) != 0 || tools.calls != 1 || events[len(events)-1].Kind != engine.EventKindMessage {
		t.Fatalf("remaining=%d errors=%v tools=%d events=%+v", script.Remaining(), script.Errors(), tools.calls, events)
	}
}
func TestProviderFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"invalid json", "data: broken\n", 200},
		{"missing completion", "data: {\"choices\":[]}\n", 200},
		{"negative usage", "data: {\"usage\":{\"prompt_tokens\":-1}}\ndata: [DONE]\n", 200},
		{"dependency", "private upstream details", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			p := &modelapi.Provider{Client: server.Client(), URL: server.URL, Model: "fixture"}
			if err := p.Run(context.Background(), nil, func(engine.Event) error { return nil }); err == nil {
				t.Fatal("expected failure")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &modelapi.Provider{Client: http.DefaultClient, URL: "http://must-not-be-called.invalid", Model: "fixture"}
	if err := p.Run(ctx, nil, func(engine.Event) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}

func TestAnthropicMessagesAdapter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("Anthropic-Version") == "" {
			t.Errorf("invalid Anthropic request: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":4}}}\n\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":2}}\n\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()
	p := &modelapi.Provider{Client: modelapi.AnthropicClient(server.Client()), URL: server.URL + "/v1/chat/completions", Model: "fixture"}
	var events []engine.Event
	if err := p.Run(context.Background(), []modelapi.Message{{Role: "system", Content: "Fixture"}, {Role: "user", Content: "Hello"}}, func(ev engine.Event) error { events = append(events, ev); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[1].InputTokens != 14 || events[1].CacheTokens != 4 || events[2].Text != "Hello" {
		t.Fatalf("events=%+v", events)
	}
}

func TestProviderReportedCostWins(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"cost\":0.0000042}}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	provider := &modelapi.Provider{Client: server.Client(), URL: server.URL, Model: "fixture"}
	var cost *int64
	if err := provider.Run(context.Background(), nil, func(event engine.Event) error {
		if event.Kind == engine.EventKindUsage {
			cost = event.CostMicros
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if cost == nil || *cost != 5 {
		t.Fatalf("cost=%v", cost)
	}
}

func TestAnthropicAdapterPreservesExtraToolDescriptions(t *testing.T) {
	var seenBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seenBody = string(raw)
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":4,\"cache_read_input_tokens\":0}}}\n\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	p := &modelapi.Provider{
		Client:         modelapi.AnthropicClient(server.Client()),
		URL:            server.URL + "/v1/chat/completions",
		Model:          "fixture",
		Tools:          &fakeTools{},
		ExtraToolsOnly: true,
		ExtraTools: []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "mcp__gitlab__get_job",
				"description": "Get details of a GitLab CI job",
				"parameters":  map[string]any{"type": "object"},
			},
		}},
	}
	if err := p.Run(context.Background(), []modelapi.Message{{Role: "user", Content: "check job"}}, func(engine.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seenBody, "Get details of a GitLab CI job") {
		t.Fatalf("expected tool description in Anthropic request, got %s", seenBody)
	}
	if strings.Contains(seenBody, `"name":"exec"`) {
		t.Fatal("sandbox tools were advertised without an isolated desktop")
	}
}
