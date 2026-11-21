// SPDX-License-Identifier: AGPL-3.0-only

package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/engine"
)

func TestLiveStreamBeforeEOF(t *testing.T) {
	for _, tc := range []struct{ cli, line string }{
		{"claude", `{"type":"stream_event","event":{"delta":{"type":"text_delta","text":"hello"}}}`},
		{"codex", `{"type":"item.completed","item":{"type":"agent_message","text":"hello"}}`},
		{"agy", `{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"agent_response","text_delta":"hello"}}`},
	} {
		t.Run(tc.cli, func(t *testing.T) {
			reader, writer := io.Pipe()
			defer func() { _ = writer.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			emitted := make(chan engine.Event, 1)
			done := make(chan error, 1)
			go func() {
				done <- engine.Stream(ctx, tc.cli, reader, nil, func(ev engine.Event) error { emitted <- ev; return nil })
			}()
			if _, err := io.WriteString(writer, tc.line+"\n"); err != nil {
				t.Fatal(err)
			}
			if ev := <-emitted; ev.Text != "hello" {
				t.Fatalf("event = %+v", ev)
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLiveStreamFailures(t *testing.T) {
	sentinel := errors.New("sink stopped")
	for _, tc := range []struct {
		name, cli, input string
		sink             func(engine.Event) error
		want             error
	}{
		{"sink", "codex", `{"type":"turn.started"}`, func(engine.Event) error { return sentinel }, sentinel},
		{"oversize", "claude", strings.Repeat("x", (1<<20)+1), func(engine.Event) error { return nil }, engine.ErrLineTooLong},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := engine.Stream(context.Background(), tc.cli, io.NopCloser(strings.NewReader(tc.input)), nil, tc.sink)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		cli, input string
		sink       func(engine.Event) error
	}{
		{"unknown", "", func(engine.Event) error { return nil }},
		{"claude", "{broken", func(engine.Event) error { return nil }},
		{"claude", "", nil},
	} {
		if err := engine.Stream(context.Background(), tc.cli, io.NopCloser(strings.NewReader(tc.input)), nil, tc.sink); err == nil {
			t.Fatal("expected error")
		}
	}
	if err := engine.Stream(context.Background(), "claude", nil, nil, func(engine.Event) error { return nil }); err == nil {
		t.Fatal("expected missing stdout error")
	}
}

func TestLiveStreamFixtures(t *testing.T) {
	for _, tc := range []struct {
		cli, fixture string
		parse        func(context.Context, io.Reader) ([]engine.Event, error)
	}{
		{"claude", "claude_stream.jsonl", engine.ParseClaudeStream},
		{"codex", "codex_stream.jsonl", engine.ParseCodexStream},
		{"agy", "agy_stream.jsonl", engine.ParseAgyStream},
	} {
		t.Run(tc.cli, func(t *testing.T) {
			want, err := tc.parse(context.Background(), openFixture(t, tc.fixture))
			if err != nil {
				t.Fatal(err)
			}
			var got []engine.Event
			err = engine.Stream(context.Background(), tc.cli, openFixture(t, tc.fixture), nil, func(ev engine.Event) error { got = append(got, ev); return nil })
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got=%+v want=%+v", got, want)
			}
		})
	}
}

func TestLiveAndBatchStreamsReportTheSameMalformedRecord(t *testing.T) {
	const input = "{\"type\":\"turn.started\"}\r\n\r\n{\"type\":"
	_, batchErr := engine.ParseCodexStream(t.Context(), strings.NewReader(input))
	var emitted []engine.Event
	liveErr := engine.Stream(t.Context(), "codex", io.NopCloser(strings.NewReader(input)), nil, func(ev engine.Event) error {
		emitted = append(emitted, ev)
		return nil
	})
	for _, err := range []error{batchErr, liveErr} {
		if engine.TurnFailureCode(err) != engine.FailureStreamFormat || !strings.Contains(err.Error(), "codex stream line 2") {
			t.Fatalf("expected a safe error for the second record, got %v", err)
		}
	}
	if len(emitted) != 1 || emitted[0].Kind != engine.EventKindStatus {
		t.Fatalf("valid record before malformed input was not delivered: %+v", emitted)
	}
}

func TestClaudeAssistantTextWithoutPartialEvents(t *testing.T) {
	input := `{"type":"assistant","message":{"content":[{"type":"text","text":"Tests passed"}]}}` + "\n"
	var got []engine.Event
	if err := engine.Stream(context.Background(), "claude", io.NopCloser(strings.NewReader(input)), nil, func(ev engine.Event) error { got = append(got, ev); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != engine.EventKindMessage || got[0].Text != "Tests passed" {
		t.Fatalf("assistant text lost: %+v", got)
	}
}

func TestLiveStreamFormatFailureCategory(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ cli, input string }{
		{"claude", `{"type":"assistant","message":"fixture-private"}`},
		{"codex", `{"type":"fixture-private"`},
		{"agy", `{"type":"fixture-private"`},
		{"claude", strings.Repeat("x", (1<<20)+1)},
	} {
		t.Run(tc.cli, func(t *testing.T) {
			t.Parallel()
			err := engine.Stream(context.Background(), tc.cli, io.NopCloser(strings.NewReader(tc.input)), nil, func(engine.Event) error { return nil })
			if engine.TurnFailureCode(err) != engine.FailureStreamFormat {
				t.Fatalf("error = %v, want stream format failure", err)
			}
			if strings.Contains(err.Error(), "fixture-private") {
				t.Fatal("private stream payload exposed")
			}
			if tc.cli == "claude" && len(tc.input) < 1<<20 {
				var cause *json.UnmarshalTypeError
				if !errors.As(err, &cause) {
					t.Fatal("parser cause lost")
				}
			}
		})
	}
}

func TestLiveStreamDoesNotMisclassifyDependencyErrors(t *testing.T) {
	t.Parallel()
	for _, cause := range []error{io.ErrUnexpectedEOF, context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			t.Parallel()
			reader, writer := io.Pipe()
			if err := writer.CloseWithError(cause); err != nil {
				t.Fatal(err)
			}
			err := engine.Stream(context.Background(), "claude", reader, nil, func(engine.Event) error { return nil })
			if !errors.Is(err, cause) || engine.TurnFailureCode(err) != engine.FailureTurn {
				t.Fatalf("dependency error misclassified: %v", err)
			}
		})
	}
}
