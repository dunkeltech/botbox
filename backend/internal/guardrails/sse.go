package guardrails

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"
)

const sseWindowBytes = 512

// SSEOutcome summarizes all guardrail evaluations accumulated across an SSE stream.
type SSEOutcome struct {
	Result  EvalResult
	Blocked bool
}

type sseDeltaKind int

const (
	deltaNone sseDeltaKind = iota
	deltaAnthropicText
	deltaOpenAIChat
	deltaOpenAIResponse
)

type sseDeltaTemplate struct {
	kind         sseDeltaKind
	index        int
	id           string
	model        string
	itemID       string
	outputIndex  int
	contentIndex int
}

// SSEFilter inspects streamed LLM text deltas using a sliding lookahead window
// so patterns spanning multiple token chunks are caught while keeping live
// streaming intact.
type SSEFilter struct {
	compiled    *CompiledPolicy
	provider    string
	dst         io.Writer
	onOutcome   func(SSEOutcome)
	passthrough bool

	lineBuf      bytes.Buffer
	pendingEvent string
	pendingText  strings.Builder
	template     sseDeltaTemplate
	hasTemplate  bool

	blocked   bool
	closed    bool
	acc       evalAccumulator
	auditText strings.Builder
}

// NewSSEFilter wraps dst with workspace guardrail enforcement for SSE streams.
func NewSSEFilter(compiled *CompiledPolicy, provider string, dst io.Writer, onOutcome func(SSEOutcome)) *SSEFilter {
	passthrough := compiled == nil || !compiled.HasOutputBlockingOrRedacting()
	return &SSEFilter{
		compiled:    compiled,
		provider:    provider,
		dst:         dst,
		onOutcome:   onOutcome,
		passthrough: passthrough,
		acc: evalAccumulator{
			slugSeen:  make(map[string]bool),
			labelSeen: make(map[string]bool),
		},
	}
}

// Write processes complete SSE lines as chunks arrive from the upstream provider.
func (f *SSEFilter) Write(p []byte) (int, error) {
	if f.blocked {
		return 0, ErrStreamBlocked
	}
	if f.passthrough {
		if _, err := f.dst.Write(p); err != nil {
			return 0, err
		}
		if f.compiled != nil && f.compiled.HasAnyOutputRule() {
			f.collectAuditBytes(p)
		}
		return len(p), nil
	}

	written := len(p)
	for len(p) > 0 {
		idx := bytes.IndexByte(p, '\n')
		if idx < 0 {
			f.lineBuf.Write(p)
			break
		}
		f.lineBuf.Write(p[:idx+1])
		line := f.lineBuf.String()
		f.lineBuf.Reset()
		if err := f.processLine(line); err != nil {
			return 0, err
		}
		p = p[idx+1:]
	}
	return written, nil
}

