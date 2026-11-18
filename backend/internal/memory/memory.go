// SPDX-License-Identifier: AGPL-3.0-only

// Package memory implements per-bot persistent knowledge base storage,
// zero-secret write guards, full-text search, and chat/MCP memory tools.
package memory

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// ModeAlways injects the memory entry into every turn's prompt context.
	ModeAlways = "always"
	// ModeOnDemand keeps the entry out of the base prompt until queried via memory_search.
	ModeOnDemand = "on_demand"

	// SourceChat marks entries written during a bot turn.
	SourceChat = "chat"
	// SourceManual marks entries created by a workspace user in Bot Settings.
	SourceManual = "manual"

	// MaxEntriesPerBot caps total stored memories per bot to keep search and management bounded.
	MaxEntriesPerBot = 200
	// AlwaysTokenLimit caps total tokens injected into every turn via always-in-context entries.
	AlwaysTokenLimit = 4000
	// MaxPromptBytes caps serialized prompt text for always-in-context memories inside CLI turn limits.
	MaxPromptBytes = 16000

	// MaxTitleChars limits memory entry titles.
	MaxTitleChars = 160
	// MaxContentChars limits memory entry content.
	MaxContentChars = 4000
	// MaxTags limits tags per entry.
	MaxTags = 12
	// MaxTagChars limits each normalized tag.
	MaxTagChars = 32
)

var (
	// ErrValidation indicates invalid memory fields, tags, or mode.
	ErrValidation = errors.New("invalid memory input")
	// ErrSecretDetected rejects content that matches a secret pattern or stored secret value.
	ErrSecretDetected = errors.New("memory entry cannot contain credentials or secret values")
	// ErrAlwaysBudgetExceeded rejects always-in-context mutations that exceed AlwaysTokenLimit.
	ErrAlwaysBudgetExceeded = errors.New("always-in-context token budget exceeded")
	// ErrLimitReached rejects creating entries beyond MaxEntriesPerBot.
	ErrLimitReached = errors.New("bot knowledge base entry limit reached")
	// ErrAutoSaveDisabled rejects bot-authored writes when memory_auto_save is off.
	ErrAutoSaveDisabled = errors.New("auto-saving memories from chat is disabled for this bot")
	// ErrNotFound indicates the memory entry or bot does not exist in this workspace.
	ErrNotFound = errors.New("memory entry not found")
	// ErrToolUnavailable indicates the memory tool call lacks a valid running bot turn.
	ErrToolUnavailable = errors.New("memory tools require an active bot turn")
	// ErrToolArguments indicates malformed or out-of-range tool arguments.
	ErrToolArguments = errors.New("invalid memory tool arguments")
)

var tagPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// ValidateTitle trims and validates a memory title.
func ValidateTitle(raw string) (string, error) {
	title := strings.TrimSpace(raw)
	if title == "" || strings.ContainsRune(title, 0) || strings.ContainsAny(title, "\r\n") {
		return "", fmt.Errorf("%w: title must be 1..%d single-line characters", ErrValidation, MaxTitleChars)
	}
	if utf8.RuneCountInString(title) > MaxTitleChars {
		return "", fmt.Errorf("%w: title must be at most %d characters", ErrValidation, MaxTitleChars)
	}
	return title, nil
}

// ValidateContent trims and validates memory content.
func ValidateContent(raw string) (string, error) {
	content := strings.TrimSpace(raw)
	if content == "" || strings.ContainsRune(content, 0) {
		return "", fmt.Errorf("%w: content must be 1..%d characters", ErrValidation, MaxContentChars)
	}
	if utf8.RuneCountInString(content) > MaxContentChars {
		return "", fmt.Errorf("%w: content must be at most %d characters", ErrValidation, MaxContentChars)
	}
	return content, nil
}

// ValidateMode ensures mode is either ModeAlways or ModeOnDemand.
func ValidateMode(raw string) (string, error) {
	mode := strings.TrimSpace(raw)
	switch mode {
	case ModeAlways, ModeOnDemand:
		return mode, nil
	default:
		return "", fmt.Errorf("%w: mode must be 'always' or 'on_demand'", ErrValidation)
	}
}

// NormalizeTags cleans, deduplicates, and validates tag names.
func NormalizeTags(raw []string) ([]string, error) {
	if len(raw) > MaxTags {
		return nil, fmt.Errorf("%w: at most %d tags are allowed", ErrValidation, MaxTags)
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		tag := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(item), "#")))
		if tag == "" {
			continue
		}
		if len(tag) > MaxTagChars || !tagPattern.MatchString(tag) {
			return nil, fmt.Errorf("%w: tags must match [a-z0-9][a-z0-9._-]{0,31}", ErrValidation)
		}
		if _, exists := seen[tag]; exists {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out, nil
}

// EstimateTokens computes a deterministic token estimate (~4 runes per token)
// so budget enforcement does not depend on external tokenizers.
func EstimateTokens(title, content string, tags []string) int32 {
	runes := utf8.RuneCountInString(title) + utf8.RuneCountInString(content)
	for _, tag := range tags {
		runes += utf8.RuneCountInString(tag) + 2
	}
	tokens := (runes + 3) / 4
	if tokens < 1 {
		return 1
	}
	return int32(tokens) // #nosec G115 -- total bounded by MaxTitleChars + MaxContentChars + MaxTags
}

// EscapeLike escapes SQL LIKE wildcards so literal search characters stay exact.
func EscapeLike(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(s)
}
