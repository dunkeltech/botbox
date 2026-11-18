// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestPiStream(t *testing.T) {
	const transcript = `{"type":"session","id":"fixture-session"}
{"type":"message_end","message":{"role":"user","content":"private input"}}
{"type":"tool_execution_start","toolCallId":"read-1","toolName":"read","args":{"path":"notes.md"}}
{"type":"tool_execution_end","toolCallId":"read-1","toolName":"read","result":{"content":[{"type":"text","text":"Notes"},{"type":"image","data":"image-bytes"}]},"isError":false}
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"Done"}}
{"type":"message_end","message":{"role":"assistant","stopReason":"stop","content":[{"type":"thinking","thinking":"private reasoning"},{"type":"text","text":"Done"}]}}
{"type":"agent_settled","aborted":false}
`
	var events []Event
	err := Stream(context.Background(), "pi", io.NopCloser(strings.NewReader(transcript)), nil, func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 ||
		events[0].SessionID != "fixture-session" ||
		events[1].Kind != EventKindToolCall || events[1].ToolID != "read-1" || string(events[1].ToolInput) != `{"path":"notes.md"}` ||
		events[2].Kind != EventKindToolResult || events[2].ToolID != "read-1" || events[2].Text != "Notes" ||
		events[3].Kind != EventKindTextDelta || events[3].Text != "Done" ||
		events[4].Kind != EventKindMessage || events[4].Text != "Done" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestPiStreamFailures(t *testing.T) {
	for _, tt := range []struct {
		name, transcript, code string
		canceled               bool
	}{
		{"truncated", `{"type":"session","id":"id"}`, FailureStreamFormat, false},
		{"malformed", `{"type":`, FailureStreamFormat, false},
		{"provider", "{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"stopReason\":\"error\",\"errorMessage\":\"provider-private\"}}\n{\"type\":\"agent_settled\"}", FailureProvider, false},
		{"aborted", `{"type":"agent_settled","aborted":true}`, "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := Stream(context.Background(), "pi", io.NopCloser(strings.NewReader(tt.transcript)), nil, func(Event) error { return nil })
			if err == nil || strings.Contains(err.Error(), "provider-private") || (tt.canceled && !errors.Is(err, context.Canceled)) || (!tt.canceled && TurnFailureCode(err) != tt.code) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