func (f *SSEFilter) collectAuditBytes(p []byte) {
	f.lineBuf.Write(p)
	for {
		buf := f.lineBuf.Bytes()
		idx := bytes.IndexByte(buf, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimSpace(string(buf[:idx+1]))
		f.lineBuf.Next(idx + 1)
		if dataPayload, ok := strings.CutPrefix(line, "data:"); ok {
			payload := strings.TrimSpace(dataPayload)
			if payload != "" && payload != "[DONE]" {
				if _, text, okDelta := extractSSETextDelta(payload); okDelta {
					if f.auditText.Len() < 256<<10 {
						f.auditText.WriteString(text)
					}
				}
			}
		}
	}
}

func (f *SSEFilter) processLine(rawLine string) error {
	trimmed := strings.TrimRight(rawLine, "\r\n")
	if strings.HasPrefix(trimmed, "event:") {
		// Hold the event line until we see whether its data payload is a buffered text delta.
		if f.pendingEvent != "" {
			if err := f.flushText(true); err != nil {
				return err
			}
			if _, err := io.WriteString(f.dst, f.pendingEvent); err != nil {
				return err
			}
		}
		f.pendingEvent = rawLine
		return nil
	}

	if dataPayload, ok := strings.CutPrefix(trimmed, "data:"); ok {
		payload := strings.TrimSpace(dataPayload)
		if payload != "" && payload != "[DONE]" {
			if tmpl, deltaText, okDelta := extractSSETextDelta(payload); okDelta {
				f.pendingEvent = ""
				if !f.hasTemplate {
					f.template = tmpl
					f.hasTemplate = true
				}
				f.pendingText.WriteString(deltaText)
				return f.flushText(false)
			}
		}
		// Control, usage, or [DONE] line: flush any held text before forwarding.
		if err := f.flushText(true); err != nil {
			return err
		}
		if f.pendingEvent != "" {
			if _, err := io.WriteString(f.dst, f.pendingEvent); err != nil {
				return err
			}
			f.pendingEvent = ""
		}
		_, err := io.WriteString(f.dst, rawLine)
		return err
	}

	// Blank separator or SSE comment line.
	if trimmed == "" && f.pendingEvent == "" && f.pendingText.Len() > 0 {
		// Suppress blank line belonging to absorbed text delta frame.
		return nil
	}
	if f.pendingEvent != "" {
		if err := f.flushText(true); err != nil {
			return err
		}
		if _, err := io.WriteString(f.dst, f.pendingEvent); err != nil {
			return err
		}
		f.pendingEvent = ""
	}
	_, err := io.WriteString(f.dst, rawLine)
	return err
}

func (f *SSEFilter) flushText(flushAll bool) error {
	if f.pendingText.Len() == 0 {
		return nil
	}
	if !flushAll && f.pendingText.Len() <= sseWindowBytes {
		return nil
	}
	text := f.pendingText.String()
	res, rewritten := f.compiled.EvaluateText(DirectionOutput, text)
	f.acc.record(res)
	if res.Action == ActionBlocked {
		f.blocked = true
		f.pendingText.Reset()
		_ = f.writeBlockedFrame()
		return ErrStreamBlocked
	}

	if flushAll {
		f.pendingText.Reset()
		return f.emitDeltaFrame(rewritten)
	}

	if len(rewritten) <= sseWindowBytes {
		f.pendingText.Reset()
		f.pendingText.WriteString(rewritten)
		return nil
	}

	cut := len(rewritten) - sseWindowBytes
	for cut > 0 && !utf8.RuneStart(rewritten[cut]) {
		cut--
	}
	if cut <= 0 {
		return nil
	}
	prefix := rewritten[:cut]
	tail := rewritten[cut:]
	f.pendingText.Reset()
	f.pendingText.WriteString(tail)
	return f.emitDeltaFrame(prefix)
}

func (f *SSEFilter) emitDeltaFrame(text string) error {
	if text == "" {
		return nil
	}
	frame, err := formatSSETextDelta(f.template, text)
	if err != nil {
		return err
	}
	_, err = io.WriteString(f.dst, frame)
	return err
}

func (f *SSEFilter) writeBlockedFrame() error {
	switch f.provider {
	case "anthropic":
		frame := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"guardrail_blocked\",\"message\":\"LLM response blocked by workspace guardrail\"}}\n\n"
		_, err := io.WriteString(f.dst, frame)
		return err
	default:
		frame := "data: {\"error\":{\"code\":\"guardrail_blocked\",\"message\":\"LLM response blocked by workspace guardrail\"}}\n\ndata: [DONE]\n\n"
		_, err := io.WriteString(f.dst, frame)
		return err
	}
}

// Close flushes any remaining lookahead text and reports the final stream outcome.
func (f *SSEFilter) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true

	var closeErr error
	if f.passthrough {
		if f.compiled != nil && f.compiled.HasAnyOutputRule() && f.auditText.Len() > 0 {
			res, _ := f.compiled.EvaluateText(DirectionOutput, f.auditText.String())
			f.acc.record(res)
		}
	} else if !f.blocked {
		if f.lineBuf.Len() > 0 {
			rem := f.lineBuf.String()
			f.lineBuf.Reset()
			closeErr = f.processLine(rem)
		}
		if closeErr == nil {
			closeErr = f.flushText(true)
		}
		if closeErr == nil && f.pendingEvent != "" {
			_, closeErr = io.WriteString(f.dst, f.pendingEvent)
			f.pendingEvent = ""
		}
	}

	if f.onOutcome != nil {
		res := EvalResult{Action: ActionAllowed}
		if f.acc.matchCount > 0 {
			res = f.acc.result(DirectionOutput)
		}
		f.onOutcome(SSEOutcome{
			Result:  res,
			Blocked: f.blocked,
		})
	}
	return closeErr
}

