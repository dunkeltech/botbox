// SPDX-License-Identifier: AGPL-3.0-only

package engine_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/engine"
)

func TestACPResumeStreamsOnlyCurrentTurn(t *testing.T) {
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}}
{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"old reply"}}}}
{"jsonrpc":"2.0","id":2,"result":{}}
{"jsonrpc":"2.0","id":3,"result":{"configOptions":[]}}
{"jsonrpc":"2.0","id":"permission","method":"session/request_permission","params":{"sessionId":"fixture"}}
{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"new reply"}}}}
{"jsonrpc":"2.0","id":4,"result":{"stopReason":"end_turn","usage":{"inputTokens":10,"outputTokens":5,"cachedReadTokens":2,"thoughtTokens":1}}}
`)
	var output bytes.Buffer
	var events []engine.Event
	err := engine.RunACP(input, &output, engine.ACPTurn{
		Directory: "/workspace", SessionID: "fixture", Model: "gemini-test", Prompt: "fixture prompt",
		MCPServers: []engine.ACPMCPServer{{Name: "botbox-computer", Command: "/bin/fixture", Args: []string{"fixture"}}},
	}, func(event engine.Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if text := eventsOfKind(events, engine.EventKindTextDelta); len(text) != 1 || text[0].Text != "new reply" {
		t.Fatalf("text events = %+v", text)
	}
	if usage := eventsOfKind(events, engine.EventKindUsage); len(usage) != 1 || usage[0].InputTokens != 10 || usage[0].CacheTokens != 2 || usage[0].OutputTokens != 6 {
		t.Fatalf("usage events = %+v", usage)
	}
	var methods []string
	decoder := json.NewDecoder(&output)
	for decoder.More() {
		var message struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if err := decoder.Decode(&message); err != nil {
			t.Fatal(err)
		}
		if message.Method != "" {
			methods = append(methods, message.Method)
			if message.Method == "session/load" && !strings.Contains(string(message.Params), `"name":"botbox-computer"`) {
				t.Fatal("resumed session lost its MCP tools")
			}
		} else if string(message.Result) != `{"outcome":{"outcome":"cancelled"}}` {
			t.Fatalf("permission response = %s", message.Result)
		}
	}
	if strings.Join(methods, ",") != "initialize,session/load,session/set_config_option,session/prompt" {
		t.Fatalf("ACP requests = %v", methods)
	}
}

func TestACPRejectsUnsafeOrIncompleteReplies(t *testing.T) {
	const init = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}` + "\n"
	const session = `{"jsonrpc":"2.0","id":2,"result":{"sessionId":"fixture"}}` + "\n"
	tests := []struct {
		name, response string
		want           string
	}{
		{"bad protocol", `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":2}}`, engine.FailureStreamFormat},
		{"login required", init + `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"private upstream detail"}}`, engine.FailureAgyLoginRequired},
		{"wrong session", init + session + `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"foreign","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"private upstream detail"}}}}`, engine.FailureStreamFormat},
		{"no stop reason", init + session + `{"jsonrpc":"2.0","id":3,"result":{}}`, engine.FailureStreamFormat},
		{"unexpected EOF", init + session, engine.FailureStreamFormat},
		{"provider error", init + session + `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"private upstream detail"}}`, engine.FailureProvider},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			err := engine.RunACP(strings.NewReader(tc.response), &output, engine.ACPTurn{Directory: "/workspace", Prompt: "fixture"}, func(event engine.Event) error {
				if event.Text != "" {
					return errors.New("unexpected assistant text")
				}
				return nil
			})
			if err == nil || engine.TurnFailureCode(err) != tc.want || strings.Contains(err.Error(), "private upstream detail") {
				t.Fatalf("ACP returned %v, want %s", err, tc.want)
			}
		})
	}
}

func TestACPApprovesOnlyAllowedToolsInTheActiveSession(t *testing.T) {
	const sessionInit = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"result":{"sessionId":"fixture"}}` + "\n"
	const done = `{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}`
	for _, tc := range []struct {
		name, session, tool, args, option, want string
	}{
		{"known tool", "fixture", "mcp_botbox-computer_screenshot", "{}", "allow_once", "selected"},
		{"foreign session", "another", "mcp_botbox-computer_screenshot", "{}", "allow_once", "cancelled"},
		{"native tool", "fixture", "run_command", "{}", "allow_once", "cancelled"},
		{"wrong arguments", "fixture", "mcp_botbox-computer_screenshot", `{"path":"/tmp"}`, "allow_once", "cancelled"},
		{"persistent permission", "fixture", "mcp_botbox-computer_screenshot", "{}", "allow_always", "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := `{"jsonrpc":"2.0","id":"ask","method":"session/request_permission","params":{"sessionId":"` + tc.session +
				`","toolCall":{"name":"` + tc.tool + `","args":` + tc.args + `},"options":[{"kind":"` + tc.option +
				`","optionId":"choice"}]}}` + "\n"
			var output bytes.Buffer
			err := engine.RunACP(strings.NewReader(sessionInit+frame+done), &output, engine.ACPTurn{
				Directory: "/workspace", Prompt: "fixture", ToolValidators: map[string]func(json.RawMessage) error{
					"mcp_botbox-computer_screenshot": func(args json.RawMessage) error {
						if string(args) != "{}" {
							return errors.New("invalid fixture arguments")
						}
						return nil
					},
				},
			}, func(engine.Event) error { return nil })
			if err != nil || !strings.Contains(output.String(), `"outcome":"`+tc.want+`"`) {
				t.Fatalf("permission outcome %s, %v, want %s", output.String(), err, tc.want)
			}
		})
	}
}
