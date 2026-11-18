// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/desk"
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
	runner  CommandRunner
	control *ControlGate
	browser Browser
	journal *Journal
	now     func() time.Time
}

// NewDesktopService constructs a DesktopService backed by runner (ExecCommandRunner if nil).
func NewDesktopService(runner CommandRunner) *DesktopService {
	if runner == nil {
		runner = ExecCommandRunner{}
	}
	return &DesktopService{runner: runner, now: time.Now}
}

// SetControl attaches the human-takeover gate checked before every action.
func (d *DesktopService) SetControl(gate *ControlGate) {
	if d != nil {
		d.control = gate
	}
}

// SetBrowser attaches the localhost browser tool.
func (d *DesktopService) SetBrowser(browser Browser) {
	if d != nil {
		d.browser = browser
	}
}

// SetHuman records a takeover and cancels the action already in flight.
func (d *DesktopService) SetHuman(held bool) {
	if d == nil || d.control == nil {
		return
	}
	d.control.SetHuman(held)
}

// Journal returns the in-agent action log shared by gRPC and MCP.
func (d *DesktopService) Journal() *Journal {
	if d == nil {
		return nil
	}
	return d.journal
}

// SetJournal records actions for the workspace timeline.
func (d *DesktopService) SetJournal(journal *Journal) {
	if d != nil {
		d.journal = journal
	}
}

// Screenshot captures the :0 X11 screen as PNG bytes alongside desktop dimensions.
func (d *DesktopService) Screenshot(ctx context.Context) ([]byte, int32, int32, error) {
	ctx, release, err := d.guard(ctx)
	if err != nil {
		d.note(desk.KindScreenshot, nil, "", err)
		return nil, 0, 0, err
	}
	defer release()
	pngBytes, err := d.runner.Run(ctx, "scrot", "--silent", "-o", "-")
	if err != nil {
		return nil, 0, 0, fmt.Errorf("capture X11 screenshot: %w", err)
	}
	if len(pngBytes) == 0 {
		return nil, 0, 0, errors.New("capture X11 screenshot: empty PNG output")
	}
	d.record(desk.KindScreenshot, map[string]any{"format": "png"}, "captured", pngBytes, "image/png", 0, 0, false, nil)
	return pngBytes, DesktopWidth, DesktopHeight, nil
}

// Click moves the pointer to (x, y) within 1280x800 and triggers left/right/middle/double click.
func (d *DesktopService) Click(ctx context.Context, x, y int32, button string) error {
	ctx, release, err := d.guard(ctx)
	if err != nil {
		d.note(desk.KindClick, map[string]any{"x": x, "y": y, "button": button}, "", err)
		return err
	}
	defer release()
	if x < 0 || x >= DesktopWidth || y < 0 || y >= DesktopHeight {
		err := fmt.Errorf("%w: coordinates (%d, %d) outside %dx%d desktop",
			ErrInvalidDesktopInput, x, y, DesktopWidth, DesktopHeight)
		d.note(desk.KindClick, map[string]any{"x": x, "y": y, "button": button}, "", err)
		return err
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
	case "triple":
		args = append(args, "--repeat", "3", "1")
	default:
		err := fmt.Errorf("%w: unsupported mouse button %q", ErrInvalidDesktopInput, button)
		d.note(desk.KindClick, map[string]any{"x": x, "y": y, "button": button}, "", err)
		return err
	}

	if _, err := d.runner.Run(ctx, "xdotool", args...); err != nil {
		wrapped := fmt.Errorf("xdotool click: %w", err)
		d.note(desk.KindClick, map[string]any{"x": x, "y": y, "button": button}, "", wrapped)
		return wrapped
	}
	d.record(desk.KindClick, map[string]any{"x": x, "y": y, "button": button}, "clicked", nil, "", x, y, true, nil)
	return nil
}

// Type types text into the focused X11 window at the default per-character delay.
func (d *DesktopService) Type(ctx context.Context, text string) error {
	return d.typeText(ctx, text, 12)
}

