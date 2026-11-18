// SPDX-License-Identifier: AGPL-3.0-only

package guardrails

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type compiledRule struct {
	slug             string
	name             string
	direction        string
	action           string
	replacementToken string
	regex            *regexp.Regexp
	keywordRegexes   []*regexp.Regexp
}

// CompiledPolicy holds pre-compiled built-in detectors, decrypted stored secrets,
// and active custom rules for fast concurrent evaluation.
type CompiledPolicy struct {
	Policy        Policy
	StoredSecrets []string
	piiEntities   map[string]bool
	customRules   []compiledRule
}

// CompilePolicy validates and compiles a workspace policy and custom rules.
func CompilePolicy(policy Policy, rules []CustomRule, storedSecrets []string) *CompiledPolicy {
	piiMap := make(map[string]bool, len(policy.PII.Entities))
	for _, entity := range policy.PII.Entities {
		piiMap[entity] = true
	}
	compiled := make([]compiledRule, 0, len(rules))
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		_, re, keywords, err := ValidatePattern(rule.MatchMode, rule.Pattern)
		if err != nil {
			continue
		}
		tok, tokErr := ValidateReplacementToken(rule.ReplacementToken)
		if tokErr != nil {
			tok = "[REDACTED]"
		}
		cr := compiledRule{
			slug:             rule.Slug,
			name:             rule.Name,
			direction:        rule.Direction,
			action:           rule.Action,
			replacementToken: tok,
			regex:            re,
		}
		if rule.MatchMode == MatchModeKeywords {
			cr.keywordRegexes = compileKeywordRegexes(keywords)
		}
		compiled = append(compiled, cr)
	}
	return &CompiledPolicy{
		Policy:        policy,
		StoredSecrets: storedSecrets,
		piiEntities:   piiMap,
		customRules:   compiled,
	}
}

func compileKeywordRegexes(keywords []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(keywords))
	for _, kw := range keywords {
		left := "()"
		right := "()"
		if isWordByte(kw[0]) {
			left = `(^|[^A-Za-z0-9_])`
		}
		if isWordByte(kw[len(kw)-1]) {
			right = `($|[^A-Za-z0-9_])`
		}
		pat := `(?i)` + left + `(` + regexp.QuoteMeta(kw) + `)` + right
		if re, err := regexp.Compile(pat); err == nil {
			out = append(out, re)
		}
	}
	return out
}

func isWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

func directionMatches(ruleDir, callDir string) bool {
	return ruleDir == DirectionBoth || ruleDir == callDir
}

// HasOutputBlockingOrRedacting reports whether streamed SSE output requires
// sliding-window lookahead buffering rather than immediate passthrough.
func (c *CompiledPolicy) HasOutputBlockingOrRedacting() bool {
	if c == nil || !c.Policy.Enabled {
		return false
	}
	checkDetector := func(d DetectorConfig) bool {
		return d.Enabled && directionMatches(d.Direction, DirectionOutput) && (d.Action == ActionBlock || d.Action == ActionRedact)
	}
	if checkDetector(c.Policy.Secrets) {
		return true
	}
	if c.Policy.PII.Enabled && len(c.Policy.PII.Entities) > 0 && directionMatches(c.Policy.PII.Direction, DirectionOutput) && (c.Policy.PII.Action == ActionBlock || c.Policy.PII.Action == ActionRedact) {
		return true
	}
	if checkDetector(c.Policy.PromptInjection) {
		return true
	}
	for _, r := range c.customRules {
		if directionMatches(r.direction, DirectionOutput) && (r.action == ActionBlock || r.action == ActionRedact) {
			return true
		}
	}
	return false
}

// HasAnyOutputRule reports whether any enabled rule applies to LLM responses.
func (c *CompiledPolicy) HasAnyOutputRule() bool {
	if c == nil || !c.Policy.Enabled {
		return false
	}
	if c.Policy.Secrets.Enabled && directionMatches(c.Policy.Secrets.Direction, DirectionOutput) {
		return true
	}
	if c.Policy.PII.Enabled && len(c.Policy.PII.Entities) > 0 && directionMatches(c.Policy.PII.Direction, DirectionOutput) {
		return true
	}
	if c.Policy.PromptInjection.Enabled && directionMatches(c.Policy.PromptInjection.Direction, DirectionOutput) {
		return true
	}
	for _, r := range c.customRules {
		if directionMatches(r.direction, DirectionOutput) {
			return true
		}
	}
	return false
}

// EvaluateText scans a plain text string against all active rules for direction,
// returning the precedence-resolved outcome and masked text when action is redacted.
func (c *CompiledPolicy) EvaluateText(direction, text string) (EvalResult, string) {
	if c == nil || !c.Policy.Enabled || text == "" {
		return EvalResult{Action: ActionAllowed}, text
	}
	spans, maxScore := c.collectSpans(direction, text)
	if len(spans) == 0 {
		return EvalResult{Action: ActionAllowed}, text
	}
	return resolveSpans(text, spans, maxScore, direction)
}

