package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	modelapi "github.com/dunkeltech/botbox/backend/internal/engine/api"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
	"github.com/dunkeltech/botbox/backend/internal/groups"
	"github.com/dunkeltech/botbox/backend/internal/memory"
	"github.com/dunkeltech/botbox/backend/internal/orchestrator"
	"github.com/google/uuid"
)

func TestLargeGroupPromptFitsAgentInputLimit(t *testing.T) {
	var members []orchestrator.Member
	for _, name := range []string{"Yoda", "Nova", "Atlas", "Mace"} {
		members = append(members, orchestrator.Member{Kind: orchestrator.MemberBot, Name: name, Instructions: strings.Repeat("role ", 700)})
	}
	var recent []orchestrator.ContextMessage
	for range 4 {
		recent = append(recent, orchestrator.ContextMessage{Speaker: "Nova", Body: strings.Repeat("older planning ", 1100)})
	}
	current := "@Yoda my answer: " + strings.Repeat("ä", 4000)
	recent = append(recent, orchestrator.ContextMessage{Speaker: "Human", Body: current})
	session := engine.Session{Bot: db.Bot{Name: "Yoda", Description: strings.Repeat("role ", 740), ReplyLanguage: "de"}, Messages: []db.Message{{Content: current}}, Preface: orchestrator.BuildContext(orchestrator.ContextInput{Members: members, Recent: recent}), TurnID: uuid.New()}
	session.TeamTool = func(context.Context, string, json.RawMessage) (string, error) { return "", nil }
	for _, external := range []string{"", "saved-session"} {
		prompt := cliTurnPrompt(session, external, engine.DesktopGuidanceFor(true))
		if len(prompt) > 65536 {
			t.Fatalf("group prompt is %d bytes; agent accepts 65536", len(prompt))
		}
		if !strings.HasSuffix(prompt, current) {
			t.Fatal("current human message was truncated")
		}
		if !strings.Contains(prompt, groups.TeamStatusTool) || !strings.Contains(prompt, "turn_id "+session.TurnID.String()) {
			t.Fatal("CLI prompt omits team tools or active turn ID")
		}
	}
}

func TestGroupPromptFitsWithLargestAcceptedMessageAndRole(t *testing.T) {
	session := engine.Session{Bot: db.Bot{Name: strings.Repeat("n", 100), Description: strings.Repeat("r", 4000), ReplyLanguage: strings.Repeat("e", 10)}, Messages: []db.Message{{Content: strings.Repeat("m", 8192)}}, Preface: orchestrator.BuildContext(orchestrator.ContextInput{Recent: []orchestrator.ContextMessage{{Speaker: "Peer", Body: strings.Repeat("p", 100000)}}})}
	if prompt := cliTurnPrompt(session, "resumed", engine.DesktopGuidanceFor(true)); len(prompt) > 65536 {
		t.Fatalf("valid fields exceeded agent input limit: %d", len(prompt))
	}
}

func TestCLITurnRefreshesGroupContext(t *testing.T) {
	for _, external := range []string{"", "resumed-session"} {
		for _, continuation := range []string{"", "Continue after approval"} {
			s := engine.Session{Bot: db.Bot{Name: "Yoda", Description: "Review every proposal for GDPR compliance.", ReplyLanguage: "de"}, Messages: []db.Message{{Content: "@all review"}}, Preface: "Current group transcript.", ContinuationPrompt: continuation}
			prompt := cliTurnPrompt(s, external, "Desktop guidance")
			for _, want := range []string{s.Preface, "Desktop guidance", "You are Yoda", s.Bot.Description, "Reply language: de", "Do not mention yourself"} {
				if !strings.Contains(prompt, want) || strings.Count(prompt, s.Preface) != 1 {
					t.Errorf("resume=%q continuation=%q missing or duplicated %q in %q", external, continuation, want, prompt)
				}
			}
			if continuation != "" && !strings.Contains(prompt, continuation) {
				t.Fatalf("missing continuation in %q", prompt)
			}
		}
	}
}

func TestCLIDirectMessageKeepsGlobalRoleOnResume(t *testing.T) {
	s := engine.Session{Bot: db.Bot{Name: "Mace Windu", Description: "Keep the system architecture understandable."}, Messages: []db.Message{{Content: "Review the design"}}}
	for _, external := range []string{"", "resumed"} {
		prompt := cliTurnPrompt(s, external, "Desktop guidance")
		if !strings.Contains(prompt, s.Bot.Description) || !strings.Contains(prompt, "You are Mace Windu") {
			t.Fatalf("missing global role in %q", prompt)
		}
	}
}

func TestModelToolsRejectHostBackedMemoryDriver(t *testing.T) {
	driver, err := compute.NewMemoryDriver()
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	tools := &sandboxTools{driver: driver}
	if _, err = tools.Call(context.Background(), "exec", []byte(`{"command":"must never run"}`)); err == nil {
		t.Fatal("model tool reached host-backed compute")
	}
}

func TestModelTeamToolsUseTurnBinding(t *testing.T) {
	ctx := context.Background()
	var tools sandboxTools
	args := json.RawMessage(`{"text":"Tests passed"}`)
	if _, err := tools.Call(ctx, groups.TeamProgressTool, args); !errors.Is(err, groups.ErrTeamToolUnavailable) {
		t.Fatalf("unbound tool: %v", err)
	}
	tools.team = func(_ context.Context, name string, input json.RawMessage) (string, error) {
		if name != groups.TeamProgressTool || string(input) != string(args) {
			t.Fatalf("unexpected team tool call %q %s", name, input)
		}
		return `{"posted":true}`, nil
	}
	if result, err := tools.Call(ctx, groups.TeamProgressTool, args); err != nil || result != `{"posted":true}` {
		t.Fatalf("bound team tool: %q %v", result, err)
	}
}