// typeText types with a per-character delay. Zero means the 12ms default because
// a missing JSON field decodes as 0 and xdotool would otherwise type with no delay.
func (d *DesktopService) typeText(ctx context.Context, text string, delayMS int32) error {
	ctx, release, err := d.guard(ctx)
	if err != nil {
		d.note(desk.KindType, map[string]any{"text": text}, "", err)
		return err
	}
	defer release()
	if text == "" {
		err := fmt.Errorf("%w: text is required", ErrInvalidDesktopInput)
		d.note(desk.KindType, map[string]any{"text": text}, "", err)
		return err
	}
	if len(text) > MaxTypeBytes {
		err := fmt.Errorf("%w: text exceeds %d bytes", ErrInvalidDesktopInput, MaxTypeBytes)
		d.note(desk.KindType, map[string]any{"text": text}, "", err)
		return err
	}
	if strings.ContainsRune(text, '\x00') {
		err := fmt.Errorf("%w: NUL bytes are not allowed", ErrInvalidDesktopInput)
		d.note(desk.KindType, map[string]any{"text": text}, "", err)
		return err
	}
	if delayMS <= 0 || delayMS > 100 {
		delayMS = 12
	}

	if _, err := d.runner.Run(ctx, "xdotool", "type", "--clearmodifiers", "--delay", strconv.Itoa(int(delayMS)), "--", text); err != nil {
		d.note(desk.KindType, map[string]any{"text": text}, "", err)
		return fmt.Errorf("xdotool type: %w", err)
	}
	d.note(desk.KindType, map[string]any{"text": text, "delay_ms": delayMS}, "typed", nil)
	return nil
}

// Key sends a key name or key combination (such as "Return" or "ctrl+l") via xdotool.
func (d *DesktopService) Key(ctx context.Context, key string) error {
	ctx, release, err := d.guard(ctx)
	if err != nil {
		d.note(desk.KindKey, map[string]any{"key": key}, "", err)
		return err
	}
	defer release()
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return fmt.Errorf("%w: key is required", ErrInvalidDesktopInput)
	}
	if len(trimmed) > MaxKeyBytes || strings.ContainsAny(trimmed, "\n\r\t \x00") {
		return fmt.Errorf("%w: invalid key specification", ErrInvalidDesktopInput)
	}

	if _, err := d.runner.Run(ctx, "xdotool", "key", "--clearmodifiers", "--", trimmed); err != nil {
		d.note(desk.KindKey, map[string]any{"key": trimmed}, "", err)
		return fmt.Errorf("xdotool key: %w", err)
	}
	d.note(desk.KindKey, map[string]any{"key": trimmed}, "pressed", nil)
	return nil
}

func (d *DesktopService) guard(ctx context.Context) (context.Context, func(), error) {
	if d == nil {
		return nil, nil, errors.New("desktop service is not configured")
	}
	if d.control == nil {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		return ctx, func() {}, nil
	}
	return d.control.Bind(ctx)
}

func (d *DesktopService) note(kind string, params map[string]any, result string, err error) {
	d.record(kind, params, result, nil, "", 0, 0, false, err)
}

func (d *DesktopService) record(kind string, params map[string]any, result string, image []byte, mime string, x, y int32, hasPoint bool, err error) {
	if d == nil || d.journal == nil {
		return
	}
	code := ""
	if err != nil {
		result = ""
		switch {
		case errors.Is(err, ErrControlLocked):
			code = "control.locked"
		case errors.Is(err, ErrInvalidDesktopInput), errors.Is(err, desk.ErrInvalid):
			code = "validation"
		default:
			code = "desktop.failed"
		}
	}
	raw, marshalErr := json.Marshal(params)
	if marshalErr != nil {
		raw = []byte(`{}`)
	}
	at := time.Now().UTC()
	if d.now != nil {
		at = d.now().UTC()
	}
	d.journal.Record(JournalEntry{Kind: kind, Params: raw, Result: result, ErrorCode: code, Image: image, Mime: mime, X: x, Y: y, HasPoint: hasPoint, At: at})
}
