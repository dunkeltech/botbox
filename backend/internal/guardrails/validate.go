package guardrails

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	slugPattern             = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$`)
	replacementTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_:\[\]-]{1,80}$`)
)

// ValidateSlug checks that a guardrail rule slug is 2..64 lowercase alphanumeric or hyphen chars.
func ValidateSlug(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if !slugPattern.MatchString(s) {
		return "", fmt.Errorf("%w: slug must be 2..64 lowercase letters, digits, or hyphens", ErrValidation)
	}
	return s, nil
}

// ValidateName checks that a custom rule name is 1..100 characters without control bytes.
func ValidateName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if len(name) == 0 || len(name) > 100 || strings.ContainsAny(name, "\r\n\x00") {
		return "", fmt.Errorf("%w: name must be 1..100 characters", ErrValidation)
	}
	return name, nil
}

// ValidateDirection checks that direction is input, output, or both.
func ValidateDirection(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case DirectionInput, DirectionOutput, DirectionBoth:
		return strings.TrimSpace(raw), nil
	default:
		return "", fmt.Errorf("%w: direction must be input, output, or both", ErrValidation)
	}
}

// ValidateAction checks that action is block, redact, or audit.
func ValidateAction(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case ActionBlock, ActionRedact, ActionAudit:
		return strings.TrimSpace(raw), nil
	default:
		return "", fmt.Errorf("%w: action must be block, redact, or audit", ErrValidation)
	}
}

// ValidateFailureMode checks that failureMode is fail_closed or fail_open.
func ValidateFailureMode(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case FailureClosed, FailureOpen:
		return strings.TrimSpace(raw), nil
	default:
		return "", fmt.Errorf("%w: failureMode must be fail_closed or fail_open", ErrValidation)
	}
}

// ValidateMatchMode checks that matchMode is regex or keywords.
func ValidateMatchMode(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case MatchModeRegex, MatchModeKeywords:
		return strings.TrimSpace(raw), nil
	default:
		return "", fmt.Errorf("%w: matchMode must be regex or keywords", ErrValidation)
	}
}

// ValidatePIIEntities normalizes and validates selectable PII entity identifiers.
func ValidatePIIEntities(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return []string{}, nil
	}
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		e := strings.TrimSpace(strings.ToLower(entry))
		switch e {
		case EntityEmail, EntityIBAN, EntityCard, EntityPhone:
			if !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		default:
			return nil, fmt.Errorf("%w: unsupported PII entity %q", ErrValidation, entry)
		}
	}
	return out, nil
}

// ValidateReplacementToken validates a mask placeholder token for redact rules.
func ValidateReplacementToken(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "[REDACTED]", nil
	}
	if !replacementTokenPattern.MatchString(trimmed) {
		return "", fmt.Errorf("%w: replacementToken must be 1..80 characters of [A-Za-z0-9_:[\\]-]", ErrValidation)
	}
	return trimmed, nil
}

// ValidatePattern validates and compiles either an RE2 regex or a keyword list.
func ValidatePattern(mode, raw string) (string, *regexp.Regexp, []string, error) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) == 0 || len(trimmed) > MaxPatternLen || strings.ContainsRune(trimmed, '\x00') {
		return "", nil, nil, fmt.Errorf("%w: pattern must be 1..%d characters", ErrValidation, MaxPatternLen)
	}
	switch mode {
	case MatchModeRegex:
		re, err := regexp.Compile(trimmed)
		if err != nil {
			return "", nil, nil, fmt.Errorf("%w: invalid regular expression: %s", ErrValidation, err.Error())
		}
		// Reject empty-string-matching regexes so rules cannot infinitely match zero width.
		if re.MatchString("") {
			return "", nil, nil, fmt.Errorf("%w: regular expression must not match empty strings", ErrValidation)
		}
		return trimmed, re, nil, nil
	case MatchModeKeywords:
		keywords, err := parseKeywords(trimmed)
		if err != nil {
			return "", nil, nil, err
		}
		return trimmed, nil, keywords, nil
	default:
		return "", nil, nil, fmt.Errorf("%w: unsupported matchMode", ErrValidation)
	}
}

func parseKeywords(raw string) ([]string, error) {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	seen := make(map[string]bool, len(parts))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		kw := strings.TrimSpace(p)
		if kw == "" {
			continue
		}
		lower := strings.ToLower(kw)
		if !seen[lower] {
			seen[lower] = true
			out = append(out, kw)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: at least one non-empty keyword is required", ErrValidation)
	}
	if len(out) > MaxKeywordsPerRule {
		return nil, fmt.Errorf("%w: at most %d keywords are allowed per rule", ErrValidation, MaxKeywordsPerRule)
	}
	return out, nil
}

// ActiveRuleCount counts enabled managed detectors plus enabled custom rules.
func ActiveRuleCount(policy Policy, rules []CustomRule) int {
	if !policy.Enabled {
		return 0
	}
	count := 0
	if policy.Secrets.Enabled {
		count++
	}
	if policy.PII.Enabled && len(policy.PII.Entities) > 0 {
		count++
	}
	if policy.PromptInjection.Enabled {
		count++
	}
	for _, rule := range rules {
		if rule.Enabled {
			count++
		}
	}
	return count
}