func TestModelMemoryToolsAndCLIMemoryPrompt(t *testing.T) {
	ctx := context.Background()
	var tools sandboxTools
	args := json.RawMessage(`{"title":"Convention","content":"Run tests cleanly"}`)
	if _, err := tools.Call(ctx, memory.ToolSave, args); !errors.Is(err, memory.ErrToolUnavailable) {
		t.Fatalf("unbound memory tool: %v, want ErrToolUnavailable", err)
	}
	tools.memory = func(_ context.Context, name string, input json.RawMessage) (string, error) {
		if name != memory.ToolSave || string(input) != string(args) {
			t.Fatalf("unexpected memory tool call %q %s", name, input)
		}
		return `{"saved":true}`, nil
	}
	if result, err := tools.Call(ctx, memory.ToolSave, args); err != nil || result != `{"saved":true}` {
		t.Fatalf("bound memory tool: %q %v", result, err)
	}

	s := engine.Session{
		Bot:          db.Bot{Name: "Atlas"},
		MemoryPrompt: "## Bot Knowledge Base\nAlways run tests cleanly.",
		Messages:     []db.Message{{Content: "Hello"}},
	}
	prompt := cliTurnPrompt(s, "", "Desktop guidance")
	if !strings.Contains(prompt, s.MemoryPrompt) {
		t.Fatalf("cliTurnPrompt omitted MemoryPrompt: %q", prompt)
	}
}

func TestModelAPIRunnerMemoryToolLoop(t *testing.T) {
	var step atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		switch step.Add(1) {
		case 1:
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_mem_1\",\"type\":\"function\",\"function\":{\"name\":\"memory_save\",\"arguments\":\"{\\\"title\\\":\\\"Commits\\\",\\\"content\\\":\\\"Use imperative English commits.\\\"}\"}}]}}]}\n\n")
			_, _ = fmt.Fprint(w, "data: {\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":8}}\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Saved to your knowledge base.\"}}]}\n\n")
			_, _ = fmt.Fprint(w, "data: {\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":6}}\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		}
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	var called bool
	tools := &sandboxTools{
		memory: func(_ context.Context, name string, raw json.RawMessage) (string, error) {
			called = true
			if name != memory.ToolSave {
				t.Fatalf("tool name = %q, want %s", name, memory.ToolSave)
			}
			return `{"saved":true,"memory_id":"` + uuid.NewString() + `"}`, nil
		},
	}

	extraTools := make([]map[string]any, 0, 4)
	for _, def := range memory.ToolDefinitions(false) {
		extraTools = append(extraTools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        def.Name,
				"description": def.Description,
				"parameters":  def.InputSchema,
			},
		})
	}

	provider := &modelapi.Provider{
		Client:         server.Client(),
		URL:            server.URL,
		Model:          "fake-model",
		BotID:          uuid.NewString(),
		Tools:          tools,
		ExtraTools:     extraTools,
		ExtraToolsOnly: true,
	}

	var events []engine.Event
	err := provider.Run(context.Background(), []modelapi.Message{
		{Role: "system", Content: "You have memory tools."},
		{Role: "user", Content: "Remember we use imperative English commits."},
	}, func(ev engine.Event) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("Provider.Run failed: %v", err)
	}
	if !called {
		t.Fatal("expected memory_save tool callback to be invoked")
	}
	var sawText string
	for _, ev := range events {
		if ev.Kind == engine.EventKindTextDelta {
			sawText += ev.Text
		}
	}
	if !strings.Contains(sawText, "Saved to your knowledge base.") {
		t.Fatalf("unexpected assistant reply %q", sawText)
	}
}

func TestCLIOnSimulatedComputerHasActionableFailure(t *testing.T) {
	driver, err := compute.NewMemoryDriver()
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	err = runCLITurn(context.Background(), nil, nil, nil, driver, engine.Session{}, func(engine.Event) error { return nil })
	if engine.TurnFailureCode(err) != engine.FailureCLIUnavailable {
		t.Fatalf("failure code=%s", engine.TurnFailureCode(err))
	}
}

func TestRequestSecretAcceptsSelfHostedGitLabBrowserOnly(t *testing.T) {
	for _, tt := range []struct {
		name, args    string
		broker, valid bool
	}{
		{"custom browser", `{"kind":"browser_login","adapter_id":"gitlab_web","target_origin":"https://gitlab.example.org","reason":"Review project"}`, true, true},
		{"disabled broker", `{"kind":"browser_login","adapter_id":"gitlab_web","target_origin":"https://gitlab.example.org","reason":"Review project"}`, false, false},
		{"other adapter", `{"kind":"browser_login","adapter_id":"github_web","target_origin":"https://gitlab.example.org","reason":"Review project"}`, true, false},
		{"custom CLI", `{"kind":"cli_egress","adapter_id":"gitlab_api","target_origin":"https://gitlab.example.org","reason":"Review project"}`, true, true},
		{"invalid target type", `{"kind":"browser_login","adapter_id":"gitlab_web","target_origin":3,"reason":"Review project"}`, true, false},
		{"missing reason", `{"kind":"browser_login","adapter_id":"gitlab_web","target_origin":"https://gitlab.example.org"}`, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := handleRequestSecretTool([]byte(tt.args), tt.broker)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v err=%v", tt.valid, err)
			}
		})
	}
}
