// SPDX-License-Identifier: AGPL-3.0-only

package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/engine"
)

// The agy_*.jsonl fixtures are stdout of the pinned agy 1.5.9 binary, recorded
// against a local fake Gemini server (ADR-0040). They are not hand-written.

func liveAgy(t *testing.T, stdout, stderr string) ([]engine.Event, error) {
	t.Helper()
	var events []engine.Event
	err := engine.Stream(t.Context(), "agy", io.NopCloser(strings.NewReader(stdout)), io.NopCloser(strings.NewReader(stderr)), func(ev engine.Event) error {
		events = append(events, ev)
		return nil
	})
	return events, err
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := io.ReadAll(openFixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func eventsOfKind(events []engine.Event, kind engine.EventKind) []engine.Event {
	var out []engine.Event
	for _, ev := range events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

func TestAgyRecordedTurns(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		fixture string
		check   func(t *testing.T, events []engine.Event)
	}{
		{"agy_stream.jsonl", func(t *testing.T, events []engine.Event) {
			if events[0].Kind != engine.EventKindStatus || events[0].SessionID != "bdb5fb49-04f9-48fa-bf18-b05c24fde0b0" {
				t.Fatalf("init must carry the conversation for resume, got %+v", events[0])
			}
			calls, results := eventsOfKind(events, engine.EventKindToolCall), eventsOfKind(events, engine.EventKindToolResult)
			if len(calls) != 1 || calls[0].ToolID != "agy-step-2" || calls[0].ToolName != "run_command" || string(calls[0].ToolInput) != `{"CommandLine":"echo spike-ok"}` {
				t.Fatalf("tool call = %+v", calls)
			}
			if len(results) != 1 || results[0].ToolID != calls[0].ToolID || results[0].IsError || !strings.Contains(results[0].Text, "spike-ok") {
				t.Fatalf("tool result = %+v", results)
			}
			// Step usage repeats in the result. Only the result total is counted.
			usage := eventsOfKind(events, engine.EventKindUsage)
			if len(usage) != 1 || usage[0].InputTokens != 468 || usage[0].OutputTokens != 14 || usage[0].CacheTokens != 2000 {
				t.Fatalf("usage = %+v", usage)
			}
			if last := events[len(events)-1]; last.Kind != engine.EventKindStatus || last.Status != "idle" {
				t.Fatalf("last event = %+v", last)
			}
		}},
		{"agy_stream_deltas.jsonl", func(t *testing.T, events []engine.Event) {
			var text strings.Builder
			for _, ev := range eventsOfKind(events, engine.EventKindTextDelta) {
				text.WriteString(ev.Text)
			}
			if text.String() != "Hello from agy.\n" {
				t.Fatalf("streamed text = %q, want each delta once and no thoughts", text.String())
			}
			usage := eventsOfKind(events, engine.EventKindUsage)
			if len(usage) != 1 || usage[0].InputTokens != 500 || usage[0].OutputTokens != 49 || usage[0].CacheTokens != 1000 {
				t.Fatalf("usage = %+v, want output tokens including thinking", usage)
			}
		}},
		{"agy_mcp_tool.jsonl", func(t *testing.T, events []engine.Event) {
			calls := eventsOfKind(events, engine.EventKindToolCall)
			if len(calls) != 1 || calls[0].ToolName != "botbox-team.post_team_progress" || string(calls[0].ToolInput) != `{"note":"halfway"}` {
				t.Fatalf("MCP call = %+v, want server.tool and its arguments", calls)
			}
		}},
		{"agy_tool_error.jsonl", func(t *testing.T, events []engine.Event) {
			results := eventsOfKind(events, engine.EventKindToolResult)
			if len(results) != 1 || !results[0].IsError || results[0].Text != "file does not exist" {
				t.Fatalf("tool error = %+v", results)
			}
		}},
		{"agy_soft_deny.jsonl", func(t *testing.T, events []engine.Event) {
			denials := eventsOfKind(events, engine.EventKindError)
			if len(denials) != 1 || denials[0].ErrorCode != "tool_soft_denied" || denials[0].ErrorMessage != "agy denied tools without asking: Bash" {
				t.Fatalf("soft denial = %+v", denials)
			}
		}},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			t.Parallel()
			batch, err := engine.ParseAgyStream(t.Context(), openFixture(t, tc.fixture))
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, batch)
			live, err := liveAgy(t, readFixture(t, tc.fixture), "")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(live, batch) {
				t.Fatalf("live=%+v batch=%+v", live, batch)
			}
		})
	}
}

