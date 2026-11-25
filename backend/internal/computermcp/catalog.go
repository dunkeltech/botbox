// SPDX-License-Identifier: AGPL-3.0-only

// Package computermcp binds ACP tool calls to one work sandbox for one bot turn.
package computermcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/dunkeltech/botbox/backend/internal/desk"
)

// ServerName identifies the only computer MCP server exposed to native ACP.
const ServerName = "botbox-computer"

const (
	toolListFiles     = "list_files"
	toolReadFile      = "read_file"
	toolWriteFile     = "write_file"
	toolExec          = "exec"
	toolTerminalStart = "terminal_start"
	toolTerminalRead  = "terminal_read"
	toolTerminalSend  = "terminal_send"
	toolTerminalStop  = "terminal_stop"
)

var errInvalid = errors.New("invalid computer tool arguments")

var desktopNames = func() map[string]bool {
	names := make(map[string]bool)
	for _, tool := range desk.MCPTools() {
		name, _ := tool["name"].(string)
		names[name] = true
	}
	return names
}()

// Tools returns the schemas offered for the work sandbox. Terminal IDs are
// valid only during the turn that opened them.
func Tools() []map[string]any {
	str := map[string]any{"type": "string"}
	num := map[string]any{"type": "integer"}
	flag := map[string]any{"type": "boolean"}
	command := map[string]any{"command": str, "args": map[string]any{"type": "array", "items": str}, "workdir": str, "timeout_ms": num}
	return append(desk.MCPTools(),
		schema(toolListFiles, "List files and directories in the bot computer. Paths are relative to /home/bot.", map[string]any{"path": str}),
		schema(toolReadFile, "Read a UTF-8 file from the bot computer under /home/bot.", map[string]any{"path": str}, "path"),
		schema(toolWriteFile, "Write a UTF-8 file in the bot computer under /home/bot.", map[string]any{"path": str, "content": str}, "path", "content"),
		schema(toolExec, "Run a short command on the bot computer and collect its output and exit status. The working directory defaults to /home/bot.", command, "command"),
		schema(toolTerminalStart, "Start a command on the bot computer and return its terminal ID. Poll terminal_read for output.", command, "command"),
		schema(toolTerminalRead, "Read bounded stdout and stderr from a running or finished command. Pass next_offset from the last result to read new output.", map[string]any{"terminal_id": str, "offset": num}, "terminal_id"),
		schema(toolTerminalSend, "Send text to a running command's stdin. Set close_input to send EOF.", map[string]any{"terminal_id": str, "input": str, "close_input": flag}, "terminal_id"),
		schema(toolTerminalStop, "Stop a running command on the bot computer.", map[string]any{"terminal_id": str}, "terminal_id"),
	)
}

func schema(name, description string, properties map[string]any, required ...string) map[string]any {
	body := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		body["required"] = required
	}
	return map[string]any{"name": name, "description": description, "inputSchema": body}
}

type fileArguments struct {
	Path    string  `json:"path"`
	Content *string `json:"content,omitempty"`
}

// Command describes a terminal invocation on the work computer.
type Command struct {
	Program   string   `json:"command"`
	Args      []string `json:"args"`
	Workdir   string   `json:"workdir"`
	TimeoutMS int64    `json:"timeout_ms"`
}

type terminalArguments struct {
	ID         string `json:"terminal_id"`
	Input      string `json:"input"`
	CloseInput bool   `json:"close_input"`
	Offset     int    `json:"offset"`
}

// ValidateCall checks the server, method, and argument shape before a model tool
// call is delivered to the credential-bearing ACP process.
func ValidateCall(server, name string, arguments json.RawMessage) error {
	if server != ServerName {
		return errInvalid
	}
	_, err := parseArguments(name, arguments)
	return err
}

// ModelTools lists the exact function names emitted by the pinned agy ACP
// client for the computer MCP server.
func ModelTools() map[string]func(json.RawMessage) error {
	tools := make(map[string]func(json.RawMessage) error)
	for _, tool := range Tools() {
		name, _ := tool["name"].(string)
		tools["mcp_"+ServerName+"_"+name] = func(arguments json.RawMessage) error {
			if len(arguments) != 0 {
				var fields map[string]json.RawMessage
				if json.Unmarshal(arguments, &fields) != nil || fields == nil {
					return errInvalid
				}
				// agy adds a display summary to every model-facing schema and
				// removes it before invoking the actual MCP server.
				if summary, present := fields["toolSummary"]; present {
					var text string
					if json.Unmarshal(summary, &text) != nil || len(text) > 4096 {
						return errInvalid
					}
					delete(fields, "toolSummary")
					data, err := json.Marshal(fields)
					if err != nil {
						return errInvalid
					}
					arguments = data
				}
			}
			return ValidateCall(ServerName, name, arguments)
		}
	}
	return tools
}
func parseArguments(name string, raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) < 2 || len(raw) > 1<<20 || raw[0] != '{' || !json.Valid(raw) {
		return nil, errInvalid
	}
	if desktopNames[name] {
		_, err := desk.Parse(name, raw)
		return raw, err
	}
	switch name {
	case toolListFiles, toolReadFile, toolWriteFile:
		var file fileArguments
		if !decode(raw, &file) || !validFile(name, file) {
			return nil, errInvalid
		}
		return file, nil
	case toolExec, toolTerminalStart:
		var cmd Command
		if !decode(raw, &cmd) || !validCommand(cmd) {
			return nil, errInvalid
		}
		return cmd, nil
	case toolTerminalRead, toolTerminalSend, toolTerminalStop:
		var input terminalArguments
		if !decode(raw, &input) || !validTerminalArguments(name, input) {
			return nil, errInvalid
		}
		return input, nil
	default:
		return nil, errInvalid
	}
}

func decode(raw []byte, dst any) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(dst) == nil
}

func validFile(name string, file fileArguments) bool {
	if len(file.Path) > 4096 || strings.ContainsRune(file.Path, 0) || (name != toolListFiles && strings.TrimSpace(file.Path) == "") {
		return false
	}
	if name == toolWriteFile {
		return file.Content != nil && len(*file.Content) <= 512<<10 && utf8.ValidString(*file.Content)
	}
	return file.Content == nil
}

func validCommand(cmd Command) bool {
	if strings.TrimSpace(cmd.Program) == "" || len(cmd.Program) > 4096 || strings.ContainsRune(cmd.Program, 0) ||
		len(cmd.Workdir) > 4096 || strings.ContainsRune(cmd.Workdir, 0) || len(cmd.Args) > 256 ||
		cmd.TimeoutMS < 0 || cmd.TimeoutMS > 300000 {
		return false
	}
	for _, arg := range cmd.Args {
		if len(arg) > 32768 || strings.ContainsRune(arg, 0) {
			return false
		}
	}
	return true
}

func validTerminalArguments(name string, input terminalArguments) bool {
	if strings.TrimSpace(input.ID) == "" || len(input.ID) > 64 || input.Offset < 0 || len(input.Input) > 64<<10 {
		return false
	}
	if name == toolTerminalSend {
		return input.Offset == 0 && (input.Input != "" || input.CloseInput)
	}
	return input.Input == "" && !input.CloseInput && (name == toolTerminalRead || input.Offset == 0)
}
