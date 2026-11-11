// Package guardrails implements workspace-scoped LLM proxy guardrail policies,
// deterministic built-in and custom detectors, sliding-window SSE filtering,
// and privacy-preserving audit and chat feedback.
package guardrails

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Directions supported by guardrail detectors and custom rules.
const (
	DirectionInput  = "input"
	DirectionOutput = "output"
	DirectionBoth   = "both"
)

// Actions supported when a guardrail rule matches.
const (
	ActionAllowed  = "allowed"
	ActionAudit    = "audit"
	ActionAudited  = "audited"
	ActionRedact   = "redact"
	ActionRedacted = "redacted"
	ActionBlock    = "block"
	ActionBlocked  = "blocked"
)

// Failure modes when guardrail policy lookup or evaluation fails.
const (
	FailureClosed = "fail_closed"
	FailureOpen   = "fail_open"
)

// Custom rule match modes.
const (
	MatchModeRegex    = "regex"
	MatchModeKeywords = "keywords"
)

// Built-in detector slugs recorded in guardrail events and chat notices.
const (
	SlugSecrets         = "secrets-leak-guard"
	SlugPII             = "pii-protection"
	SlugPromptInjection = "prompt-injection"
)

// Supported PII entity types.
const (
	EntityEmail = "email"
	EntityIBAN  = "iban"
	EntityCard  = "card"
	EntityPhone = "phone"
)

// Bounded limits for workspace custom rules.
const (
	MaxCustomRules     = 100
	MaxPatternLen      = 2000
	MaxKeywordsPerRule = 50
)

// Sentinel errors returned by guardrail validation and streaming filters.
var (
	ErrValidation    = errors.New("invalid guardrail configuration")
	ErrRuleNotFound  = errors.New("guardrail rule not found")
	ErrRuleConflict  = errors.New("guardrail rule slug already exists")
	ErrRuleLimit     = errors.New("workspace guardrail rule limit reached")
	ErrStreamBlocked = errors.New("llm stream blocked by workspace guardrail")
)

// CoveredProviders lists all llmproxy provider routes protected by workspace guardrails.
var CoveredProviders = []string{
	"anthropic",
	"openai",
	"openai-plan",
	"openrouter",
	"google",
}

// DetectorConfig describes one managed built-in detector's settings.
type DetectorConfig struct {
	Enabled   bool   `json:"enabled"`
	Direction string `json:"direction"`
	Action    string `json:"action"`
}

// PIIDetectorConfig extends DetectorConfig with selectable PII entity categories.
type PIIDetectorConfig struct {
	Enabled   bool     `json:"enabled"`
	Direction string   `json:"direction"`
	Action    string   `json:"action"`
	Entities  []string `json:"entities"`
}

// Policy represents the workspace-wide LLM guardrail configuration.
type Policy struct {
	WorkspaceID     uuid.UUID         `json:"workspaceId"`
	Enabled         bool              `json:"enabled"`
	FailureMode     string            `json:"failureMode"`
	Secrets         DetectorConfig    `json:"secrets"`
	PII             PIIDetectorConfig `json:"pii"`
	PromptInjection DetectorConfig    `json:"promptInjection"`
	Revision        int64             `json:"revision"`
	UpdatedAt       time.Time         `json:"updatedAt"`
}

// CustomRule defines a user-managed RE2 regex or keyword rule.
type CustomRule struct {
	ID               uuid.UUID `json:"id"`
	WorkspaceID      uuid.UUID `json:"workspaceId"`
	Slug             string    `json:"slug"`
	Name             string    `json:"name"`
	MatchMode        string    `json:"matchMode"`
	Pattern          string    `json:"pattern"`
	Direction        string    `json:"direction"`
	Action           string    `json:"action"`
	ReplacementToken string    `json:"replacementToken"`
	Enabled          bool      `json:"enabled"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// DefaultPolicy returns the opt-in initial guardrail configuration for a workspace
// so operators can enable enforcement with pre-tuned detector defaults in one click.
func DefaultPolicy(workspaceID uuid.UUID) Policy {
	return Policy{
		WorkspaceID: workspaceID,
		Enabled:     false,
		FailureMode: FailureClosed,
		Secrets: DetectorConfig{
			Enabled:   true,
			Direction: DirectionBoth,
			Action:    ActionBlock,
		},
		PII: PIIDetectorConfig{
			Enabled:   true,
			Direction: DirectionBoth,
			Action:    ActionRedact,
			Entities:  []string{EntityEmail, EntityIBAN, EntityCard, EntityPhone},
		},
		PromptInjection: DetectorConfig{
			Enabled:   true,
			Direction: DirectionInput,
			Action:    ActionAudit,
		},
		Revision: 1,
	}
}

// ChatGuardrailMetadata stores sanitized guardrail intervention details inside
// messages.metadata for kind='guardrail' and kind='tool' trace rows.
// It never stores matched plaintext values.
type ChatGuardrailMetadata struct {
	EventID          uuid.UUID `json:"eventId"`
	TurnID           string    `json:"turnId,omitempty"`
	Direction        string    `json:"direction"`
	Action           string    `json:"action"`
	Provider         string    `json:"provider"`
	Model            string    `json:"model,omitempty"`
	RoutePath        string    `json:"routePath,omitempty"`
	StatusCode       int       `json:"statusCode"`
	LatencyMs        int       `json:"latencyMs"`
	RuleSlugs        []string  `json:"ruleSlugs"`
	MatchLabels      []string  `json:"matchLabels"`
	MatchCount       int       `json:"matchCount"`
	Summary          string    `json:"summary"`
	ReplacementToken string    `json:"replacementToken,omitempty"`
}

// ToolGuardrailMetadata wraps ChatGuardrailMetadata in the format parsed by
// parseToolCallMeta in internal/api/chat.go.
type ToolGuardrailMetadata struct {
	Kind      string                `json:"Kind"`
	TurnID    string                `json:"TurnID,omitempty"`
	ToolID    string                `json:"ToolID,omitempty"`
	IsError   bool                  `json:"IsError"`
	ErrorCode string                `json:"ErrorCode,omitempty"`
	Guardrail ChatGuardrailMetadata `json:"Guardrail"`
}

// EvalResult captures the outcome of evaluating a text or JSON payload.
type EvalResult struct {
	Action      string
	RuleSlugs   []string
	MatchLabels []string
	MatchCount  int
	MaxScore    float64
	Summary     string
}

// TestResult captures the stateless Quick Test result for a custom rule draft.
type TestResult struct {
	Valid      bool   `json:"valid"`
	MatchCount int    `json:"matchCount"`
	Preview    string `json:"preview"`
	Error      string `json:"error,omitempty"`
}