func TestAgyFailedResultFailsTurnWithoutLeakingUpstreamText(t *testing.T) {
	t.Parallel()
	for _, fixture := range []string{
		// HTTP 401 from the proxy: agy exits 3.
		"agy_provider_error.jsonl",
		// HTTP 429 retries outlast --print-timeout: agy exits 0.
		"agy_retry_timeout.jsonl",
	} {
		t.Run(fixture, func(t *testing.T) {
			t.Parallel()
			events, err := liveAgy(t, readFixture(t, fixture), "AGY_ERROR: {\"short_error\":\"budget_exceeded\"}\n")
			if engine.TurnFailureCode(err) != engine.FailureProvider {
				t.Fatalf("error = %v, want provider failure", err)
			}
			for _, private := range []string{"invalid_credentials", "budget_exceeded", "Error 4"} {
				if strings.Contains(err.Error(), private) {
					t.Fatalf("upstream text %q in public error %q", private, err)
				}
			}
			for _, ev := range events {
				if ev.Kind == engine.EventKindStatus && ev.Status == "idle" {
					t.Fatal("failed turn reported idle")
				}
				if ev.Kind == engine.EventKindError {
					t.Fatalf("stderr or result error surfaced in chat: %+v", ev)
				}
			}
			if _, batchErr := engine.ParseAgyStream(t.Context(), openFixture(t, fixture)); engine.TurnFailureCode(batchErr) != engine.FailureProvider {
				t.Fatalf("batch error = %v", batchErr)
			}
		})
	}
	t.Run("secret in result error", func(t *testing.T) {
		t.Parallel()
		input := `{"event":"result","result":{"status":"ERROR","error":"Error 401 for key sk-fixture-secret"}}` + "\n"
		_, err := liveAgy(t, input, "")
		if engine.TurnFailureCode(err) != engine.FailureProvider || strings.Contains(err.Error(), "sk-fixture-secret") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("unknown status", func(t *testing.T) {
		t.Parallel()
		_, err := liveAgy(t, `{"event":"result","result":{"status":"CANCELLED"}}`+"\n", "")
		if engine.TurnFailureCode(err) != engine.FailureProvider {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestAgyStderrIsNotSurfaced(t *testing.T) {
	t.Parallel()
	stderr := "jetski: a tool was auto-denied\npermission denied\nAGY_ERROR: {\"short_error\":\"Error 403 PERMISSION_DENIED\"}\n"
	events, err := liveAgy(t, `{"event":"result","result":{"status":"SUCCESS"}}`+"\n", stderr)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != engine.EventKindStatus {
		t.Fatalf("stderr produced events: %+v", events)
	}
}

func TestAgyMalformedRecords(t *testing.T) {
	t.Parallel()
	for name, input := range map[string]string{
		"old type envelope":   `{"type":"result","status":"success","fixture-private":1}`,
		"step without object": `{"event":"step_update"}`,
		"step as string":      `{"event":"step_update","step_update":"fixture-private"}`,
		"result as list":      `{"event":"result","result":["fixture-private"]}`,
		"tool without index":  `{"event":"step_update","step_update":{"state":"ACTIVE","step_type":"tool","tool_info":{"name":"run_command"}}}`,
		"tool without info":   `{"event":"step_update","step_update":{"step_index":2,"state":"DONE","step_type":"tool"}}`,
		"wrong field type":    `{"event":"step_update","step_update":{"step_index":"fixture-private","step_type":"tool"}}`,
		"truncated":           `{"event":"result","result":{"status":"SUCC`,
		"stream without result": `{"event":"init","conversation_id":"c"}` + "\n" +
			`{"event":"step_update","step_update":{"step_index":1,"state":"DONE","step_type":"agent_response","text_delta":"partial"}}`,
		"empty stream": "",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := liveAgy(t, input+"\n", "")
			if engine.TurnFailureCode(err) != engine.FailureStreamFormat {
				t.Fatalf("error = %v, want stream format failure", err)
			}
			if strings.Contains(err.Error(), "fixture-private") {
				t.Fatal("private stream payload exposed")
			}
		})
	}
}

func TestAgyToolSteps(t *testing.T) {
	t.Parallel()
	active := `{"event":"step_update","step_update":{"step_index":4,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"ls"}}}}`
	done := `{"event":"step_update","step_update":{"step_index":4,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"ls"},"output":"a.txt"}}}`
	result := `{"event":"result","result":{"status":"SUCCESS"}}`
	for _, tc := range []struct {
		name  string
		lines []string
		calls int
	}{
		{"repeated active update", []string{active, active, done, result}, 1},
		{"done without active update", []string{done, result}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			events, err := liveAgy(t, strings.Join(tc.lines, "\n")+"\n", "")
			if err != nil {
				t.Fatal(err)
			}
			calls, results := eventsOfKind(events, engine.EventKindToolCall), eventsOfKind(events, engine.EventKindToolResult)
			if len(calls) != tc.calls || len(results) != 1 || results[0].ToolID != calls[0].ToolID || results[0].Text != "a.txt" {
				t.Fatalf("calls=%+v results=%+v", calls, results)
			}
		})
	}
	t.Run("MCP call without server keeps the raw call", func(t *testing.T) {
		t.Parallel()
		line := `{"event":"step_update","step_update":{"step_index":2,"state":"ACTIVE","step_type":"tool","tool_name":"call_mcp_tool","tool_info":{"name":"call_mcp_tool","parameters":{"ToolName":"request_secret","Arguments":{}}}}}`
		events, err := liveAgy(t, line+"\n"+result+"\n", "")
		if err != nil {
			t.Fatal(err)
		}
		calls := eventsOfKind(events, engine.EventKindToolCall)
		if len(calls) != 1 || calls[0].ToolName != "call_mcp_tool" || engine.IsRequestSecretTool(calls[0].ToolName) {
			t.Fatalf("incomplete MCP call = %+v", calls)
		}
	})
	t.Run("MCP secret request is recognized", func(t *testing.T) {
		t.Parallel()
		line := `{"event":"step_update","step_update":{"step_index":2,"state":"ACTIVE","step_type":"tool","tool_name":"call_mcp_tool","tool_info":{"name":"call_mcp_tool","parameters":{"ServerName":"botbox-desktop","ToolName":"request_secret","Arguments":{"reason":"deploy"}}}}}`
		events, err := liveAgy(t, line+"\n"+result+"\n", "")
		if err != nil {
			t.Fatal(err)
		}
		calls := eventsOfKind(events, engine.EventKindToolCall)
		if len(calls) != 1 || !engine.IsRequestSecretTool(calls[0].ToolName) || string(calls[0].ToolInput) != `{"reason":"deploy"}` {
			t.Fatalf("secret request call = %+v", calls)
		}
	})
}

func TestAgyDeniedNoticeIsBounded(t *testing.T) {
	t.Parallel()
	var denied []map[string]string
	for i := range 12 {
		denied = append(denied, map[string]string{"action": "command", "display_name": fmt.Sprintf("Tool%d", i)})
	}
	denied = append(denied,
		map[string]string{"action": "command", "display_name": "Tool0"},
		map[string]string{"action": "", "display_name": " "},
	)
	payload, err := json.Marshal(map[string]any{"event": "result", "result": map[string]any{"status": "SUCCESS", "denied_actions": denied}})
	if err != nil {
		t.Fatal(err)
	}
	events, err := liveAgy(t, string(payload)+"\n", "")
	if err != nil {
		t.Fatal(err)
	}
	notices := eventsOfKind(events, engine.EventKindError)
	want := "agy denied tools without asking: Tool0, Tool1, Tool2, Tool3, Tool4, Tool5, Tool6, Tool7, Tool8, Tool9, …"
	if len(notices) != 1 || notices[0].ErrorMessage != want {
		t.Fatalf("notice = %+v", notices)
	}

	fallback := `{"event":"result","result":{"status":"SUCCESS","denied_actions":[{"action":"file_write","display_name":""}]}}`
	events, err = liveAgy(t, fallback+"\n", "")
	if err != nil {
		t.Fatal(err)
	}
	if notices = eventsOfKind(events, engine.EventKindError); len(notices) != 1 || notices[0].ErrorMessage != "agy denied tools without asking: file_write" {
		t.Fatalf("fallback notice = %+v", notices)
	}
}

func TestAgyCancellationWinsOverMissingResult(t *testing.T) {
	t.Parallel()
	reader, writer := io.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	emitted := make(chan engine.Event, 1)
	done := make(chan error, 1)
	go func() {
		done <- engine.Stream(ctx, "agy", reader, nil, func(ev engine.Event) error { emitted <- ev; return nil })
	}()
	if _, err := io.WriteString(writer, `{"event":"init","conversation_id":"c"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	<-emitted
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) || engine.TurnFailureCode(err) == engine.FailureStreamFormat {
		t.Fatalf("error = %v, want cancellation", err)
	}
	_ = writer.Close()
}
