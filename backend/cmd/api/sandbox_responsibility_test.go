// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/engine"
	"github.com/dunkeltech/botbox/backend/internal/gen/db"
)

func TestSandboxResponsibilityReachesEveryTurn(t *testing.T) {
	for _, tt := range []struct {
		name, external, continuation, preface, role string
	}{
		{name: "new direct message"},
		{name: "resumed direct message", external: "existing-session"},
		{name: "new group", preface: "Current group transcript."},
		{name: "resumed group", external: "existing-session", preface: "Current group transcript."},
		{name: "continuation", continuation: "Continue after secret fulfillment."},
		{name: "resumed continuation", external: "existing-session", continuation: "Continue after secret fulfillment."},
		{name: "global role", role: "Review architecture proposals."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			session := engine.Session{
				Bot:                db.Bot{Name: "Yoda", Description: tt.role, ReplyLanguage: "de"},
				Messages:           []db.Message{{Content: "Continue your work."}},
				Preface:            tt.preface,
				ContinuationPrompt: tt.continuation,
			}
			// API turns use the same identity prompt as fresh and resumed CLI turns.
			prompts := map[string]string{
				"api identity":       botIdentityPrompt(session),
				"cli without broker": cliTurnPrompt(session, tt.external, engine.DesktopGuidanceFor(false)),
				"cli with broker":    cliTurnPrompt(session, tt.external, engine.DesktopGuidanceFor(true)),
			}
			for mode, prompt := range prompts {
				for _, want := range []string{
					"Your sandbox is your own persistent work computer.",
					"Make routine local decisions yourself without asking for confirmation",
					"remove obsolete files you created when you have verified they are no longer needed",
					"check for active references",
					"Prefer recoverable cleanup",
					"Do not delete user-provided files, shared data, or uncommitted work without authorization.",
					"Local housekeeping does not authorize external actions",
					"Continue to obey secret-handling rules, workspace boundaries, network policy, budgets, and human desktop control.",
				} {
					if strings.Count(prompt, want) != 1 {
						t.Errorf("%s: expected responsibility or boundary instruction once: %q", mode, want)
					}
				}
				if mode != "api identity" {
					wantFetch := "To fetch external websites or APIs, use shell tools (such as curl) or the desktop browser tools so requests go through the egress proxy."
					if strings.Count(prompt, wantFetch) != 1 {
						t.Errorf("%s: expected egress fetch guidance once: %q", mode, wantFetch)
					}
				}
			}
		})
	}
}
