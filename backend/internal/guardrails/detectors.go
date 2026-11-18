// SPDX-License-Identifier: AGPL-3.0-only

package guardrails

import (
	"math/big"
	"regexp"
	"strings"
	"unicode"
)

type staticDetectorPattern struct {
	label string
	re    *regexp.Regexp
}

var secretDetectorPatterns = []staticDetectorPattern{
	{label: "private_key", re: regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----|-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)},
	{label: "api_key", re: regexp.MustCompile(`\bsk-(?:ant|proj)-[A-Za-z0-9_-]{16,}\b`)},
	{label: "api_key", re: regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}\b`)},
	{label: "api_key", re: regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}\b`)},
	{label: "api_key", re: regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`)},
	{label: "api_key", re: regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{16,}\b`)},
	{label: "api_key", re: regexp.MustCompile(`\bxox[bpoas]-[A-Za-z0-9-]{16,}\b`)},
	{label: "api_key", re: regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{label: "api_key", re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{20,}\b`)},
	{label: "api_key", re: regexp.MustCompile(`\bnpm_[A-Za-z0-9]{20,}\b`)},
	{label: "api_key", re: regexp.MustCompile(`\bpypi-[A-Za-z0-9_-]{20,}\b`)},
	{label: "jwt", re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)},
	{label: "auth_header", re: regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/-]{20,}=*\b`)},
	{label: "auth_header", re: regexp.MustCompile(`(?i)\bBasic\s+[A-Za-z0-9+/]{16,}={0,2}\b`)},
	{label: "db_uri", re: regexp.MustCompile(`(?:postgres(?:ql)?|mysql|redis|rediss|mongodb(?:\+srv)?|https?)://[^/\s:@]+:[^/\s@]+@[^\s"'` + "`" + `<>]+`)},
}

var (
	emailPattern = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)
	ibanPattern  = regexp.MustCompile(`\b[A-Z]{2}[0-9]{2}(?:[ ]?[A-Z0-9]{4}){2,7}(?:[ ]?[A-Z0-9]{1,4})?\b`)
	cardPattern  = regexp.MustCompile(`\b(?:\d[ -]?){13,19}\b`)
	phonePattern = regexp.MustCompile(`(?:\+\d{1,3}[ -]?(?:\(\d{1,4}\)[ -]?)?(?:\d[ -]?){6,14}\d|\(\d{3}\)[ -]?\d{3}[ -]?\d{4})`)
)

type weightedSignal struct {
	weight float64
	re     *regexp.Regexp
}

var injectionSignals = []weightedSignal{
	{weight: 0.70, re: regexp.MustCompile(`(?i)\bignore\s+(?:all\s+|any\s+)?(?:previous|prior|above)\s+instructions?\b`)},
	{weight: 0.70, re: regexp.MustCompile(`(?i)\bdisregard\s+(?:all\s+|your\s+)?(?:previous|system)\s+(?:instructions?|rules?|prompts?)\b`)},
	{weight: 0.75, re: regexp.MustCompile(`(?i)(?:<\|im_start\|>system|<\|system\|>|###\s*SYSTEM\s*:|\[SYSTEM\s+OVERRIDE\])`)},
	{weight: 0.65, re: regexp.MustCompile(`(?i)\b(?:DAN\s+mode|developer\s+mode\s+override|jailbreak\s+mode)\b`)},
	{weight: 0.65, re: regexp.MustCompile(`(?i)\b(?:reveal|dump|print|exfiltrate)\s+(?:your\s+)?(?:system\s+prompt|hidden\s+instructions?|environment\s+variables?|api\s+keys?)\b`)},
	{weight: 0.35, re: regexp.MustCompile(`(?i)\byou\s+are\s+now\s+(?:unrestricted|unfiltered|an?\s+admin)\b`)},
	{weight: 0.35, re: regexp.MustCompile(`(?i)\bdo\s+not\s+follow\s+(?:the\s+)?(?:safety|security|guardrail)\b`)},
}

const minStoredSecretMatchLen = 8

type spanMatch struct {
	start       int
	end         int
	ruleSlug    string
	label       string
	action      string
	replacement string
	score       float64
}

func scanSecrets(text, action string, storedSecrets []string) []spanMatch {
	var out []spanMatch
	for _, pat := range secretDetectorPatterns {
		locs := pat.re.FindAllStringIndex(text, -1)
		for _, loc := range locs {
			out = append(out, spanMatch{
				start:       loc[0],
				end:         loc[1],
				ruleSlug:    SlugSecrets,
				label:       pat.label,
				action:      action,
				replacement: "[REDACTED:SECRET]",
			})
		}
	}
	for _, secretVal := range storedSecrets {
		if len(secretVal) < minStoredSecretMatchLen || strings.Contains(secretVal, "botbox-placeholder") {
			continue
		}
		offset := 0
		for offset < len(text) {
			idx := strings.Index(text[offset:], secretVal)
			if idx < 0 {
				break
			}
			start := offset + idx
			end := start + len(secretVal)
			out = append(out, spanMatch{
				start:       start,
				end:         end,
				ruleSlug:    SlugSecrets,
				label:       "stored_secret",
				action:      action,
				replacement: "[REDACTED:SECRET]",
			})
			offset = end
		}
	}
	return out
}

