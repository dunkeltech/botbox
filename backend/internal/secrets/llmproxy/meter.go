package llmproxy

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
)

// Usage contains provider accounting only; it never retains response text.
// Known is false if the stream is incomplete or usage cannot be reconciled.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	CacheTokens  int64
	ReportedCost json.Number
	Known        bool
}

// usageCollector bounds retained data to one SSE line or one JSON response.
type usageCollector struct {
	Usage
	buffer    []byte
	streaming bool
	invalid   bool
	terminal  bool
	seen      bool
	hasInput  bool
	hasOutput bool
	anthropic bool
	uncached  int64
	created   int64
}

func (c *usageCollector) Write(data []byte) (int, error) {
	size := len(data)
	if c.invalid {
		return size, nil
	}
	if !c.streaming {
		if len(c.buffer)+size > 1<<20 {
			c.invalid = true
			c.buffer = nil
			return size, nil
		}
		c.buffer = append(c.buffer, data...)
		return size, nil
	}
	for len(data) > 0 {
		at := bytes.IndexByte(data, '\n')
		end := len(data)
		if at >= 0 {
			end = at + 1
		}
		if len(c.buffer)+end > 1<<20 {
			c.invalid = true
			c.buffer = nil
			return size, nil
		}
		c.buffer = append(c.buffer, data[:end]...)
		data = data[end:]
		if at >= 0 {
			line := bytes.TrimSpace(c.buffer)
			if bytes.HasPrefix(line, []byte("data:")) {
				c.parse(bytes.TrimSpace(line[5:]))
			}
			c.buffer = c.buffer[:0]
		}
	}
	return size, nil
}

func (c *usageCollector) parse(data []byte) {
	if bytes.Equal(data, []byte("[DONE]")) {
		c.terminal = true
		return
	}
	if len(data) == 0 {
		return
	}
	var env struct {
		Type    string          `json:"type"`
		Usage   json.RawMessage `json:"usage"`
		Message struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"message"`
		Response struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &env) != nil {
		c.invalid = true
		return
	}
	if env.Type == "message_stop" || env.Type == "response.completed" {
		c.terminal = true
	}
	usage := env.Usage
	if len(env.Message.Usage) > 0 {
		usage = env.Message.Usage
	}
	if len(env.Response.Usage) > 0 {
		usage = env.Response.Usage
	}
	if len(usage) == 0 || bytes.Equal(usage, []byte("null")) {
		return
	}
	var u struct {
		Input         *int64 `json:"input_tokens"`
		Output        *int64 `json:"output_tokens"`
		Prompt        *int64 `json:"prompt_tokens"`
		Completion    *int64 `json:"completion_tokens"`
		Read          *int64 `json:"cache_read_input_tokens"`
		Created       *int64 `json:"cache_creation_input_tokens"`
		PromptDetails struct {
			Cached *int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		InputDetails struct {
			Cached *int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		Cost json.Number `json:"cost"`
	}
	if json.Unmarshal(usage, &u) != nil {
		c.invalid = true
		return
	}
	input, output := u.Input, u.Output
	if u.Prompt != nil {
		input = u.Prompt
	}
	if u.Completion != nil {
		output = u.Completion
	}
	if strings.HasPrefix(env.Type, "message_") || u.Read != nil || u.Created != nil {
		c.anthropic = true
	}
	if input != nil {
		c.hasInput = true
		c.uncached = *input
		c.InputTokens = *input
	}
	if output != nil {
		c.hasOutput = true
		c.OutputTokens = *output
	}
	cache := u.InputDetails.Cached
	if u.PromptDetails.Cached != nil {
		cache = u.PromptDetails.Cached
	}
	if cache != nil {
		c.CacheTokens = *cache
	}
	if u.Read != nil {
		c.CacheTokens = *u.Read
	}
	// Anthropic usage fields are cumulative and can be updated independently.
	// Retain the uncached count so later cache updates never add it twice.
	if u.Created != nil {
		c.created = *u.Created
	}
	if c.anthropic {
		c.InputTokens = c.uncached
		for _, extra := range []int64{c.CacheTokens, c.created} {
			if extra < 0 || c.InputTokens > math.MaxInt64-extra {
				c.invalid = true
				return
			}
			c.InputTokens += extra
		}
	}
	if u.Cost != "" {
		c.ReportedCost = u.Cost
	}
	if input != nil || output != nil {
		c.seen = true
	}
	if c.InputTokens < 0 || c.OutputTokens < 0 || c.CacheTokens < 0 || c.CacheTokens > c.InputTokens {
		c.invalid = true
	}
}

func (c *usageCollector) finish(complete bool) Usage {
	if !c.streaming && !c.invalid {
		c.parse(c.buffer)
	}
	c.Known = complete && !c.invalid && c.seen && c.hasInput && c.hasOutput && (!c.streaming || c.terminal)
	c.buffer = nil
	return c.Usage
}

func newUsageCollector(contentType string) *usageCollector {
	return &usageCollector{streaming: strings.HasPrefix(strings.ToLower(contentType), "text/event-stream")}
}
