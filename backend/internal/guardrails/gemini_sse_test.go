// SPDX-License-Identifier: AGPL-3.0-only

package guardrails_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/guardrails"
	"github.com/dunkeltech/botbox/backend/internal/secrets/llmproxy"
)

const geminiSecret = "sk-fixture-secret-123" //nolint:gosec // fake stored secret for detector tests

func geminiPolicy(action string) *guardrails.CompiledPolicy {
	return guardrails.CompilePolicy(guardrails.Policy{
		Enabled:     true,
		FailureMode: guardrails.FailureClosed,
		Secrets:     guardrails.DetectorConfig{Enabled: true, Direction: guardrails.DirectionOutput, Action: action},
	}, nil, []string{geminiSecret})
}

// geminiFrames builds a Gemini stream with \r\n framing, as the API sends it.
func geminiFrames(chunks ...string) string {
	var out strings.Builder
	for _, chunk := range chunks {
		out.WriteString("data: " + chunk + "\r\n\r\n")
	}
	return out.String()
}

// The secret is split across chunks. A thought, a thought signature, a tool
// call and the finish reason share chunks with the text.
var geminiSecretStream = geminiFrames(
	`{"candidates":[{"content":{"role":"model","parts":[{"text":"Thinking.","thought":true}]},"index":0}],"modelVersion":"gemini-fixture","responseId":"r1"}`,
	`{"candidates":[{"content":{"role":"model","parts":[{"text":"The key is sk-fix","thoughtSignature":"c2ln"}]},"index":0}],"modelVersion":"gemini-fixture","responseId":"r1","usageMetadata":{"promptTokenCount":10}}`,
	`{"candidates":[{"content":{"role":"model","parts":[{"text":"ture-secret-123 and"}]},"index":0}],"modelVersion":"gemini-fixture","responseId":"r1","usageMetadata":{"promptTokenCount":10}}`,
	`{"candidates":[{"content":{"role":"model","parts":[{"text":" more."},{"functionCall":{"name":"run_command","args":{"CommandLine":"ls"}}}]},"finishReason":"STOP","index":0}],"modelVersion":"gemini-fixture","responseId":"r1","usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`,
)

