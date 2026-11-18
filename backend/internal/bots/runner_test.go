// SPDX-License-Identifier: AGPL-3.0-only

package bots

import (
	"strings"
	"testing"
)

func TestRunnerForProvider(t *testing.T) {
	for _, tt := range []struct{ stored, cli, upstream string }{
		{"openrouter", "pi", "openrouter"},
		{"openai", "codex", "openai"}, {"codex", "codex", "openai"},
		{"anthropic", "claude", "anthropic"}, {"claude", "claude", "anthropic"},
		{"google", "agy", "google"}, {"agy", "agy", "google"},
		{"pi", "", ""}, {"", "", ""},
	} {
		if cli, upstream := RunnerForProvider(tt.stored); cli != tt.cli || upstream != tt.upstream {
			t.Errorf("provider %q resolved to %q/%q", tt.stored, cli, upstream)
		}
	}
}

func TestValidOpenRouterModel(t *testing.T) {
	for _, tt := range []struct {
		model string
		valid bool
	}{
		{"openrouter/auto", true},
		{"vendor/model-1.2:free", true},
		{"~vendor/model-latest", true},
		{"", false},
		{"model", false},
		{"-vendor/model", false},
		{"vendor/another/model", false},
		{"vendor/model with spaces", false},
		{"vendor/" + strings.Repeat("x", 100), false},
	} {
		if ValidOpenRouterModel(tt.model) != tt.valid {
			t.Errorf("model %q valid=%v, want %v", tt.model, !tt.valid, tt.valid)
		}
	}
}
