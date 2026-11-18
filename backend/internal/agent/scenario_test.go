// SPDX-License-Identifier: AGPL-3.0-only

package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/agent"
	"github.com/dunkeltech/botbox/backend/internal/desk"
)

func TestScriptedFormThroughBrowserAndPixels(t *testing.T) {
	t.Parallel()
	driver := agent.NewFormDriver()
	desktop := agent.NewDesktopService(driver)
	desktop.SetBrowser(driver)
	handler := agent.NewMCPHandler(desktop)

	browser := []string{
		`{"name":"browser_navigate","arguments":{"url":"http://127.0.0.1:8765/form"}}`,
		`{"name":"browser_fill","arguments":{"selector":"#name","text":"Ada"}}`,
		`{"name":"browser_fill","arguments":{"selector":"#email","text":"ada@example.com"}}`,
		`{"name":"browser_click","arguments":{"selector":"#submit"}}`,
		`{"name":"browser_read","arguments":{"selector":"#status"}}`,
	}
	var last string
	for i, call := range browser {
		last = callTool(t, handler, i+1, call)
	}
	ok, name, email := driver.Page().Submitted()
	if !ok || name != "Ada" || email != "ada@example.com" || last != "saved Ada" {
		t.Fatalf("browser path = %v %s %s %q", ok, name, email, last)
	}

	driver = agent.NewFormDriver()
	desktop = agent.NewDesktopService(driver)
	handler = agent.NewMCPHandler(desktop)
	pixels := []string{
		`{"name":"click","arguments":{"x":100,"y":100,"button":"left"}}`,
		`{"name":"type","arguments":{"text":"Ada"}}`,
		`{"name":"click","arguments":{"x":100,"y":160,"button":"left"}}`,
		`{"name":"type","arguments":{"text":"ada@example.com"}}`,
		`{"name":"click","arguments":{"x":100,"y":230,"button":"left"}}`,
	}
	for i, call := range pixels {
		callTool(t, handler, i+1, call)
	}
	ok, name, email = driver.Page().Submitted()
	if !ok || name != "Ada" || email != "ada@example.com" {
		t.Fatalf("pixel path = %v %s %s", ok, name, email)
	}
}

func TestTakeoverCancelsDesktopAction(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	gate := agent.NewControlGate("")
	desktop := agent.NewDesktopService(&blockingRunner{started: started})
	desktop.SetControl(gate)
	done := make(chan error, 1)
	go func() {
		done <- desktop.Click(context.Background(), 10, 10, "left")
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("click did not reach xdotool")
	}
	gate.SetHuman(true)
	select {
	case err := <-done:
		if !errors.Is(err, agent.ErrControlLocked) && !errors.Is(err, context.Canceled) {
			t.Fatalf("click err = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("takeover did not stop the action within 1s")
	}
	if err := desktop.Wait(context.Background(), time.Second); !errors.Is(err, agent.ErrControlLocked) {
		t.Fatalf("wait during takeover = %v", err)
	}
	gate.SetHuman(false)
	if err := desktop.Click(context.Background(), 12, 12, "left"); err != nil {
		t.Fatalf("click after hand-back = %v", err)
	}
}

type blockingRunner struct {
	started chan struct{}
	once    bool
}

func (b *blockingRunner) Run(ctx context.Context, name string, _ ...string) ([]byte, error) {
	if name == "xdotool" && !b.once {
		b.once = true
		close(b.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return []byte("X=10\nY=10\n"), nil
}

func callTool(t *testing.T, handler *agent.MCPHandler, id int, params string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": json.RawMessage(params)})
	if err != nil {
		t.Fatal(err)
	}
	resp, ok := handler.HandleMessage(context.Background(), body)
	if !ok {
		t.Fatalf("tool %s produced no response", params)
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Error  *struct{ Message string } `json:"error"`
		Result json.RawMessage           `json:"result"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Error != nil {
		t.Fatalf("tool %s response %s", params, raw)
	}
	raw = envelope.Result
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		t.Fatalf("result %s", raw)
	}
	for _, item := range parsed.Content {
		if item.Type == "text" {
			return item.Text
		}
	}
	t.Fatalf("no text in %s", raw)
	return ""
}

func TestCoordinateScalingOnClick(t *testing.T) {
	t.Parallel()
	var got []string
	desktop := agent.NewDesktopService(&recordRunner{got: &got})
	_, err := desktop.Perform(context.Background(), desk.Action{Kind: desk.KindClick, X: 319, Y: 199, HasPoint: true, Button: "left", ImageWidth: 640, ImageHeight: 400})
	if err != nil {
		t.Fatal(err)
	}
	joined := stringsJoin(got)
	if !containsAll(joined, "638", "398") {
		t.Fatalf("scaled click args = %s", joined)
	}
	if _, err = desktop.Perform(context.Background(), desk.Action{Kind: desk.KindClick, X: 700, Y: 1, HasPoint: true, ImageWidth: 640, ImageHeight: 400}); !errors.Is(err, agent.ErrInvalidDesktopInput) && !errors.Is(err, desk.ErrInvalid) {
		t.Fatalf("oob scaled click err=%v", err)
	}
}

type recordRunner struct{ got *[]string }

func (r *recordRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	*r.got = append(*r.got, args...)
	return []byte("X=1\nY=1\n"), nil
}

func stringsJoin(parts []string) string {
	out := ""
	for _, part := range parts {
		out += part + " "
	}
	return out
}

func containsAll(s string, parts ...string) bool {
	for _, part := range parts {
		if !contains(s, part) {
			return false
		}
	}
	return true
}

func contains(s, part string) bool {
	return len(s) >= len(part) && (s == part || len(part) == 0 || indexOf(s, part) >= 0)
}

func indexOf(s, part string) int {
	for i := 0; i+len(part) <= len(s); i++ {
		if s[i:i+len(part)] == part {
			return i
		}
	}
	return -1
}