func (c *CompiledPolicy) collectSpans(direction, text string) ([]spanMatch, float64) {
	var spans []spanMatch
	var maxScore float64

	if c.Policy.Secrets.Enabled && directionMatches(c.Policy.Secrets.Direction, direction) {
		spans = append(spans, scanSecrets(text, c.Policy.Secrets.Action, c.StoredSecrets)...)
	}
	if c.Policy.PII.Enabled && len(c.piiEntities) > 0 && directionMatches(c.Policy.PII.Direction, direction) {
		spans = append(spans, scanPII(text, c.Policy.PII.Action, c.piiEntities)...)
	}
	if c.Policy.PromptInjection.Enabled && directionMatches(c.Policy.PromptInjection.Direction, direction) {
		injSpans, score := scanPromptInjection(text, c.Policy.PromptInjection.Action)
		if score > maxScore {
			maxScore = score
		}
		spans = append(spans, injSpans...)
	}
	for _, rule := range c.customRules {
		if !directionMatches(rule.direction, direction) {
			continue
		}
		if rule.regex != nil {
			for _, loc := range rule.regex.FindAllStringIndex(text, -1) {
				spans = append(spans, spanMatch{
					start:       loc[0],
					end:         loc[1],
					ruleSlug:    rule.slug,
					label:       rule.replacementToken,
					action:      rule.action,
					replacement: rule.replacementToken,
				})
			}
			continue
		}
		for _, kwRe := range rule.keywordRegexes {
			offset := 0
			for offset < len(text) {
				loc := kwRe.FindStringSubmatchIndex(text[offset:])
				if len(loc) < 6 {
					break
				}
				start := offset + loc[4]
				end := offset + loc[5]
				spans = append(spans, spanMatch{
					start:       start,
					end:         end,
					ruleSlug:    rule.slug,
					label:       rule.replacementToken,
					action:      rule.action,
					replacement: rule.replacementToken,
				})
				if end <= offset {
					offset++
				} else {
					offset = end
				}
			}
		}
	}
	return spans, maxScore
}

func resolveSpans(text string, spans []spanMatch, maxScore float64, direction string) (EvalResult, string) {
	highest := ActionAudited
	hasBlock := false
	hasRedact := false
	for _, s := range spans {
		switch s.action {
		case ActionBlock:
			hasBlock = true
		case ActionRedact:
			hasRedact = true
		}
	}
	switch {
	case hasBlock:
		highest = ActionBlocked
	case hasRedact:
		highest = ActionRedacted
	}

	slugs := make([]string, 0, len(spans))
	slugSeen := make(map[string]bool)
	labels := make([]string, 0, len(spans))
	labelSeen := make(map[string]bool)

	var redactSpans []spanMatch
	for _, s := range spans {
		if !slugSeen[s.ruleSlug] {
			slugSeen[s.ruleSlug] = true
			slugs = append(slugs, s.ruleSlug)
		}
		if s.label != "" && !labelSeen[s.label] {
			labelSeen[s.label] = true
			labels = append(labels, s.label)
		}
		if s.action == ActionRedact {
			redactSpans = append(redactSpans, s)
		}
	}

	outText := text
	if highest == ActionRedacted && len(redactSpans) > 0 {
		outText = applySpanReplacements(text, redactSpans)
	}

	res := EvalResult{
		Action:      highest,
		RuleSlugs:   slugs,
		MatchLabels: labels,
		MatchCount:  len(spans),
		MaxScore:    maxScore,
	}
	res.Summary = formatSafeSummary(res, direction)
	return res, outText
}

func applySpanReplacements(text string, spans []spanMatch) string {
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end > spans[j].end
	})
	var b strings.Builder
	b.Grow(len(text))
	cursor := 0
	for _, s := range spans {
		if s.start < cursor || s.end > len(text) || s.start >= s.end {
			continue
		}
		b.WriteString(text[cursor:s.start])
		b.WriteString(s.replacement)
		cursor = s.end
	}
	b.WriteString(text[cursor:])
	return b.String()
}

func formatSafeSummary(res EvalResult, direction string) string {
	switch res.Action {
	case ActionBlocked:
		dirLabel := "request"
		if direction == DirectionOutput {
			dirLabel = "response"
		}
		return fmt.Sprintf("%d policy violation(s) detected · %s rejected", res.MatchCount, dirLabel)
	case ActionRedacted:
		if len(res.MatchLabels) > 0 {
			return fmt.Sprintf("%d match(es) masked (%s)", res.MatchCount, strings.Join(res.MatchLabels, ", "))
		}
		return fmt.Sprintf("%d match(es) masked", res.MatchCount)
	case ActionAudited:
		if res.MaxScore > 0 {
			return fmt.Sprintf("Heuristic score %.2f · allowed (audit-only)", res.MaxScore)
		}
		return fmt.Sprintf("%d audit match(es) recorded", res.MatchCount)
	default:
		return "No policy matches"
	}
}