func scanPII(text, action string, entities map[string]bool) []spanMatch {
	var out []spanMatch
	if entities[EntityEmail] {
		for _, loc := range emailPattern.FindAllStringIndex(text, -1) {
			out = append(out, spanMatch{
				start:       loc[0],
				end:         loc[1],
				ruleSlug:    SlugPII,
				label:       EntityEmail,
				action:      action,
				replacement: "[REDACTED:EMAIL]",
			})
		}
	}
	if entities[EntityIBAN] {
		for _, loc := range ibanPattern.FindAllStringIndex(text, -1) {
			candidate := text[loc[0]:loc[1]]
			if validIBAN(candidate) {
				out = append(out, spanMatch{
					start:       loc[0],
					end:         loc[1],
					ruleSlug:    SlugPII,
					label:       EntityIBAN,
					action:      action,
					replacement: "[REDACTED:IBAN]",
				})
			}
		}
	}
	if entities[EntityCard] {
		for _, loc := range cardPattern.FindAllStringIndex(text, -1) {
			candidate := strings.TrimSpace(text[loc[0]:loc[1]])
			if validLuhnCard(candidate) {
				// Adjust span to exclude any trailing delimiter matched by `\b`.
				trimmedEnd := loc[0] + len(strings.TrimRight(text[loc[0]:loc[1]], " -"))
				out = append(out, spanMatch{
					start:       loc[0],
					end:         trimmedEnd,
					ruleSlug:    SlugPII,
					label:       EntityCard,
					action:      action,
					replacement: "[REDACTED:CARD]",
				})
			}
		}
	}
	if entities[EntityPhone] {
		for _, loc := range phonePattern.FindAllStringIndex(text, -1) {
			digits := countDigits(text[loc[0]:loc[1]])
			if digits >= 8 && digits <= 15 {
				out = append(out, spanMatch{
					start:       loc[0],
					end:         loc[1],
					ruleSlug:    SlugPII,
					label:       EntityPhone,
					action:      action,
					replacement: "[REDACTED:PHONE]",
				})
			}
		}
	}
	return out
}

func scanPromptInjection(text, action string) ([]spanMatch, float64) {
	var score float64
	var matches []spanMatch
	for _, sig := range injectionSignals {
		locs := sig.re.FindAllStringIndex(text, -1)
		if len(locs) == 0 {
			continue
		}
		score += sig.weight
		for _, loc := range locs {
			matches = append(matches, spanMatch{
				start:       loc[0],
				end:         loc[1],
				ruleSlug:    SlugPromptInjection,
				label:       "prompt_injection",
				action:      action,
				replacement: "[REDACTED:INJECTION]",
			})
		}
	}
	if score > 1.0 {
		score = 1.0
	}
	if score < 0.65 {
		return nil, score
	}
	for i := range matches {
		matches[i].score = score
	}
	return matches, score
}

func countDigits(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsDigit(r) {
			n++
		}
	}
	return n
}

// validIBAN validates ISO 13616 length (15..34) and mod-97 checksum == 1.
func validIBAN(raw string) bool {
	cleaned := strings.ToUpper(strings.ReplaceAll(raw, " ", ""))
	if len(cleaned) < 15 || len(cleaned) > 34 {
		return false
	}
	rearranged := cleaned[4:] + cleaned[:4]
	var digits strings.Builder
	digits.Grow(len(rearranged) * 2)
	for i := 0; i < len(rearranged); i++ {
		c := rearranged[i]
		switch {
		case c >= '0' && c <= '9':
			digits.WriteByte(c)
		case c >= 'A' && c <= 'Z':
			val := int(c-'A') + 10
			digits.WriteByte(byte('0' + val/10))
			digits.WriteByte(byte('0' + val%10))
		default:
			return false
		}
	}
	num, ok := new(big.Int).SetString(digits.String(), 10)
	if !ok {
		return false
	}
	mod := new(big.Int).Mod(num, big.NewInt(97))
	return mod.Int64() == 1
}

// validLuhnCard checks that a candidate card number has 13..19 digits and passes Luhn.
func validLuhnCard(raw string) bool {
	var digits [19]int
	n := 0
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c >= '0' && c <= '9' {
			if n >= len(digits) {
				return false
			}
			digits[n] = int(c - '0')
			n++
		} else if c != ' ' && c != '-' {
			return false
		}
	}
	if n < 13 || n > 19 {
		return false
	}
	sum := 0
	alt := false
	for i := n - 1; i >= 0; i-- {
		d := digits[i]
		if alt {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
	}
	return sum%10 == 0
}
