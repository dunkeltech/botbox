package agent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Desktop dimensions and payload limits enforced on X11 automation calls.
const (
	// DesktopWidth matches the fixed Xtigervnc desktop width in pixels.
	DesktopWidth = 1280
	// DesktopHeight matches the fixed Xtigervnc desktop height in pixels.
	DesktopHeight = 800

	// MaxTypeBytes caps xdotool typed payload size per call.
	MaxTypeBytes = 4096
	// MaxKeyBytes caps single keychord specification length per call.
	MaxKeyBytes = 128
)

// ErrInvalidDesktopInput indicates out-of-range coordinates or unsupported action arguments.
var ErrInvalidDesktopInput = errors.New("invalid desktop action input")

// CommandRunner abstracts subprocess execution so desktop tools remain testable without X11.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecCommandRunner runs real xdotool/scrot subprocesses against DISPLAY=:0.
type ExecCommandRunner struct{}

// Run executes name with args bound to ctx.
func (ExecCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	//nolint:gosec // command names ("xdotool", "scrot") and flags are fixed internal constants
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.Output()
}

// DesktopService executes validated desktop actions and screenshots inside the sandbox.
type DesktopService struct {
	runner CommandRunner
}

// NewDesktopService constructs a DesktopService backed by runner (ExecCommandRunner if nil).
func NewDesktopService(runner CommandRunner) *DesktopService {
	if runner == nil {
		runner = ExecCommandRunner{}
	}
	return &DesktopService{runner: runner}
}

// Screenshot captures the :0 X11 screen as PNG bytes alongside desktop dimensions.
func (d *DesktopService) Screenshot(ctx context.Context) ([]byte, int32, int32, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, 0, fmt.Errorf("screenshot canceled: %w", err)
	}
	pngBytes, err := d.runner.Run(ctx, "scrot", "--silent", "-o", "-")
	if err != nil {
		return nil, 0, 0, fmt.Errorf("capture X11 screenshot: %w", err)
	}
	if len(pngBytes) == 0 {
		return nil, 0, 0, errors.New("capture X11 screenshot: empty PNG output")
	}
	return pngBytes, DesktopWidth, DesktopHeight, nil
}

// Click moves the pointer to (x, y) within 1280x800 and triggers left/right/middle/double click.
func (d *DesktopService) Click(ctx context.Context, x, y int32, button string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("click canceled: %w", err)
	}
	if x < 0 || x >= DesktopWidth || y < 0 || y >= DesktopHeight {
		return fmt.Errorf("%w: coordinates (%d, %d) outside %dx%d desktop",
			ErrInvalidDesktopInput, x, y, DesktopWidth, DesktopHeight)
	}

	args := []string{"mousemove", "--sync", strconv.Itoa(int(x)), strconv.Itoa(int(y)), "click"}
	switch strings.ToLower(strings.TrimSpace(button)) {
	case "", "left":
		args = append(args, "1")
	case "middle":
		args = append(args, "2")
	case "right":
		args = append(args, "3")
	case "double":
		args = append(args, "--repeat", "2", "1")
	default:
		return fmt.Errorf("%w: unsupported mouse button %q", ErrInvalidDesktopInput, button)
	}

	if _, err := d.runner.Run(ctx, "xdotool", args...); err != nil {
		return fmt.Errorf("xdotool click: %w", err)
	}
	return nil
}

// Type types text into the focused X11 window.
func (d *DesktopService) Type(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("type canceled: %w", err)
	}
	if text == "" {
		return fmt.Errorf("%w: text is required", ErrInvalidDesktopInput)
	}
	if len(text) > MaxTypeBytes {
		return fmt.Errorf("%w: text exceeds %d bytes", ErrInvalidDesktopInput, MaxTypeBytes)
	}
	if strings.ContainsRune(text, '\x00') {
		return fmt.Errorf("%w: NUL bytes are not allowed", ErrInvalidDesktopInput)
	}

	if _, err := d.runner.Run(ctx, "xdotool", "type", "--clearmodifiers", "--delay", "12", "--", text); err != nil {
		return fmt.Errorf("xdotool type: %w", err)
	}
	return nil
}

// Key sends a key name or key combination (such as "Return" or "ctrl+l") via xdotool.
func (d *DesktopService) Key(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("key canceled: %w", err)
	}
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return fmt.Errorf("%w: key is required", ErrInvalidDesktopInput)
	}
	if len(trimmed) > MaxKeyBytes || strings.ContainsAny(trimmed, "\n\r\t \x00") {
		return fmt.Errorf("%w: invalid key specification", ErrInvalidDesktopInput)
	}

	if _, err := d.runner.Run(ctx, "xdotool", "key", "--clearmodifiers", "--", trimmed); err != nil {
		return fmt.Errorf("xdotool key: %w", err)
	}
	return nil
}
