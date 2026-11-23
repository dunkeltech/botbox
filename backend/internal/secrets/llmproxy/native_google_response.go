// SPDX-License-Identifier: AGPL-3.0-only

package llmproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

func (p *NativeGoogleProxy) forwardNativeGoogle(ctx context.Context, w http.ResponseWriter, response *http.Response, route proxyRoute, model, token string, stream, metered bool) Usage {
	if response.StatusCode != http.StatusOK {
		status := response.StatusCode
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		// Provider error bodies and headers can contain request credentials.
		writeError(w, p.options.Logger, status, "upstream_error", "Cloud Code request rejected")
		return Usage{Known: response.StatusCode >= 400 && response.StatusCode < 500}
	}
	expected := "application/json"
	if stream {
		expected = "text/event-stream"
	}
	contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if contentType != expected || response.Header.Get("Content-Encoding") != "" {
		http.Error(w, "unsupported Cloud Code response", http.StatusBadGateway)
		return Usage{}
	}
	var body []byte
	var usage Usage
	var ok bool
	if stream {
		body, usage, ok = p.inspectNativeStream(ctx, w, response.Body, route, model, token)
	} else {
		body, ok = readNativeJSON(response.Body, token)
		if ok && metered {
			var inner json.RawMessage
			inner, ok = unwrapNativeResponse(body)
			collector := newUsageCollector("application/json")
			collector.parse(inner)
			usage = collector.finish(ok)
			if _, err := nativeTextParts(body); err != nil || !ok {
				body = nil
				ok = false
			} else if body, ok = p.checkNativeOutput(ctx, w, route, model, body); !ok {
				return usage
			}
		}
	}
	if !ok {
		if body == nil {
			http.Error(w, "invalid Cloud Code response", http.StatusBadGateway)
		}
		return usage
	}
	w.Header().Set("Content-Type", expected)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
	return usage
}

// inspectNativeStream buffers a bounded model response so a malformed frame or
// a match split across deltas fails before any unverified response reaches agy.
func (p *NativeGoogleProxy) inspectNativeStream(ctx context.Context, w http.ResponseWriter, source io.Reader, route proxyRoute, model, token string) ([]byte, Usage, bool) {
	data, err := io.ReadAll(io.LimitReader(source, maxModelBodyBytes+1))
	if err != nil || len(data) > maxModelBodyBytes {
		return nil, Usage{}, false
	}
	frames, usage, ok := nativeGoogleFrames(data, token)
	if !ok {
		return nil, usage, false
	}
	var response nativeStreamDocument
	response.Frames = make([]map[string]any, 0, len(frames))
	var text strings.Builder
	visibleCount := 0
	for _, frame := range frames {
		parts, err := nativeTextParts(frame)
		if err != nil {
			return nil, usage, false
		}
		response.Frames = append(response.Frames, parts.frame)
		for _, part := range parts.visible {
			piece, _ := part["text"].(string)
			text.WriteString(piece)
			visibleCount++
		}
	}
	response.Text = text.String()
	if strings.Contains(response.Text, token) {
		return nil, usage, false
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, usage, false
	}
	checked, ok := p.checkNativeOutput(ctx, w, route, model, encoded)
	if !ok {
		return []byte{}, usage, false
	}
	if !bytes.Equal(checked, encoded) {
		frames, ok = redactNativeStream(response.Text, checked, visibleCount)
		if !ok {
			return nil, usage, false
		}
	}
	var result bytes.Buffer
	for _, frame := range frames {
		result.WriteString("data: ")
		result.Write(frame)
		result.WriteString("\r\n\r\n")
	}
	if result.Len() > maxModelBodyBytes {
		return nil, usage, false
	}
	return result.Bytes(), usage, true
}

type nativeStreamDocument struct {
	Frames []map[string]any `json:"frames"`
	Text   string           `json:"text"`
}

func redactNativeStream(original string, checked []byte, visibleCount int) ([]json.RawMessage, bool) {
	var next nativeStreamDocument
	decoder := json.NewDecoder(bytes.NewReader(checked))
	decoder.UseNumber()
	if decoder.Decode(&next) != nil || len(next.Frames) == 0 {
		return nil, false
	}
	if next.Text != original && visibleCount > 0 {
		remaining := next.Text
		for _, frame := range next.Frames {
			visible, err := visibleNativeParts(frame)
			if err != nil {
				return nil, false
			}
			for _, part := range visible {
				part["text"] = remaining
				remaining = ""
			}
		}
	}
	frames := make([]json.RawMessage, 0, len(next.Frames))
	for _, frame := range next.Frames {
		data, err := json.Marshal(frame)
		if err != nil {
			return nil, false
		}
		frames = append(frames, data)
	}
	return frames, true
}

