// SPDX-License-Identifier: AGPL-3.0-only

package agent_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/agent"
)

type recordedCmd struct {
	name string
	args []string
}

type fakeCmdRunner struct {
	mu    sync.Mutex
	calls []recordedCmd
	out   []byte
	err   error
}

func (f *fakeCmdRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, recordedCmd{
		name: name,
		args: slices.Clone(args),
	})
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

func TestDesktopScreenshot(t *testing.T) {
	t.Parallel()

	pngSample := []byte("\x89PNG\r\n\x1a\nfake")

	tests := []struct {
		name      string
		cancelCtx bool
		runner    *fakeCmdRunner
		wantErr   bool
	}{
		{
			name:   "returns PNG bytes and 1280x800 dimensions",
			runner: &fakeCmdRunner{out: pngSample},
		},
		{
			name:    "rejects empty scrot output",
			runner:  &fakeCmdRunner{out: nil},
			wantErr: true,
		},
		{
			name:    "propagates scrot failure",
			runner:  &fakeCmdRunner{err: errors.New("display unavailable")},
			wantErr: true,
		},
		{
			name:      "canceled context stops before running scrot",
			cancelCtx: true,
			runner:    &fakeCmdRunner{out: pngSample},
			wantErr:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			if tc.cancelCtx {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			svc := agent.NewDesktopService(tc.runner)
			got, w, h, err := svc.Screenshot(ctx)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !slices.Equal(got, pngSample) || w != agent.DesktopWidth || h != agent.DesktopHeight {
				t.Fatalf("got (%v, %d, %d), want (%v, %d, %d)", got, w, h, pngSample, agent.DesktopWidth, agent.DesktopHeight)
			}
			if len(tc.runner.calls) != 1 || tc.runner.calls[0].name != "scrot" {
				t.Fatalf("unexpected calls: %+v", tc.runner.calls)
			}
		})
	}
}

func TestDesktopClick(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		x           int32
		y           int32
		button      string
		cancelCtx   bool
		runnerErr   error
		wantArgs    []string
		wantInvalid bool
		wantErr     bool
	}{
		{
			name:     "default empty button maps to left click",
			x:        120,
			y:        340,
			wantArgs: []string{"mousemove", "--sync", "120", "340", "click", "1"},
		},
		{
			name:     "right click at max boundary",
			x:        agent.DesktopWidth - 1,
			y:        agent.DesktopHeight - 1,
			button:   "right",
			wantArgs: []string{"mousemove", "--sync", "1279", "799", "click", "3"},
		},
		{
			name:     "double click sends repeat 2",
			x:        0,
			y:        0,
			button:   "double",
			wantArgs: []string{"mousemove", "--sync", "0", "0", "click", "--repeat", "2", "1"},
		},
		{
			name:        "negative coordinate rejected",
			x:           -1,
			y:           10,
			button:      "left",
			wantInvalid: true,
			wantErr:     true,
		},
		{
			name:        "x equal desktop width rejected",
			x:           agent.DesktopWidth,
			y:           10,
			button:      "left",
			wantInvalid: true,
			wantErr:     true,
		},
		{
			name:        "y equal desktop height rejected",
			x:           10,
			y:           agent.DesktopHeight,
			button:      "left",
			wantInvalid: true,
			wantErr:     true,
		},
		{
			name:        "unsupported button rejected",
			x:           10,
			y:           10,
			button:      "scroll-up",
			wantInvalid: true,
			wantErr:     true,
		},
		{
			name:      "canceled context rejected",
			x:         10,
			y:         10,
			button:    "left",
			cancelCtx: true,
			wantErr:   true,
		},
		{
			name:      "xdotool failure propagated",
			x:         10,
			y:         10,
			button:    "left",
			runnerErr: errors.New("xdotool failed"),
			wantErr:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			if tc.cancelCtx {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			runner := &fakeCmdRunner{err: tc.runnerErr}
			svc := agent.NewDesktopService(runner)
			err := svc.Click(ctx, tc.x, tc.y, tc.button)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tc.wantInvalid && !errors.Is(err, agent.ErrInvalidDesktopInput) {
					t.Fatalf("expected ErrInvalidDesktopInput, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(runner.calls) != 1 || runner.calls[0].name != "xdotool" || !slices.Equal(runner.calls[0].args, tc.wantArgs) {
				t.Fatalf("calls = %+v, want args %v", runner.calls, tc.wantArgs)
			}
		})
	}
}

func TestDesktopTypeAndKey(t *testing.T) {
	t.Parallel()

	t.Run("type validates input bounds and passes -- separator", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name        string
			text        string
			wantInvalid bool
			runnerErr   error
		}{
			{name: "valid string", text: "--flag https://example.com"},
			{name: "empty text rejected", text: "", wantInvalid: true},
			{name: "oversized text rejected", text: strings.Repeat("x", agent.MaxTypeBytes+1), wantInvalid: true},
			{name: "NUL byte rejected", text: "hi\x00there", wantInvalid: true},
			{name: "runner error returned", text: "ok", runnerErr: errors.New("boom")},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				runner := &fakeCmdRunner{err: tc.runnerErr}
				svc := agent.NewDesktopService(runner)
				err := svc.Type(context.Background(), tc.text)
				if tc.wantInvalid || tc.runnerErr != nil {
					if err == nil {
						t.Fatal("expected error, got nil")
					}
					if tc.wantInvalid && !errors.Is(err, agent.ErrInvalidDesktopInput) {
						t.Fatalf("expected ErrInvalidDesktopInput, got %v", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				want := []string{"type", "--clearmodifiers", "--delay", "12", "--", tc.text}
				if len(runner.calls) != 1 || !slices.Equal(runner.calls[0].args, want) {
					t.Fatalf("args = %+v, want %v", runner.calls, want)
				}
			})
		}
	})

	t.Run("key rejects whitespace and oversized specs", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name        string
			key         string
			wantInvalid bool
			wantKey     string
		}{
			{name: "valid chord", key: "ctrl+l", wantKey: "ctrl+l"},
			{name: "empty key rejected", key: "   ", wantInvalid: true},
			{name: "spaces inside key rejected", key: "Return BackSpace", wantInvalid: true},
			{name: "oversized key rejected", key: strings.Repeat("k", agent.MaxKeyBytes+1), wantInvalid: true},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				runner := &fakeCmdRunner{}
				svc := agent.NewDesktopService(runner)
				err := svc.Key(context.Background(), tc.key)
				if tc.wantInvalid {
					if !errors.Is(err, agent.ErrInvalidDesktopInput) {
						t.Fatalf("expected ErrInvalidDesktopInput, got %v", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				want := []string{"key", "--clearmodifiers", "--", tc.wantKey}
				if len(runner.calls) != 1 || !slices.Equal(runner.calls[0].args, want) {
					t.Fatalf("args = %+v, want %v", runner.calls, want)
				}
			})
		}
	})
}
