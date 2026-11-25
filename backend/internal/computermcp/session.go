// SPDX-License-Identifier: AGPL-3.0-only

package computermcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/desk"
)

// Backend performs operations on the work sandbox, using a trusted driver.
type Backend interface {
	DesktopAction(context.Context, compute.EnvID, string, []byte) (desk.Result, error)
	ListFiles(context.Context, compute.EnvID, string) ([]compute.FileEntry, error)
	ReadFile(context.Context, compute.EnvID, string) ([]byte, error)
	WriteFile(context.Context, compute.EnvID, string, []byte) error
	OpenTerminal(context.Context, compute.EnvID, Command) (Terminal, error)
}

// Session holds the target computer and temporary terminals for one bot turn.
// Call and Close are used by the serial MCP connection.
type Session struct {
	backend Backend
	target  compute.EnvID
	check   func(context.Context) error
	jobs    map[string]*terminalJob
	order   []string
}

// NewSession binds tools to a trusted target and a per-call turn permission check.
func NewSession(backend Backend, target compute.EnvID, check func(context.Context) error) *Session {
	return &Session{backend: backend, target: target, check: check, jobs: make(map[string]*terminalJob)}
}

// Close ends all commands owned by this MCP session.
func (s *Session) Close() {
	for _, job := range s.jobs {
		job.stop()
	}
}

// Call validates one MCP request and executes it in the bound work sandbox.
func (s *Session) Call(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	args, err := parseArguments(name, raw)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.backend == nil || s.target == "" || s.check == nil {
		return nil, errors.New("computer tools unavailable")
	}
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	switch value := args.(type) {
	case json.RawMessage:
		return s.desktop(ctx, name, value)
	case fileArguments:
		return s.file(ctx, name, value)
	case Command:
		return s.command(ctx, name, value)
	case terminalArguments:
		return s.terminal(name, value)
	default:
		return nil, errInvalid
	}
}

type contentBlock struct {
	Type string  `json:"type"`
	Text *string `json:"text,omitempty"`
	MIME string  `json:"mimeType,omitempty"`
	Data string  `json:"data,omitempty"`
}

type toolResult struct {
	Content []contentBlock `json:"content"`
}

func textResult(text string) toolResult {
	return toolResult{Content: []contentBlock{{Type: "text", Text: &text}}}
}

func jsonResult(value any) (toolResult, error) {
	data, err := json.Marshal(value)
	return textResult(string(data)), err
}

func (s *Session) desktop(ctx context.Context, name string, raw []byte) (toolResult, error) {
	result, err := s.backend.DesktopAction(ctx, s.target, name, raw)
	if err != nil {
		return toolResult{}, err
	}
	reply := textResult(result.Text)
	if len(result.Image) > 0 {
		if len(result.Image) > 3<<20 || (result.Mime != "image/png" && result.Mime != "image/jpeg") {
			return toolResult{}, errors.New("unsupported screenshot")
		}
		reply.Content = append(reply.Content, contentBlock{
			Type: "image", MIME: result.Mime, Data: base64.StdEncoding.EncodeToString(result.Image),
		})
	}
	return reply, nil
}

func (s *Session) file(ctx context.Context, name string, args fileArguments) (toolResult, error) {
	switch name {
	case toolListFiles:
		entries, err := s.backend.ListFiles(ctx, s.target, args.Path)
		if err != nil {
			return toolResult{}, err
		}
		if len(entries) > 1024 {
			return jsonResult(struct {
				Entries   []compute.FileEntry `json:"entries"`
				Truncated bool                `json:"truncated"`
			}{Entries: entries[:1024], Truncated: true})
		}
		return jsonResult(entries)
	case toolReadFile:
		content, err := s.backend.ReadFile(ctx, s.target, args.Path)
		if err != nil {
			return toolResult{}, err
		}
		if len(content) > 512<<10 || !utf8.Valid(content) {
			return toolResult{}, errors.New("file must be UTF-8 and at most 512 KiB")
		}
		return textResult(string(content)), nil
	case toolWriteFile:
		if err := s.backend.WriteFile(ctx, s.target, args.Path, []byte(*args.Content)); err != nil {
			return toolResult{}, err
		}
		return textResult("File written"), nil
	default:
		return toolResult{}, errInvalid
	}
}

func printableOutput(data []byte) string {
	text := strings.ToValidUTF8(string(data), "�")
	return strings.Map(func(r rune) rune {
		// Preserve normal whitespace and render terminal controls as data.
		if r < 32 && r != '\n' && r != '\t' && r != '\r' || r == 127 || (r >= 128 && r <= 159) {
			return -1
		}
		return r
	}, text)
}
