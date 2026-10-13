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
)

type scriptedTools struct {
	calls int
	out   string
}

func (s *scriptedTools) Call(context.Context, string, json.RawMessage) (string, error) {
	s.calls++
	if s.out == "" {
		return "ok", nil
	}
	return s.out, nil
}

func TestAnthropicComputerToolRewritesMarkerAndImage(t *testing.T) {
	var bodies []string
	var betas []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			t.Errorf("path %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		betas = append(betas, r.Header.Get("anthropic-beta"))
		if len(bodies) == 1 {
			_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3,\"cache_read_input_tokens\":0}}}\n\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"computer\"}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"action\\\":\\\"screenshot\\\"}\"}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n"))
			return
		}
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":4,\"cache_read_input_tokens\":0}}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()
	tools := &scriptedTools{out: modelapi.EncodeImageResult("shot", "image/png", []byte{1, 2, 3}, 1280, 800)}
	provider := &modelapi.Provider{
		Client:         modelapi.AnthropicClient(server.Client()),
		URL:            server.URL + "/v1/chat/completions",
		Model:          "fixture",
		Tools:          tools,
		Computer:       modelapi.ComputerAnthropic,
		MaxScreenshots: 2,
	}
	if err := provider.Run(context.Background(), []modelapi.Message{{Role: "user", Content: "look"}}, func(engine.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || tools.calls != 1 {
		t.Fatalf("bodies=%d calls=%d", len(bodies), tools.calls)
	}
	if !strings.Contains(bodies[0], "computer_20251124") || strings.Contains(bodies[0], "x-botbox-computer") {
		t.Fatalf("tool shape %s", bodies[0])
	}
	if !strings.Contains(betas[0], "computer-use-2025-11-24") {
		t.Fatalf("beta %q", betas[0])
	}
	if !strings.Contains(bodies[1], `"media_type":"image/png"`) || !strings.Contains(bodies[1], "AQID") {
		t.Fatalf("image result %s", bodies[1])
	}
}

func TestOpenAIComputerTransportUsesResponsesAPI(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("path %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		if len(bodies) == 1 {
			_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"computer_call\",\"call_id\":\"call_1\",\"action\":{\"type\":\"click\",\"x\":3,\"y\":4,\"button\":\"left\"}}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n"))
			return
		}
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n"))
	}))
	defer server.Close()
	tools := &scriptedTools{out: "clicked"}
	provider := &modelapi.Provider{
		Client:   modelapi.OpenAIComputerClient(server.Client()),
		URL:      server.URL + "/v1/chat/completions",
		Model:    "fixture",
		Tools:    tools,
		Computer: modelapi.ComputerOpenAI,
	}
	if err := provider.Run(context.Background(), []modelapi.Message{{Role: "user", Content: "click"}}, func(engine.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || tools.calls != 1 {
		t.Fatalf("bodies=%d calls=%d", len(bodies), tools.calls)
	}
	if !strings.Contains(bodies[0], "computer_use_preview") || !strings.Contains(bodies[0], `"display_width":1280`) || strings.Contains(bodies[0], "x-botbox-computer") {
		t.Fatalf("request %s", bodies[0])
	}
	if !strings.Contains(bodies[1], "computer_call_output") || !strings.Contains(bodies[1], "clicked") {
		t.Fatalf("output %s", bodies[1])
	}
}

func TestComputerUseLimits(t *testing.T) {
	shot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"tool-1\",\"function\":{\"name\":\"screenshot\",\"arguments\":\"{}\"}}]}}]}\n\ndata: {\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"))
	}))
	defer shot.Close()
	tools := &scriptedTools{out: "png"}
	provider := &modelapi.Provider{Client: shot.Client(), URL: shot.URL, Model: "fixture", Tools: tools, Computer: modelapi.ComputerGeneric, MaxScreenshots: 1}
	err := provider.Run(context.Background(), nil, func(engine.Event) error { return nil })
	if !errors.Is(err, engine.ErrScreenshotLimitExceeded) || tools.calls != 1 {
		t.Fatalf("limit err=%v calls=%d", err, tools.calls)
	}

	budget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n"))
	}))
	defer budget.Close()
	provider = &modelapi.Provider{Client: budget.Client(), URL: budget.URL, Model: "fixture", MaxTurnTokens: 5}
	if err = provider.Run(context.Background(), nil, func(engine.Event) error { return nil }); !errors.Is(err, engine.ErrTurnTokenBudget) {
		t.Fatalf("budget err=%v", err)
	}
}