type nativeResponseParts struct {
	frame   map[string]any
	visible []map[string]any
}

func nativeTextParts(data []byte) (nativeResponseParts, error) {
	var result nativeResponseParts
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&result.frame); err != nil {
		return result, err
	}
	visible, err := visibleNativeParts(result.frame)
	result.visible = visible
	return result, err
}

var errNativeShape = errors.New("unsupported Cloud Code response shape")

func visibleNativeParts(frame map[string]any) ([]map[string]any, error) {
	response, ok := frame["response"].(map[string]any)
	if !ok {
		return nil, errNativeShape
	}
	rawCandidates, exists := response["candidates"]
	if !exists {
		return nil, nil
	}
	candidates, ok := rawCandidates.([]any)
	if !ok || len(candidates) > 1 {
		return nil, errNativeShape
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	candidate, ok := candidates[0].(map[string]any)
	if !ok {
		return nil, errNativeShape
	}
	rawContent, exists := candidate["content"]
	if !exists {
		return nil, nil
	}
	content, ok := rawContent.(map[string]any)
	if !ok {
		return nil, errNativeShape
	}
	rawParts, exists := content["parts"]
	if !exists {
		return nil, nil
	}
	parts, ok := rawParts.([]any)
	if !ok {
		return nil, errNativeShape
	}
	return inspectNativeParts(parts)
}

func inspectNativeParts(parts []any) ([]map[string]any, error) {
	var visible []map[string]any
	for _, item := range parts {
		part, ok := item.(map[string]any)
		if !ok {
			return nil, errNativeShape
		}
		for key, value := range part {
			// Never deliver executable or opaque parts to an authenticated runtime.
			switch key {
			case "text", "thoughtSignature":
				if _, ok := value.(string); !ok {
					return nil, errNativeShape
				}
			case "thought":
				if _, ok := value.(bool); !ok {
					return nil, errNativeShape
				}
			default:
				return nil, errNativeShape
			}
		}
		thought, _ := part["thought"].(bool)
		if _, text := part["text"]; text && !thought {
			visible = append(visible, part)
		}
	}
	return visible, nil
}

func nativeGoogleFrames(body []byte, token string) ([]json.RawMessage, Usage, bool) {
	var frames []json.RawMessage
	collector := newUsageCollector("text/event-stream")
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	needSeparator := false
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			needSeparator = false
			continue
		}
		payload, found := bytes.CutPrefix(line, []byte("data:"))
		if !found || needSeparator {
			return nil, Usage{}, false
		}
		frame, valid := readNativeJSON(bytes.NewReader(bytes.TrimSpace(payload)), token)
		if !valid {
			return nil, Usage{}, false
		}
		inner, valid := unwrapNativeResponse(frame)
		if !valid {
			return nil, Usage{}, false
		}
		collector.parse(inner)
		frames = append(frames, frame)
		needSeparator = true
	}
	if scanner.Err() != nil || len(frames) == 0 || needSeparator || !collector.terminal {
		return nil, Usage{}, false
	}
	return frames, collector.finish(true), true
}

func unwrapNativeResponse(body []byte) (json.RawMessage, bool) {
	var envelope struct {
		Response json.RawMessage `json:"response"`
	}
	var inner struct {
		Candidates []json.RawMessage `json:"candidates"`
	}
	if !decodeJSONObject(body, &envelope) || !decodeJSONObject(envelope.Response, &inner) || len(inner.Candidates) > 1 {
		return nil, false
	}
	return envelope.Response, true
}

func (p *NativeGoogleProxy) checkNativeOutput(ctx context.Context, w http.ResponseWriter, route proxyRoute, model string, body []byte) ([]byte, bool) {
	decision, err := p.options.Guardrails.EvaluateOutputJSON(ctx, route.provider, model, route.path, body)
	if !p.allowGuardrailDecision(w, route, decision.Action, decision.Code, decision.Message, "LLM response blocked by workspace guardrail", err) {
		return nil, false
	}
	if decision.Action == "redacted" {
		return decision.Body, true
	}
	return body, true
}
