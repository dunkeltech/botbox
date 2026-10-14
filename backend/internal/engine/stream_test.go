package engine_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/engine"
)

func openFixture(t *testing.T, name string) *os.File {
	t.Helper()
	root, err := os.OpenRoot("testdata")
	if err != nil {
		t.Fatalf("open testdata root: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	f, err := root.Open(name)
	if err != nil {
		t.Fatalf("open fixture %s: %v", name, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func collectKinds(events []engine.Event) []engine.EventKind {
	kinds := make([]engine.EventKind, 0, len(events))
	for _, ev := range events {
		kinds = append(kinds, ev.Kind)
	}
	return kinds
}

func TestStreamParsersAgainstFixtures(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("claude fixture maps onto unified event model", func(t *testing.T) {
		t.Parallel()
		f := openFixture(t, "claude_stream.jsonl")
		events, err := engine.ParseClaudeStream(ctx, f)
		if err != nil {
			t.Fatalf("ParseClaudeStream: %v", err)
		}

		want := []engine.EventKind{
			engine.EventKindStatus,
			engine.EventKindTextDelta,
			engine.EventKindToolCall,
			engine.EventKindToolResult,
			engine.EventKindTextDelta,
			engine.EventKindUsage,
			engine.EventKindStatus,
		}
		got := collectKinds(events)
		if len(got) != len(want) {
			t.Fatalf("events = %+v, want kinds %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("kind[%d] = %s, want %s", i, got[i], want[i])
			}
		}
		if events[5].InputTokens != 120 || events[5].OutputTokens != 45 || events[5].CacheTokens != 800 {
			t.Fatalf("unexpected usage: %+v", events[5])
		}
	})

	t.Run("codex fixture maps onto unified event model", func(t *testing.T) {
		t.Parallel()
		f := openFixture(t, "codex_stream.jsonl")
		events, err := engine.ParseCodexStream(ctx, f)
		if err != nil {
			t.Fatalf("ParseCodexStream: %v", err)
		}

		want := []engine.EventKind{
			engine.EventKindStatus,
			engine.EventKindStatus,
			engine.EventKindToolCall,
			engine.EventKindToolResult,
			engine.EventKindTextDelta,
			engine.EventKindUsage,
			engine.EventKindStatus,
		}
		got := collectKinds(events)
		if len(got) != len(want) {
			t.Fatalf("events = %+v, want kinds %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("kind[%d] = %s, want %s", i, got[i], want[i])
			}
		}
		if events[2].ToolName != "shell" {
			t.Fatalf("tool name = %q, want shell", events[2].ToolName)
		}
	})

	t.Run("agy fixture maps stdout and surfaces stderr soft-denial notice", func(t *testing.T) {
		t.Parallel()
		stdout := openFixture(t, "agy_stream.jsonl")
		stderr := openFixture(t, "agy_soft_deny.stderr")
		events, err := engine.ParseAgyStream(ctx, stdout, stderr)
		if err != nil {
			t.Fatalf("ParseAgyStream: %v", err)
		}

		last := events[len(events)-1]
		if last.Kind != engine.EventKindError || last.ErrorCode != "tool_soft_denied" {
			t.Fatalf("expected final soft-denial error event, got %+v", last)
		}
	})
}

func TestStreamParsersNegativeCases(t *testing.T) {
	t.Parallel()

	t.Run("malformed JSON returns error", func(t *testing.T) {
		t.Parallel()
		if _, err := engine.ParseClaudeStream(context.Background(), strings.NewReader("{bad-json\n")); err == nil {
			t.Fatal("expected error for malformed JSON, got nil")
		}
	})

	t.Run("oversized line returns ErrLineTooLong", func(t *testing.T) {
		t.Parallel()
		huge := `{"type":"stream_event","pad":"` + strings.Repeat("a", (1<<20)+16) + `"}`
		_, err := engine.ParseCodexStream(context.Background(), strings.NewReader(huge))
		if !errors.Is(err, engine.ErrLineTooLong) {
			t.Fatalf("err = %v, want ErrLineTooLong", err)
		}
	})

	t.Run("unknown event type is ignored cleanly", func(t *testing.T) {
		t.Parallel()
		events, err := engine.ParseAgyStream(context.Background(), strings.NewReader(`{"type":"future_event","payload":1}`+"\n"), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(events) != 0 {
			t.Fatalf("expected 0 events, got %+v", events)
		}
	})

	t.Run("canceled context stops stream parsing", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := engine.ParseClaudeStream(ctx, strings.NewReader(`{"type":"system"}`+"\n"))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})

	t.Run("nil reader returns error", func(t *testing.T) {
		t.Parallel()
		if _, err := engine.ParseCodexStream(context.Background(), nil); err == nil {
			t.Fatal("expected error for nil reader")
		}
	})
}

func TestParseClaudeStreamAcceptsBlockContent(t *testing.T) {
	t.Parallel()

	const image = "BASE64IMAGE"
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-1"}`,
		`{"type":"user","message":{"role":"user","content":"echoed prompt"}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_shot","name":"screenshot","input":{}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_shot","is_error":false,"content":[{"type":"image","data":"` + image + `","mimeType":"image/png"},{"type":"text","text":"desktop"},{"type":"text","text":"ready"}]}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"","content":[{"type":"text","text":"done"}]}]}}`,
		`{"type":"result","is_error":false,"usage":{"input_tokens":3,"output_tokens":4,"cache_read_input_tokens":5}}`,
		"",
	}, "\n")

	events, err := engine.ParseClaudeStream(context.Background(), strings.NewReader(stream))
	if err != nil {
		t.Fatalf("ParseClaudeStream: %v", err)
	}
	var toolResult *engine.Event
	var message string
	for i := range events {
		ev := &events[i]
		if strings.Contains(ev.Text, image) || strings.Contains(string(ev.ToolInput), image) || strings.Contains(ev.ErrorMessage, image) {
			t.Fatalf("image payload leaked into event %+v", ev)
		}
		if ev.Kind == engine.EventKindToolResult {
			toolResult = ev
		}
		if ev.Kind == engine.EventKindMessage {
			message = ev.Text
		}
	}
	if toolResult == nil {
		t.Fatal("missing tool result")
	}
	if toolResult.ToolID != "toolu_shot" || toolResult.Text != "desktop\nready" || toolResult.IsError {
		t.Fatalf("tool result = %+v", toolResult)
	}
	if message != "done" {
		t.Fatalf("message = %q", message)
	}
}

func TestParseClaudeStreamRejectsNonBlockContent(t *testing.T) {
	t.Parallel()

	cases := []string{
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t","content":1}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t","content":{"raw":true}}]}}`,
		`{"type":"user","message":{"content":1}}`,
	}
	for _, line := range cases {
		t.Run(line, func(t *testing.T) {
			t.Parallel()
			if _, err := engine.ParseClaudeStream(context.Background(), strings.NewReader(line+"\n")); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
