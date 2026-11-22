// SPDX-License-Identifier: AGPL-3.0-only

package llmproxy

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"time"
)

// geminiRefusalWindow covers agy's retry backoff after a stopped stream. agy
// retries any stream that ends early, up to five times over about a minute,
// and every retry is a new billed request that the guardrail stops again. A
// 403 before the stream starts ends the turn at once (ADR-0040).
const geminiRefusalWindow = 2 * time.Minute

// GeminiUpstreamURL is the Gemini API host that agy reaches through the
// /llm/google route (ADR-0040). The sandbox never connects to it directly.
const GeminiUpstreamURL = "https://generativelanguage.googleapis.com"

// geminiPath accepts only the two text generation methods agy uses. Gemini
// names the model in the path, so the pattern also bounds the model ID that
// budgets and prices use.
var geminiPath = regexp.MustCompile(`^/v1beta/models/([a-z0-9][a-z0-9.-]{0,99}):(streamGenerateContent|generateContent)$`)

// geminiRequestModel returns the model of a metered Gemini request. Streaming
// must use alt=sse, because the usage meter only reads SSE or one JSON body.
// Other query parameters are refused. A key= parameter is already removed
// before this check, so only the injected header authenticates.
func geminiRequestModel(path, rawQuery string, body []byte) (string, bool) {
	match := geminiPath.FindStringSubmatch(path)
	if match == nil {
		return "", false
	}
	switch match[2] {
	case "streamGenerateContent":
		if rawQuery != "alt=sse" {
			return "", false
		}
	default:
		if rawQuery != "" {
			return "", false
		}
	}
	var request struct {
		GenerationConfig struct {
			CandidateCount *int `json:"candidateCount"`
		} `json:"generationConfig"`
	}
	if trimmed := bytes.TrimSpace(body); len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(trimmed, &request) != nil {
		return "", false
	}
	// The output guardrail inspects one candidate per chunk. The sandbox
	// writes the request, so it must not ask for more.
	if count := request.GenerationConfig.CandidateCount; count != nil && *count != 1 {
		return "", false
	}
	return match[1], true
}

type geminiCandidate struct {
	FinishReason string `json:"finishReason"`
}

// geminiFinished reports the last chunk of a response. Gemini sends no [DONE]
// line; a finish reason marks the end instead.
func geminiFinished(candidates []geminiCandidate) bool {
	for _, candidate := range candidates {
		if candidate.FinishReason != "" {
			return true
		}
	}
	return false
}

// parseGemini reads usageMetadata. Stream chunks carry running totals, so each
// report replaces the previous one. promptTokenCount already includes cached
// tokens, as Usage expects. Output is billed as candidate plus thought tokens.
// The API omits zero counts, so a finished response without an output count
// produced no output tokens.
func (c *usageCollector) parseGemini(raw json.RawMessage, finished bool) {
	if finished {
		c.terminal = true
	}
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return
	}
	var u struct {
		Prompt     *int64 `json:"promptTokenCount"`
		ToolUse    *int64 `json:"toolUsePromptTokenCount"`
		Candidates *int64 `json:"candidatesTokenCount"`
		Thoughts   *int64 `json:"thoughtsTokenCount"`
		Cached     *int64 `json:"cachedContentTokenCount"`
	}
	if json.Unmarshal(raw, &u) != nil {
		c.invalid = true
		return
	}
	sum := func(parts ...*int64) (int64, bool) {
		var total int64
		for _, part := range parts {
			if part == nil {
				continue
			}
			if *part < 0 || total > math.MaxInt64-*part {
				return 0, false
			}
			total += *part
		}
		return total, true
	}
	input, inputOK := sum(u.Prompt, u.ToolUse)
	output, outputOK := sum(u.Candidates, u.Thoughts)
	cache, cacheOK := sum(u.Cached)
	if !inputOK || !outputOK || !cacheOK || cache > input {
		c.invalid = true
		return
	}
	c.InputTokens, c.OutputTokens, c.CacheTokens = input, output, cache
	c.seen = true
	if u.Prompt != nil {
		c.hasInput = true
	}
	if u.Candidates != nil || u.Thoughts != nil || finished {
		c.hasOutput = true
	}
}

// refuseNextGemini makes the next Gemini request of this bot fail before it
// reaches the provider. The handler is scoped to one bot.
func (h *proxyHandler) refuseNextGemini() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.refuseGeminiUntil = h.options.Now().Add(geminiRefusalWindow)
}

// takeGeminiRefusal reports whether this request must be refused. It refuses
// one request only, so a later turn is not affected.
func (h *proxyHandler) takeGeminiRefusal() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.refuseGeminiUntil.IsZero() {
		return false
	}
	refuse := h.options.Now().Before(h.refuseGeminiUntil)
	h.refuseGeminiUntil = time.Time{}
	return refuse
}
