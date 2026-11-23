// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

// ACPTurn describes one prompt against a dedicated ACP stdio process.
type ACPTurn struct {
	Directory string
	SessionID string
	Prompt    string
	Model     string
}

type acpClient struct {
	input  *bufio.Scanner
	output *json.Encoder
	id     int
	bytes  int
}

type acpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code int `json:"code"`
	} `json:"error,omitempty"`
}

// RunACP runs one ACP protocol v1 turn. The caller owns the transport and
// must interrupt reads and writes when the turn is cancelled.
func RunACP(input io.Reader, output io.Writer, turn ACPTurn, emit func(Event) error) error {
	if !path.IsAbs(turn.Directory) || len(turn.SessionID) > 255 || len(turn.Model) > 100 ||
		strings.TrimSpace(turn.Prompt) == "" || len(turn.Prompt) > 65536 || emit == nil {
		return acpFormatError("invalid turn")
	}
	client := acpClient{input: bufio.NewScanner(input), output: json.NewEncoder(output)}
	client.input.Buffer(make([]byte, 4096), maxStreamLineBytes)
	var init struct {
		ProtocolVersion   int `json:"protocolVersion"`
		AgentCapabilities struct {
			LoadSession bool `json:"loadSession"`
		} `json:"agentCapabilities"`
	}
	if err := client.call("initialize", map[string]any{
		"protocolVersion": 1, "clientCapabilities": struct{}{},
		"clientInfo": map[string]string{"name": "bot.Box", "version": "1"},
	}, &init, nil); err != nil {
		return err
	}
	if init.ProtocolVersion != 1 {
		return acpFormatError("unsupported protocol version")
	}
	sessionID, err := client.session(turn, init.AgentCapabilities.LoadSession)
	if err != nil {
		return err
	}
	if err := emit(Event{Kind: EventKindStatus, SessionID: sessionID}); err != nil {
		return err
	}
	var result struct {
		StopReason string `json:"stopReason"`
		Usage      struct {
			Input   int64 `json:"inputTokens"`
			Output  int64 `json:"outputTokens"`
			Cached  int64 `json:"cachedReadTokens"`
			Thought int64 `json:"thoughtTokens"`
		} `json:"usage"`
	}
	if err := client.call("session/prompt", map[string]any{
		"sessionId": sessionID, "prompt": []map[string]string{{"type": "text", "text": turn.Prompt}},
	}, &result, func(params json.RawMessage) error { return acpUpdate(params, sessionID, emit) }); err != nil {
		return err
	}
	switch result.StopReason {
	case "end_turn", "max_tokens", "max_turn_requests", "refusal":
	case "cancelled":
		return WithFailureCode(FailureProvider, errors.New("ACP turn cancelled"))
	default:
		return acpFormatError("unknown stop reason")
	}
	// The egress meter takes precedence when it has per-request usage.
	usage := result.Usage
	if usage.Input < 0 || usage.Cached < 0 || usage.Output < 0 || usage.Thought < 0 || usage.Output+usage.Thought < 0 {
		return acpFormatError("invalid usage")
	}
	if usage.Input != 0 || usage.Cached != 0 || usage.Output != 0 || usage.Thought != 0 {
		return emit(Event{Kind: EventKindUsage, InputTokens: usage.Input, OutputTokens: usage.Output + usage.Thought, CacheTokens: usage.Cached})
	}
	return nil
}

func (c *acpClient) session(turn ACPTurn, loadSupported bool) (string, error) {
	params := map[string]any{"cwd": turn.Directory, "mcpServers": []any{}}
	var session struct {
		ID string `json:"sessionId"`
	}
	if turn.SessionID == "" {
		if err := c.call("session/new", params, &session, nil); err != nil {
			return "", err
		}
		if strings.TrimSpace(session.ID) == "" || len(session.ID) > 255 {
			return "", acpFormatError("invalid session identifier")
		}
	} else {
		if !loadSupported {
			return "", acpFormatError("agent cannot load sessions")
		}
		params["sessionId"] = turn.SessionID
		if err := c.call("session/load", params, nil, nil); err != nil {
			return "", err
		}
		session.ID = turn.SessionID
	}
	if turn.Model != "" {
		if err := c.call("session/set_config_option", map[string]string{
			"sessionId": session.ID, "configId": "model", "value": turn.Model,
		}, nil, nil); err != nil {
			return "", err
		}
	}
	return session.ID, nil
}

func (c *acpClient) call(method string, params any, result any, onUpdate func(json.RawMessage) error) error {
	c.id++
	id := strconv.Itoa(c.id)
	if err := c.output.Encode(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params}); err != nil {
		return acpFormatError("cannot send request")
	}
	for c.input.Scan() {
		frame := c.input.Bytes()
		c.bytes += len(frame) + 1
		if c.bytes > 8<<20 {
			return acpFormatError("stream exceeds maximum size")
		}
		var msg acpMessage
		if json.Unmarshal(frame, &msg) != nil || msg.JSONRPC != "2.0" {
			return acpFormatError("invalid JSON-RPC message")
		}
		if msg.Method != "" {
			if err := c.handleAgentMessage(msg, onUpdate); err != nil {
				return err
			}
			continue
		}
		if string(msg.ID) != id || (msg.Error == nil) == (len(msg.Result) == 0) {
			return acpFormatError("unexpected JSON-RPC reply")
		}
		if msg.Error != nil {
			if msg.Error.Code == -32000 {
				return WithFailureCode(FailureAgyLoginRequired, errors.New("ACP login required"))
			}
			// Never expose error messages and data that can include credential details.
			return WithFailureCode(FailureProvider, fmt.Errorf("ACP %s failed", method))
		}
		if result != nil && json.Unmarshal(msg.Result, result) != nil {
			return acpFormatError("invalid JSON-RPC result")
		}
		return nil
	}
	return acpFormatError("stream ended before the reply")
}

func (c *acpClient) handleAgentMessage(msg acpMessage, onUpdate func(json.RawMessage) error) error {
	if len(msg.ID) == 0 {
		if msg.Method == "session/update" && onUpdate != nil {
			return onUpdate(msg.Params)
		}
		return nil
	}
	reply := map[string]any{"jsonrpc": "2.0", "id": msg.ID}
	if msg.Method == "session/request_permission" {
		reply["result"] = map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}
	} else {
		// The runner advertises no client-side file or terminal capabilities.
		reply["error"] = map[string]any{"code": -32601, "message": "unsupported client method"}
	}
	if err := c.output.Encode(reply); err != nil {
		return acpFormatError("cannot reply to agent request")
	}
	return nil
}

func acpUpdate(params json.RawMessage, sessionID string, emit func(Event) error) error {
	var update struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Kind    string `json:"sessionUpdate"`
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &update) != nil || update.SessionID != sessionID || update.Update.Kind == "" {
		return acpFormatError("invalid session update")
	}
	if update.Update.Kind == "agent_message_chunk" && update.Update.Content.Type == "text" && update.Update.Content.Text != "" {
		return emit(Event{Kind: EventKindTextDelta, Text: update.Update.Content.Text})
	}
	return nil
}

func acpFormatError(message string) error {
	return WithFailureCode(FailureStreamFormat, errors.New("ACP "+message))
}