// EvaluateJSON inspects text-bearing fields inside an LLM request or response JSON
// payload without disturbing protocol metadata such as model, role, IDs, or usage.
func (c *CompiledPolicy) EvaluateJSON(direction string, raw []byte) (EvalResult, []byte, error) {
	if c == nil || !c.Policy.Enabled || len(bytes.TrimSpace(raw)) == 0 {
		return EvalResult{Action: ActionAllowed}, raw, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		// Fall back to plain-text scanning if the payload is non-JSON text.
		res, out := c.EvaluateText(direction, string(raw))
		return res, []byte(out), nil
	}

	agg := evalAccumulator{
		slugSeen:  make(map[string]bool),
		labelSeen: make(map[string]bool),
	}
	walkJSONValue("", doc, &agg, func(text string) (EvalResult, string) {
		return c.EvaluateText(direction, text)
	})
	if agg.matchCount == 0 {
		return EvalResult{Action: ActionAllowed}, raw, nil
	}
	res := agg.result(direction)
	if res.Action != ActionRedacted {
		return res, raw, nil
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return res, raw, err
	}
	return res, encoded, nil
}

type evalAccumulator struct {
	hasBlock   bool
	hasRedact  bool
	slugs      []string
	slugSeen   map[string]bool
	labels     []string
	labelSeen  map[string]bool
	matchCount int
	maxScore   float64
}

func (a *evalAccumulator) record(r EvalResult) {
	if r.MatchCount == 0 {
		return
	}
	a.matchCount += r.MatchCount
	if r.MaxScore > a.maxScore {
		a.maxScore = r.MaxScore
	}
	switch r.Action {
	case ActionBlocked:
		a.hasBlock = true
	case ActionRedacted:
		a.hasRedact = true
	}
	for _, s := range r.RuleSlugs {
		if !a.slugSeen[s] {
			a.slugSeen[s] = true
			a.slugs = append(a.slugs, s)
		}
	}
	for _, l := range r.MatchLabels {
		if !a.labelSeen[l] {
			a.labelSeen[l] = true
			a.labels = append(a.labels, l)
		}
	}
}

func (a *evalAccumulator) result(direction string) EvalResult {
	action := ActionAudited
	if a.hasBlock {
		action = ActionBlocked
	} else if a.hasRedact {
		action = ActionRedacted
	}
	res := EvalResult{
		Action:      action,
		RuleSlugs:   a.slugs,
		MatchLabels: a.labels,
		MatchCount:  a.matchCount,
		MaxScore:    a.maxScore,
	}
	res.Summary = formatSafeSummary(res, direction)
	return res
}

var skipJSONKeys = map[string]bool{
	"model":          true,
	"role":           true,
	"type":           true,
	"id":             true,
	"object":         true,
	"tool_use_id":    true,
	"call_id":        true,
	"name":           true,
	"stop_reason":    true,
	"finish_reason":  true,
	"status":         true,
	"media_type":     true,
	"mime_type":      true,
	"usage":          true,
	"service_tier":   true,
	"reasoning_type": true,
}

func walkJSONValue(parentKey string, v any, acc *evalAccumulator, eval func(string) (EvalResult, string)) any {
	switch typed := v.(type) {
	case map[string]any:
		// Skip base64 binary blobs (screenshots/images) to avoid false matches on image bytes.
		if t, _ := typed["type"].(string); t == "base64" || t == "image" || t == "input_image" {
			return typed
		}
		for k, val := range typed {
			if skipJSONKeys[k] {
				continue
			}
			if k == "url" {
				if s, ok := val.(string); ok && strings.HasPrefix(s, "data:image/") {
					continue
				}
			}
			typed[k] = walkJSONValue(k, val, acc, eval)
		}
		return typed
	case []any:
		for i, item := range typed {
			typed[i] = walkJSONValue(parentKey, item, acc, eval)
		}
		return typed
	case string:
		if typed == "" || skipJSONKeys[parentKey] {
			return typed
		}
		res, next := eval(typed)
		acc.record(res)
		if res.Action == ActionRedacted {
			return next
		}
		return typed
	default:
		return v
	}
}

// TestRule validates and evaluates a draft custom rule against sampleText without
// writing to the database or impacting live traffic.
func TestRule(matchMode, pattern, replacementToken, sampleText string) TestResult {
	mode, err := ValidateMatchMode(matchMode)
	if err != nil {
		return TestResult{Valid: false, Preview: sampleText, Error: err.Error()}
	}
	cleanPattern, _, _, err := ValidatePattern(mode, pattern)
	if err != nil {
		return TestResult{Valid: false, Preview: sampleText, Error: err.Error()}
	}
	tok, err := ValidateReplacementToken(replacementToken)
	if err != nil {
		return TestResult{Valid: false, Preview: sampleText, Error: err.Error()}
	}
	policy := Policy{
		Enabled:     true,
		FailureMode: FailureClosed,
	}
	compiled := CompilePolicy(policy, []CustomRule{{
		Slug:             "preview-rule",
		Name:             "Preview",
		MatchMode:        mode,
		Pattern:          cleanPattern,
		Direction:        DirectionBoth,
		Action:           ActionRedact,
		ReplacementToken: tok,
		Enabled:          true,
	}}, nil)
	res, preview := compiled.EvaluateText(DirectionBoth, sampleText)
	return TestResult{
		Valid:      true,
		MatchCount: res.MatchCount,
		Preview:    preview,
	}
}
