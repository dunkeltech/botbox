// SPDX-License-Identifier: AGPL-3.0-only

package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/desk"
	"github.com/dunkeltech/botbox/backend/internal/engine"
)

type fakeDesktopController struct {
	screenshotBytes []byte
	screenshotErr   error
	actionErr       error
	calls           []string
}

func (f *fakeDesktopController) Do(_ context.Context, act engine.DesktopAction) (desk.Result, error) {
	f.calls = append(f.calls, act.Kind)
	if act.Kind == engine.ActionScreenshot {
		if f.screenshotErr != nil {
			return desk.Result{}, f.screenshotErr
		}
		return desk.Result{Image: f.screenshotBytes}, nil
	}
	if f.actionErr != nil {
		return desk.Result{}, f.actionErr
	}
	return desk.Result{Text: "ok"}, nil
}

type fakeComputerUseModel struct {
	decisions []engine.ModelDecision
	err       error
}

func (f *fakeComputerUseModel) NextStep(_ context.Context, step int, _ []byte, _ []engine.DesktopAction) (engine.ModelDecision, error) {
	if f.err != nil {
		return engine.ModelDecision{}, f.err
	}
	idx := step - 1
	if idx >= len(f.decisions) {
		return engine.ModelDecision{
			Action: &engine.DesktopAction{Kind: engine.ActionClick, X: 10, Y: 10, Button: "left"},
		}, nil
	}
	return f.decisions[idx], nil
}

func TestComputerUseRunner(t *testing.T) {
	t.Parallel()

	t.Run("executes bounded desktop actions until Done", func(t *testing.T) {
		t.Parallel()

		desktop := &fakeDesktopController{screenshotBytes: []byte("png")}
		model := &fakeComputerUseModel{
			decisions: []engine.ModelDecision{
				{Action: &engine.DesktopAction{Kind: engine.ActionClick, X: 400, Y: 300, Button: "left"}},
				{Action: &engine.DesktopAction{Kind: engine.ActionType, Text: "https://example.com"}},
				{Action: &engine.DesktopAction{Kind: engine.ActionKey, Key: "Return"}},
				{Done: true, Summary: "opened example.com"},
			},
		}

		runner := &engine.ComputerUseRunner{
			MaxSteps: 8,
			Desktop:  desktop,
			Model:    model,
		}
		summary, executed, err := runner.Run(context.Background())
		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}
		if summary != "opened example.com" || len(executed) != 3 {
			t.Fatalf("got (%q, %d actions), want (opened example.com, 3)", summary, len(executed))
		}
	})

	t.Run("enforces MaxSteps limit", func(t *testing.T) {
		t.Parallel()

		desktop := &fakeDesktopController{screenshotBytes: []byte("png")}
		model := &fakeComputerUseModel{}
		runner := &engine.ComputerUseRunner{
			MaxSteps: 2,
			Desktop:  desktop,
			Model:    model,
		}
		_, executed, err := runner.Run(context.Background())
		if !errors.Is(err, engine.ErrStepLimitExceeded) {
			t.Fatalf("err = %v, want ErrStepLimitExceeded", err)
		}
		if len(executed) != 2 {
			t.Fatalf("executed = %d actions, want 2", len(executed))
		}
	})

	t.Run("human takeover preempts before mutating action", func(t *testing.T) {
		t.Parallel()

		takenOver := false
		desktop := &fakeDesktopController{screenshotBytes: []byte("png")}
		model := &fakeComputerUseModel{
			decisions: []engine.ModelDecision{
				{Action: &engine.DesktopAction{Kind: engine.ActionClick, X: 10, Y: 10}},
			},
		}

		// Trigger takeover right after model returns by flipping state on Screenshot/NextStep hook.
		modelWithTakeover := &takeoverTriggerModel{
			inner:     model,
			onObserve: func() { takenOver = true },
		}

		runner := &engine.ComputerUseRunner{
			MaxSteps:    5,
			IsTakenOver: func() bool { return takenOver },
			Desktop:     desktop,
			Model:       modelWithTakeover,
		}
		_, _, err := runner.Run(context.Background())
		if !errors.Is(err, engine.ErrTakeoverConflict) {
			t.Fatalf("err = %v, want ErrTakeoverConflict", err)
		}
		if len(desktop.calls) != 1 || desktop.calls[0] != "screenshot" {
			t.Fatalf("desktop calls = %v, want only initial screenshot before preemption", desktop.calls)
		}
	})

	t.Run("rejects nil dependencies and propagates canceled context or errors", func(t *testing.T) {
		t.Parallel()

		empty := &engine.ComputerUseRunner{}
		if _, _, err := empty.Run(context.Background()); err == nil {
			t.Fatal("expected error for missing dependencies")
		}

		canceledCtx, cancel := context.WithCancel(context.Background())
		cancel()
		runner := &engine.ComputerUseRunner{
			Desktop: &fakeDesktopController{screenshotBytes: []byte("png")},
			Model:   &fakeComputerUseModel{},
		}
		if _, _, err := runner.Run(canceledCtx); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
}

type takeoverTriggerModel struct {
	inner     engine.ComputerUseModel
	onObserve func()
}

func (m *takeoverTriggerModel) NextStep(ctx context.Context, step int, shot []byte, executed []engine.DesktopAction) (engine.ModelDecision, error) {
	dec, err := m.inner.NextStep(ctx, step, shot, executed)
	if m.onObserve != nil {
		m.onObserve()
	}
	return dec, err
}
