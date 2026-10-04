package engine

import (
	"context"
	"errors"
	"fmt"
)

var (
	// ErrStepLimitExceeded indicates a computer-use turn reached MaxSteps without completing.
	ErrStepLimitExceeded = errors.New("computer-use step limit exceeded")
	// ErrTakeoverConflict corresponds to HTTP 409 when a human owns the desktop control lock.
	ErrTakeoverConflict = errors.New("desktop control lock held by human")
)

// DesktopActionKind identifies the desktop primitive executed in one step.
type DesktopActionKind string

// Supported desktop action kinds dispatched by ComputerUseRunner.
const (
	ActionScreenshot DesktopActionKind = "screenshot"
	ActionClick      DesktopActionKind = "click"
	ActionType       DesktopActionKind = "type"
	ActionKey        DesktopActionKind = "key"
)

// DesktopAction describes one action chosen by the Anthropic computer-use adapter.
type DesktopAction struct {
	Kind   DesktopActionKind
	X      int32
	Y      int32
	Button string
	Text   string
	Key    string
}

// ModelDecision is returned by a ComputerUseModel on each observation step.
type ModelDecision struct {
	Done    bool
	Summary string
	Action  *DesktopAction
}

// DesktopController is satisfied by the gRPC client talking to cmd/agent.
type DesktopController interface {
	Screenshot(ctx context.Context) ([]byte, error)
	Click(ctx context.Context, x, y int32, button string) error
	Type(ctx context.Context, text string) error
	Key(ctx context.Context, key string) error
}

// ComputerUseModel abstracts the LLM computer-use loop so tests use deterministic fakes.
type ComputerUseModel interface {
	NextStep(ctx context.Context, step int, lastScreenshot []byte, executed []DesktopAction) (ModelDecision, error)
}

// ComputerUseRunner runs a bounded screenshot -> action loop with takeover preemption.
type ComputerUseRunner struct {
	MaxSteps    int
	IsTakenOver func() bool
	Desktop     DesktopController
	Model       ComputerUseModel
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

	var executed []DesktopAction
	for step := 1; step <= maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return "", executed, fmt.Errorf("computer-use loop canceled: %w", err)
		}
		if r.IsTakenOver != nil && r.IsTakenOver() {
			return "", executed, ErrTakeoverConflict
		}

		shot, err := r.Desktop.Screenshot(ctx)
		if err != nil {
			return "", executed, fmt.Errorf("capture screenshot on step %d: %w", step, err)
		}

		decision, err := r.Model.NextStep(ctx, step, shot, executed)
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
		if r.IsTakenOver != nil && r.IsTakenOver() {
			return "", executed, ErrTakeoverConflict
		}

		act := *decision.Action
		if err := r.dispatchAction(ctx, act); err != nil {
			return "", executed, fmt.Errorf("execute action %s on step %d: %w", act.Kind, step, err)
		}
		executed = append(executed, act)
	}

	return "", executed, ErrStepLimitExceeded
}

func (r *ComputerUseRunner) dispatchAction(ctx context.Context, act DesktopAction) error {
	switch act.Kind {
	case ActionScreenshot:
		_, err := r.Desktop.Screenshot(ctx)
		return err
	case ActionClick:
		return r.Desktop.Click(ctx, act.X, act.Y, act.Button)
	case ActionType:
		return r.Desktop.Type(ctx, act.Text)
	case ActionKey:
		return r.Desktop.Key(ctx, act.Key)
	default:
		return fmt.Errorf("unsupported desktop action %q", act.Kind)
	}
}
