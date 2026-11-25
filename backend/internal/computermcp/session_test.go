// SPDX-License-Identifier: AGPL-3.0-only

package computermcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/desk"
)

type fixtureComputer struct {
	calls []compute.EnvID
	files map[string][]byte
}

func (f *fixtureComputer) DesktopAction(_ context.Context, id compute.EnvID, name string, _ []byte) (desk.Result, error) {
	f.calls = append(f.calls, id)
	if name == "screenshot" {
		return desk.Result{Text: "desktop", Image: []byte("fixture image"), Mime: "image/png"}, nil
	}
	return desk.Result{Text: "clicked"}, nil
}

func (f *fixtureComputer) ListFiles(_ context.Context, id compute.EnvID, _ string) ([]compute.FileEntry, error) {
	f.calls = append(f.calls, id)
	return []compute.FileEntry{{Name: "example.txt", Path: "/home/bot/example.txt"}}, nil
}

func (f *fixtureComputer) WriteFile(_ context.Context, id compute.EnvID, path string, content []byte) error {
	f.calls = append(f.calls, id)
	f.files[path] = content
	return nil
}

func (f *fixtureComputer) ReadFile(_ context.Context, id compute.EnvID, path string) ([]byte, error) {
	f.calls = append(f.calls, id)
	return f.files[path], nil
}

func (f *fixtureComputer) OpenTerminal(_ context.Context, id compute.EnvID, cmd Command) (Terminal, error) {
	f.calls = append(f.calls, id)
	if cmd.Program != "go" || cmd.Workdir != "/home/bot" {
		return nil, errors.New("wrong terminal target")
	}
	return &fixtureTerminal{frames: []TerminalFrame{{Data: []byte("ok")}, {Exited: true}}}, nil
}

type fixtureTerminal struct {
	frames []TerminalFrame
}

func (f *fixtureTerminal) Receive() (TerminalFrame, error) {
	if len(f.frames) == 0 {
		return TerminalFrame{}, io.EOF
	}
	frame := f.frames[0]
	f.frames = f.frames[1:]
	return frame, nil
}

func (*fixtureTerminal) SendInput([]byte, bool) error { return nil }
func (*fixtureTerminal) Close()                       {}

func TestComputerSessionRoutesEachToolToExistingWorkSandbox(t *testing.T) {
	computer := &fixtureComputer{files: make(map[string][]byte)}
	allowed := true
	s := NewSession(computer, "work-sandbox", func(context.Context) error {
		if !allowed {
			return errors.New("human has control")
		}
		return nil
	})
	defer s.Close()
	for _, tc := range []struct{ name, args, want string }{
		{"write_file", `{"path":"example.txt","content":"fixture"}`, "File written"},
		{"read_file", `{"path":"example.txt"}`, "fixture"},
		{"exec", `{"command":"go","args":["test"],"workdir":"/home/bot"}`, "stdout"},
		{"screenshot", "{}", "image/png"},
		{"click", `{"x":14,"y":30}`, "clicked"},
	} {
		result, err := s.Call(t.Context(), tc.name, json.RawMessage(tc.args))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		data, err := json.Marshal(result)
		if err != nil || !strings.Contains(string(data), tc.want) {
			t.Fatalf("%s result = %s, %v", tc.name, data, err)
		}
	}
	for _, id := range computer.calls {
		if id != "work-sandbox" {
			t.Fatalf("operation escaped to %s", id)
		}
	}
	before := len(computer.calls)
	for _, tc := range []struct{ name, args string }{
		{"write_file", `{"path":"missing-content"}`},
		{"click", `{"x":-1,"y":0}`},
		{"read_file", `{"path":"x","bot_id":"another-bot"}`},
		{"request_secret", "{}"},
		{"terminal_read", `{"terminal_id":"unknown"}`},
		{"exec", `{"command":"go","timeout_ms":300001}`},
	} {
		if _, err := s.Call(t.Context(), tc.name, json.RawMessage(tc.args)); err == nil {
			t.Fatalf("invalid %s was accepted", tc.name)
		}
	}
	allowed = false
	if _, err := s.Call(t.Context(), "screenshot", json.RawMessage("{}")); err == nil {
		t.Fatal("took screenshot under human control")
	}
	if len(computer.calls) != before {
		t.Fatal("a denied request reached the sandbox")
	}
}