type geminiChunk struct {
	Candidates []struct {
		Content struct {
			Parts []map[string]any `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata map[string]any `json:"usageMetadata"`
	Error         map[string]any `json:"error"`
}

// parseGeminiOutput fails on any frame the Gemini SDK in agy would reject:
// mixed line endings, stray separator lines or a payload that is not JSON.
func parseGeminiOutput(t *testing.T, out string) []geminiChunk {
	t.Helper()
	if strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\n") {
		t.Fatalf("bare \\n line ending in %q", out)
	}
	frames := strings.Split(out, "\r\n\r\n")
	if frames[len(frames)-1] != "" {
		t.Fatalf("unterminated frame in %q", out)
	}
	var chunks []geminiChunk
	for _, frame := range frames[:len(frames)-1] {
		payload, ok := strings.CutPrefix(frame, "data: ")
		var chunk geminiChunk
		if !ok || json.Unmarshal([]byte(payload), &chunk) != nil {
			t.Fatalf("invalid frame %q in %q", frame, out)
		}
		chunks = append(chunks, chunk)
	}
	return chunks
}

func runGeminiFilter(t *testing.T, compiled *guardrails.CompiledPolicy, stream string) (string, guardrails.SSEOutcome, error) {
	t.Helper()
	var out bytes.Buffer
	var outcome guardrails.SSEOutcome
	filter := guardrails.NewSSEFilter(compiled, "google", &out, func(o guardrails.SSEOutcome) { outcome = o })
	var writeErr error
	// Small writes cross line and frame boundaries like network reads do.
	for i := 0; i < len(stream) && writeErr == nil; i += 7 {
		_, writeErr = filter.Write([]byte(stream[i:min(i+7, len(stream))]))
	}
	if closeErr := filter.Close(); writeErr == nil {
		writeErr = closeErr
	}
	return out.String(), outcome, writeErr
}

func TestGeminiSSERedactsAcrossChunks(t *testing.T) {
	out, outcome, err := runGeminiFilter(t, geminiPolicy(guardrails.ActionRedact), geminiSecretStream)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, geminiSecret) || outcome.Result.Action != guardrails.ActionRedacted {
		t.Fatalf("secret split across chunks was not redacted: %q outcome=%+v", out, outcome)
	}
	var visible strings.Builder
	var order []string
	var signature, thought bool
	var last geminiChunk
	for _, chunk := range parseGeminiOutput(t, out) {
		last = chunk
		for _, part := range chunk.Candidates[0].Content.Parts {
			switch {
			case part["thought"] == true:
				thought = true
				order = append(order, "thought")
			case part["functionCall"] != nil:
				order = append(order, "call")
			case part["thoughtSignature"] == "c2ln":
				signature = part["text"] == ""
			case part["text"] != nil && part["text"] != "":
				visible.WriteString(part["text"].(string))
				order = append(order, "text")
			}
		}
	}
	if !strings.HasPrefix(visible.String(), "The key is ") || !strings.HasSuffix(visible.String(), " and more.") || !strings.Contains(visible.String(), "[REDACTED") {
		t.Fatalf("visible text = %q", visible.String())
	}
	if !thought || !signature {
		t.Fatalf("thought=%v signature=%v in %q", thought, signature, out)
	}
	if order[0] != "thought" || order[len(order)-1] != "call" {
		t.Fatalf("part order = %v, want thought first and the tool call after the text", order)
	}
	if last.Candidates[0].FinishReason != "STOP" || last.UsageMetadata["candidatesTokenCount"] != float64(5) {
		t.Fatalf("finish reason or final usage lost: %+v", last)
	}
}

func TestGeminiSSEBlockEndsWithSafetyFinish(t *testing.T) {
	out, outcome, err := runGeminiFilter(t, geminiPolicy(guardrails.ActionBlock), geminiSecretStream)
	if !errors.Is(err, llmproxy.ErrStreamBlocked) || !outcome.Blocked {
		t.Fatalf("err=%v outcome=%+v", err, outcome)
	}
	if strings.Contains(out, geminiSecret) || strings.Contains(out, "ture-secret") {
		t.Fatalf("blocked text forwarded: %q", out)
	}
	chunks := parseGeminiOutput(t, out)
	if final := chunks[len(chunks)-1]; len(final.Candidates) != 1 || final.Candidates[0].FinishReason != "SAFETY" {
		t.Fatalf("final frame = %+v", final)
	}
}

func TestGeminiSSEPassesCleanStreamUnchangedInText(t *testing.T) {
	stream := geminiFrames(
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello "}]},"index":0}],"usageMetadata":{"promptTokenCount":10}}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"world."}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2}}`,
	)
	out, outcome, err := runGeminiFilter(t, geminiPolicy(guardrails.ActionRedact), stream)
	if err != nil || outcome.Result.Action != guardrails.ActionAllowed {
		t.Fatalf("err=%v outcome=%+v", err, outcome)
	}
	var text strings.Builder
	for _, chunk := range parseGeminiOutput(t, out) {
		for _, part := range chunk.Candidates[0].Content.Parts {
			text.WriteString(part["text"].(string))
		}
	}
	if text.String() != "Hello world." {
		t.Fatalf("text = %q", text.String())
	}
}

func TestGeminiSSEAuditOnlyKeepsBytes(t *testing.T) {
	compiled := geminiPolicy(guardrails.ActionAudit)
	out, outcome, err := runGeminiFilter(t, compiled, geminiSecretStream)
	if err != nil || out != geminiSecretStream {
		t.Fatalf("audit mode changed the stream: err=%v", err)
	}
	if outcome.Result.Action != guardrails.ActionAudited || outcome.Result.MatchCount == 0 {
		t.Fatalf("Gemini text not audited: %+v", outcome)
	}
}

func TestGeminiSSELeavesNonTextChunksAlone(t *testing.T) {
	stream := geminiFrames(
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"run_command","args":{"CommandLine":"ls"}}}]},"index":0}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":10}}`,
	)
	out, _, err := runGeminiFilter(t, geminiPolicy(guardrails.ActionRedact), stream)
	if err != nil || out != stream {
		t.Fatalf("non-text chunks changed: err=%v out=%q", err, out)
	}
}

func TestGeminiRequestMetadataIsNotRewritten(t *testing.T) {
	compiled := guardrails.CompilePolicy(guardrails.Policy{
		Enabled:     true,
		FailureMode: guardrails.FailureClosed,
		Secrets:     guardrails.DetectorConfig{Enabled: true, Direction: guardrails.DirectionInput, Action: guardrails.ActionRedact},
	}, nil, []string{geminiSecret})
	body := `{"contents":[{"role":"user","parts":[` +
		`{"text":"use ` + geminiSecret + `"},` +
		`{"inlineData":{"mimeType":"image/png","data":"` + geminiSecret + `"}},` +
		`{"functionCall":{"name":"x","args":{}},"thoughtSignature":"` + geminiSecret + `"}]}]}`
	res, next, err := compiled.EvaluateJSON(guardrails.DirectionInput, []byte(body))
	if err != nil || res.Action != guardrails.ActionRedacted || res.MatchCount != 1 {
		t.Fatalf("err=%v result=%+v", err, res)
	}
	var doc struct {
		Contents []struct {
			Parts []map[string]any `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(next, &doc); err != nil {
		t.Fatal(err)
	}
	parts := doc.Contents[0].Parts
	if strings.Contains(parts[0]["text"].(string), geminiSecret) {
		t.Fatal("text part not redacted")
	}
	if parts[1]["inlineData"].(map[string]any)["data"] != geminiSecret || parts[2]["thoughtSignature"] != geminiSecret {
		t.Fatalf("image data or thought signature rewritten: %s", next)
	}
}