func extractSSETextDelta(payload string) (sseDeltaTemplate, string, bool) {
	var raw map[string]json.RawMessage
	if json.Unmarshal([]byte(payload), &raw) != nil || raw == nil {
		return sseDeltaTemplate{}, "", false
	}
	var typ string
	_ = json.Unmarshal(raw["type"], &typ)

	if typ == "content_block_delta" {
		var msg struct {
			Index int `json:"index"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if json.Unmarshal([]byte(payload), &msg) == nil && msg.Delta.Type == "text_delta" {
			return sseDeltaTemplate{kind: deltaAnthropicText, index: msg.Index}, msg.Delta.Text, true
		}
		return sseDeltaTemplate{}, "", false
	}

	if typ == "response.output_text.delta" {
		var msg struct {
			ItemID       string `json:"item_id"`
			OutputIndex  int    `json:"output_index"`
			ContentIndex int    `json:"content_index"`
			Delta        string `json:"delta"`
		}
		if json.Unmarshal([]byte(payload), &msg) == nil && msg.Delta != "" {
			return sseDeltaTemplate{
				kind:         deltaOpenAIResponse,
				itemID:       msg.ItemID,
				outputIndex:  msg.OutputIndex,
				contentIndex: msg.ContentIndex,
			}, msg.Delta, true
		}
		return sseDeltaTemplate{}, "", false
	}

	if _, hasChoices := raw["choices"]; hasChoices {
		var msg struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Index        int     `json:"index"`
				FinishReason *string `json:"finish_reason"`
				Delta        struct {
					Content *string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(payload), &msg) == nil && len(msg.Choices) == 1 && msg.Choices[0].Delta.Content != nil && msg.Choices[0].FinishReason == nil {
			text := *msg.Choices[0].Delta.Content
			if text != "" {
				return sseDeltaTemplate{
					kind:  deltaOpenAIChat,
					id:    msg.ID,
					model: msg.Model,
					index: msg.Choices[0].Index,
				}, text, true
			}
		}
	}

	return sseDeltaTemplate{}, "", false
}

func formatSSETextDelta(tmpl sseDeltaTemplate, text string) (string, error) {
	switch tmpl.kind {
	case deltaAnthropicText:
		data, err := json.Marshal(map[string]any{
			"type":  "content_block_delta",
			"index": tmpl.index,
			"delta": map[string]any{
				"type": "text_delta",
				"text": text,
			},
		})
		if err != nil {
			return "", err
		}
		return "event: content_block_delta\ndata: " + string(data) + "\n\n", nil
	case deltaOpenAIResponse:
		data, err := json.Marshal(map[string]any{
			"type":          "response.output_text.delta",
			"item_id":       tmpl.itemID,
			"output_index":  tmpl.outputIndex,
			"content_index": tmpl.contentIndex,
			"delta":         text,
		})
		if err != nil {
			return "", err
		}
		return "data: " + string(data) + "\n\n", nil
	case deltaOpenAIChat:
		data, err := json.Marshal(map[string]any{
			"id":     tmpl.id,
			"object": "chat.completion.chunk",
			"model":  tmpl.model,
			"choices": []map[string]any{
				{
					"index": tmpl.index,
					"delta": map[string]any{
						"content": text,
					},
				},
			},
		})
		if err != nil {
			return "", err
		}
		return "data: " + string(data) + "\n\n", nil
	default:
		return "", nil
	}
}
