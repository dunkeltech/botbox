// SPDX-License-Identifier: AGPL-3.0-only

package guardrails

import (
	"encoding/json"
)

// geminiDroppable lists chunk fields that may be left out when a chunk only
// carries text. Gemini repeats running usage totals on stream chunks. Clients
// read the last report, and the proxy meters the upstream bytes before this
// filter, so dropping an intermediate report keeps the lookahead window intact.
var geminiDroppable = map[string]bool{
	"candidates":    true,
	"modelVersion":  true,
	"responseId":    true,
	"createTime":    true,
	"usageMetadata": true,
}

// extractGeminiTextDelta reads one streamGenerateContent chunk (ADR-0040).
// It returns the visible model text for inspection. Parts without visible text
// are split off:
//   - Thoughts and the thought signature of a text part go before the text.
//     They carry no visible text, so the lookahead window keeps running.
//   - Tool calls, other parts and the finish reason go after the text. They end
//     the text, so the filter releases the checked text first.
//
// A text part with a signature keeps the signature in an empty text part, the
// shape Gemini itself uses at the end of a stream.
func extractGeminiTextDelta(raw map[string]json.RawMessage) (sseDelta, bool) {
	var candidates []map[string]json.RawMessage
	if json.Unmarshal(raw["candidates"], &candidates) != nil || len(candidates) != 1 {
		return sseDelta{}, false
	}
	candidate := candidates[0]
	var content map[string]json.RawMessage
	if json.Unmarshal(candidate["content"], &content) != nil || content == nil {
		return sseDelta{}, false
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(content["parts"], &parts) != nil {
		return sseDelta{}, false
	}

	var text string
	var before, after []map[string]json.RawMessage
	for _, part := range parts {
		var piece string
		var thought bool
		_ = json.Unmarshal(part["thought"], &thought)
		_, hasText := part["text"]
		switch {
		case hasText && thought:
			before = append(before, part)
		case !hasText || json.Unmarshal(part["text"], &piece) != nil:
			after = append(after, part)
		default:
			text += piece
			if len(part) > 1 {
				rest := make(map[string]json.RawMessage, len(part))
				for key, value := range part {
					rest[key] = value
				}
				rest["text"] = json.RawMessage(`""`)
				before = append(before, rest)
			}
		}
	}
	if text == "" {
		return sseDelta{}, false
	}

	delta := sseDelta{tmpl: sseDeltaTemplate{kind: deltaGeminiText, role: "model"}, text: text}
	_ = json.Unmarshal(candidate["index"], &delta.tmpl.index)
	_ = json.Unmarshal(content["role"], &delta.tmpl.role)
	_ = json.Unmarshal(raw["modelVersion"], &delta.tmpl.model)
	_ = json.Unmarshal(raw["responseId"], &delta.tmpl.responseID)

	ends := len(after) > 0
	for key := range candidate {
		if key != "content" && key != "index" {
			ends = true
		}
	}
	for key := range raw {
		if !geminiDroppable[key] {
			ends = true
		}
	}
	var err error
	if len(before) > 0 {
		// Only parts and IDs go first. Usage and the finish reason stay at the end.
		top, head := map[string]json.RawMessage{}, map[string]json.RawMessage{}
		for _, key := range []string{"modelVersion", "responseId"} {
			if value, ok := raw[key]; ok {
				top[key] = value
			}
		}
		if index, ok := candidate["index"]; ok {
			head["index"] = index
		}
		if delta.before, err = geminiChunk(top, head, content, before); err != nil {
			return sseDelta{}, false
		}
	}
	if ends {
		if len(after) == 0 {
			after = []map[string]json.RawMessage{{"text": json.RawMessage(`""`)}}
		}
		if delta.after, err = geminiChunk(raw, candidate, content, after); err != nil {
			return sseDelta{}, false
		}
	}
	return delta, true
}

// geminiChunk rebuilds a chunk around parts. top and candidate hold the other
// fields to keep; content keeps its role.
func geminiChunk(top, candidate, content map[string]json.RawMessage, parts []map[string]json.RawMessage) (string, error) {
	partsJSON, err := json.Marshal(parts)
	if err != nil {
		return "", err
	}
	nextContent := map[string]json.RawMessage{"parts": partsJSON}
	if role, ok := content["role"]; ok {
		nextContent["role"] = role
	}
	contentJSON, err := json.Marshal(nextContent)
	if err != nil {
		return "", err
	}
	nextCandidate := make(map[string]json.RawMessage, len(candidate)+1)
	for key, value := range candidate {
		nextCandidate[key] = value
	}
	nextCandidate["content"] = contentJSON
	candidatesJSON, err := json.Marshal([]map[string]json.RawMessage{nextCandidate})
	if err != nil {
		return "", err
	}
	next := make(map[string]json.RawMessage, len(top)+1)
	for key, value := range top {
		next[key] = value
	}
	next["candidates"] = candidatesJSON
	encoded, err := json.Marshal(next)
	return string(encoded), err
}

func formatGeminiTextDelta(tmpl sseDeltaTemplate, text string) (string, error) {
	chunk := map[string]any{
		"candidates": []map[string]any{{
			"content": map[string]any{
				"role":  tmpl.role,
				"parts": []map[string]any{{"text": text}},
			},
			"index": tmpl.index,
		}},
	}
	if tmpl.model != "" {
		chunk["modelVersion"] = tmpl.model
	}
	if tmpl.responseID != "" {
		chunk["responseId"] = tmpl.responseID
	}
	data, err := json.Marshal(chunk)
	if err != nil {
		return "", err
	}
	return "data: " + string(data) + "\n\n", nil
}

// geminiBlockedFrame ends the response like a Gemini safety block. The SDK in
// agy parses it as a normal chunk and reports a content filter. agy still
// retries, so the proxy refuses the next request (ADR-0040).
func geminiBlockedFrame() string {
	data, _ := json.Marshal(map[string]any{"candidates": []map[string]any{{
		"content":      map[string]any{"role": "model", "parts": []map[string]any{{"text": ""}}},
		"finishReason": "SAFETY",
		"index":        0,
	}}})
	return "data: " + string(data) + "\n\n"
}

// geminiErrorFrame uses the Google error shape for an unavailable guardrail.
// Gemini streams have no [DONE] line.
func geminiErrorFrame(message string) string {
	data, _ := json.Marshal(map[string]any{"error": map[string]any{
		"code":    403,
		"message": message,
		"status":  "PERMISSION_DENIED",
	}})
	return "data: " + string(data) + "\n\n"
}
