// SPDX-License-Identifier: AGPL-3.0-only

package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestValidationHelpers(t *testing.T) {
	t.Parallel()

	t.Run("ValidateTitle", func(t *testing.T) {
		t.Parallel()
		got, err := ValidateTitle("  Deploy rule  ")
		if err != nil || got != "Deploy rule" {
			t.Fatalf("got (%q, %v), want (%q, nil)", got, err, "Deploy rule")
		}
		for _, bad := range []string{
			"",
			"   ",
			"line1\nline2",
			"line1\rline2",
			"null\x00byte",
			strings.Repeat("a", MaxTitleChars+1),
		} {
			if _, err := ValidateTitle(bad); !errors.Is(err, ErrValidation) {
				t.Fatalf("ValidateTitle(%q) err = %v, want ErrValidation", bad, err)
			}
		}
	})

	t.Run("ValidateContent", func(t *testing.T) {
		t.Parallel()
		got, err := ValidateContent("\nRun tests before commit.\n")
		if err != nil || got != "Run tests before commit." {
			t.Fatalf("got (%q, %v), want (%q, nil)", got, err, "Run tests before commit.")
		}
		for _, bad := range []string{
			"",
			"   \t  ",
			"bad\x00value",
			strings.Repeat("x", MaxContentChars+1),
		} {
			if _, err := ValidateContent(bad); !errors.Is(err, ErrValidation) {
				t.Fatalf("ValidateContent(%q) err = %v, want ErrValidation", bad, err)
			}
		}
	})

	t.Run("ValidateMode", func(t *testing.T) {
		t.Parallel()
		for _, good := range []string{"always", " on_demand "} {
			if _, err := ValidateMode(good); err != nil {
				t.Fatalf("ValidateMode(%q) unexpected err: %v", good, err)
			}
		}
		for _, bad := range []string{"", "never", "ALWAYS"} {
			if _, err := ValidateMode(bad); !errors.Is(err, ErrValidation) {
				t.Fatalf("ValidateMode(%q) err = %v, want ErrValidation", bad, err)
			}
		}
	})

	t.Run("NormalizeTags", func(t *testing.T) {
		t.Parallel()
		tags, err := NormalizeTags([]string{" #Git ", "CI.CD", "git", "", "v1_release"})
		if err != nil {
			t.Fatalf("NormalizeTags err: %v", err)
		}
		if strings.Join(tags, ",") != "git,ci.cd,v1_release" {
			t.Fatalf("tags = %v, want [git ci.cd v1_release]", tags)
		}

		tooMany := make([]string, MaxTags+1)
		for i := range tooMany {
			tooMany[i] = "tag"
		}
		if _, err := NormalizeTags(tooMany); !errors.Is(err, ErrValidation) {
			t.Fatalf("tooMany tags err = %v, want ErrValidation", err)
		}

		for _, bad := range [][]string{{"bad tag"}, {"-leading"}, {strings.Repeat("t", MaxTagChars+1)}, {"no/slash"}} {
			if _, err := NormalizeTags(bad); !errors.Is(err, ErrValidation) {
				t.Fatalf("NormalizeTags(%v) err = %v, want ErrValidation", bad, err)
			}
		}
	})

	t.Run("EstimateTokensAndEscapeLike", func(t *testing.T) {
		t.Parallel()
		if got := EstimateTokens("a", "b", nil); got != 1 {
			t.Fatalf("minimum EstimateTokens = %d, want 1", got)
		}
		if got := EstimateTokens("Title", strings.Repeat("word ", 20), []string{"release"}); got < 25 {
			t.Fatalf("EstimateTokens unexpectedly low: %d", got)
		}
		if got := EscapeLike(`100%_done\now`); got != `100\%\_done\\now` {
			t.Fatalf("EscapeLike = %q", got)
		}
	})
}

func TestCheckZeroSecretStaticPatterns(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	wsID := uuid.New()
	botID := uuid.New()

	secrets := []string{
		"sk-ant-api03-1234567890abcdef",
		"ghp_1234567890abcdefghij",
		"glpat-abcdef1234567890",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIE...",
		"Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
		"https://deploy:supersecret@git.example.com/repo.git",
	}
	for _, secret := range secrets {
		err := CheckZeroSecret(ctx, nil, nil, wsID, botID, "note", secret)
		if !errors.Is(err, ErrSecretDetected) {
			t.Fatalf("expected ErrSecretDetected for pattern %q, got %v", secret, err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error echoed secret text: %q", err.Error())
		}
	}

	safe := []string{
		"Always use squashed commits on release branches.",
		"Run `make lint test` inside devenv before opening PRs.",
		"Use the injected GITLAB_TOKEN secret rather than hardcoding credentials.",
	}
	if err := CheckZeroSecret(ctx, nil, nil, wsID, botID, safe...); err != nil {
		t.Fatalf("safe text triggered secret guard: %v", err)
	}
}

func TestToolDefinitionsAndSanitization(t *testing.T) {
	t.Parallel()

	apiDefs := ToolDefinitions(false)
	cliDefs := ToolDefinitions(true)
	if len(apiDefs) != 4 || len(cliDefs) != 4 {
		t.Fatalf("unexpected tool definition count: api=%d cli=%d", len(apiDefs), len(cliDefs))
	}
	for _, def := range apiDefs {
		req := def.InputSchema["required"].([]string)
		for _, r := range req {
			if r == "turn_id" {
				t.Fatalf("API tool %s should not require turn_id", def.Name)
			}
		}
	}
	for _, def := range cliDefs {
		req := def.InputSchema["required"].([]string)
		found := false
		for _, r := range req {
			if r == "turn_id" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("CLI tool %s must require turn_id", def.Name)
		}
	}

	for _, name := range []string{
		"memory_save",
		"mcp__botbox_memory__memory_save",
		"mcp__botbox-memory__memory-search",
		"botbox_memory.memory_update",
		"memory_delete",
	} {
		if !IsMemoryTool(name) {
			t.Fatalf("IsMemoryTool(%q) = false, want true", name)
		}
	}
	if IsMemoryTool("run_command") {
		t.Fatal("IsMemoryTool(run_command) = true, want false")
	}

	if got := ToolResult(`{"ok":true}`, nil); got != `{"ok":true}` {
		t.Fatalf("ToolResult success = %q", got)
	}
	if got := ToolResult("", ErrSecretDetected); got != ErrSecretDetected.Error() {
		t.Fatalf("ToolResult domain err = %q", got)
	}
	if got := ToolResult("", errors.New("pq: internal connection string leak")); got != "memory tool temporarily unavailable" {
		t.Fatalf("ToolResult leaked internal error: %q", got)
	}
}
