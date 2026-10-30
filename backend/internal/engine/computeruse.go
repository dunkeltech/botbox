package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/dunkeltech/botbox/backend/internal/desk"
)

var (
	// ErrStepLimitExceeded indicates a computer-use turn reached MaxSteps without completing.
	ErrStepLimitExceeded = errors.New("computer-use step limit exceeded")
	// ErrScreenshotLimitExceeded indicates a turn captured too many screenshots.
	ErrScreenshotLimitExceeded = errors.New("computer-use screenshot limit exceeded")
	// ErrTurnTokenBudget indicates a computer-use turn spent its token allowance.
	ErrTurnTokenBudget = errors.New("turn token budget exceeded")
	// ErrTakeoverConflict corresponds to HTTP 409 when a human owns the desktop control lock.
	ErrTakeoverConflict = errors.New("desktop control lock held by human")
)

// GitLabCLIGuidance keeps glab token handling outside the sandbox, including resumed CLI sessions.
const GitLabCLIGuidance = " For GitLab API or Git over HTTPS, use request_secret(kind=\"cli_egress\", adapter_id=\"gitlab_api\" or \"gitlab_git\", target_origin=the exact approved HTTPS origin, env_placeholder_name=\"GITLAB_TOKEN\", reason=...). Custom instances must be registered and assigned to this bot; egress permission is separate. When given a GITLAB_TOKEN placeholder, try it with GITLAB_HOST and GITLAB_API_HOST set to that instance and HTTPS protocols. Use glab api user to check REST authentication and git ls-remote to check Git HTTPS access. Request replacement only after an authentication failure. Never request real tokens in chat, run glab auth login with a real token, or put real tokens in files, arguments or environment variables. API/Git token requests do not require the browser broker."

// DesktopGuidance is appended to API and CLI prompts to prefer direct tools and refresh after human use.
const DesktopGuidance = "Desktop tools are available: screenshot, pointer, keyboard, and browser tools (navigate, click_selector, fill, read_text). Prefer shell commands and browser tools over pixel clicks. The desktop is 1280 by 800; coordinates on a downscaled screenshot are scaled back to that desktop. After a human uses the desktop, take a fresh screenshot before acting."

// DesktopGuidanceFor appends isolated broker login guidance when the external
// authentication broker is active on the control plane.
func DesktopGuidanceFor(brokerEnabled bool) string {
	if !brokerEnabled {
		return DesktopGuidance + GitLabCLIGuidance
	}
	return DesktopGuidance + GitLabCLIGuidance + " For website logins on reviewed adapters (odoo_staging, gitlab_web, github_web), never enter credentials into the sandbox browser; call request_secret(kind=\"browser_login\", adapter_id=..., reason=...) and then use broker_open, broker_read, broker_click, and broker_screenshot with the returned broker_session_id."
}

// HandbackNote is the system note sent when a person returns the desktop to the bot.
const HandbackNote = "A human used the desktop; take a fresh screenshot."

// DesktopActionKind identifies the desktop primitive executed in one step.
type DesktopActionKind = string

// Supported desktop action kinds dispatched by ComputerUseRunner.
const (
	ActionScreenshot DesktopActionKind = desk.KindScreenshot
	ActionClick      DesktopActionKind = desk.KindClick
	ActionType       DesktopActionKind = desk.KindType
	ActionKey        DesktopActionKind = desk.KindKey
)

// DesktopAction is one computer-use operation shared with the sandbox agent.
type DesktopAction = desk.Action

// ModelDecision is returned by a ComputerUseModel on each observation step.
type ModelDecision struct {
	Done    bool
	Summary string
	Action  *DesktopAction
}

// DesktopController is satisfied by the gRPC client talking to cmd/agent.
type DesktopController interface {
	Do(ctx context.Context, act DesktopAction) (desk.Result, error)
}

// ComputerUseModel abstracts the LLM computer-use loop so tests use deterministic fakes.
type ComputerUseModel interface {
	NextStep(ctx context.Context, step int, lastScreenshot []byte, executed []DesktopAction) (ModelDecision, error)
}

// ComputerUseRunner runs a bounded screenshot -> action loop with takeover preemption.
type ComputerUseRunner struct {
	MaxSteps       int
	MaxScreenshots int
	IsTakenOver    func() bool
	Desktop        DesktopController
	Model          ComputerUseModel
}

// Run executes steps until the model reports Done or hits a safety limit or human takeover.
func (r *ComputerUseRunner) Run(ctx context.Context) (string, []DesktopAction, error) {
	if r.Desktop == nil || r.Model == nil {
		return "", nil, errors.New("computer-use runner requires Desktop and Model")
	}
	maxSteps := r.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 10
	}
	maxShots := r.MaxScreenshots
	if maxShots <= 0 {
		maxShots = 12
	}

	var executed []DesktopAction
	shots := 0
	for step := 1; step <= maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return "", executed, fmt.Errorf("computer-use loop canceled: %w", err)
		}
		if r.takenOver() {
			return "", executed, ErrTakeoverConflict
		}
		shots++
		if shots > maxShots {
			return "", executed, ErrScreenshotLimitExceeded
		}
		shot, err := r.Desktop.Do(ctx, desk.Action{Kind: desk.KindScreenshot})
		if err != nil {
			return "", executed, fmt.Errorf("capture screenshot on step %d: %w", step, err)
		}

		decision, err := r.Model.NextStep(ctx, step, shot.Image, executed)
		if err != nil {
			return "", executed, fmt.Errorf("model step %d: %w", step, err)
		}
		if decision.Done {
			return decision.Summary, executed, nil
		}
		if decision.Action == nil {
			return "", executed, fmt.Errorf("model step %d returned neither Done nor Action", step)
		}

		// Re-check takeover right before mutating desktop state (maps to HTTP 409 action rejection).
		if r.takenOver() {
			return "", executed, ErrTakeoverConflict
		}

		act := *decision.Action
		if act.Kind == desk.KindScreenshot {
			shots++
			if shots > maxShots {
				return "", executed, ErrScreenshotLimitExceeded
			}
		}
		if _, err := r.Desktop.Do(ctx, act); err != nil {
			return "", executed, fmt.Errorf("execute action %s on step %d: %w", act.Kind, step, err)
		}
		executed = append(executed, act)
	}

	return "", executed, ErrStepLimitExceeded
}

func (r *ComputerUseRunner) takenOver() bool {
	return r.IsTakenOver != nil && r.IsTakenOver()
}
