// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"strconv"

	"github.com/dunkeltech/botbox/backend/internal/desk"
)

// FormDriver is the deterministic desktop and browser double behind the
// scripted form scenario. It is not wired into the sandbox image.
type FormDriver struct {
	page *desk.FormPage
}

// NewFormDriver returns a driver whose browser and pointer share one form.
func NewFormDriver() *FormDriver {
	return &FormDriver{page: desk.NewFormPage()}
}

// Page exposes the form so a test can assert what was submitted.
func (f *FormDriver) Page() *desk.FormPage { return f.page }

// Run accepts the same scrot and xdotool invocations DesktopService emits.
func (f *FormDriver) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	switch name {
	case "scrot":
		res, err := f.page.Apply(desk.Action{Kind: desk.KindScreenshot})
		return res.Image, err
	case "xdotool":
		act, err := actionFromXdotool(args)
		if err != nil {
			return nil, err
		}
		_, err = f.page.Apply(act)
		return nil, err
	default:
		return nil, desk.ErrInvalid
	}
}

// Navigate opens the fixture form.
func (f *FormDriver) Navigate(_ context.Context, rawURL string) error {
	_, err := f.page.Apply(desk.Action{Kind: desk.KindBrowserNavigate, URL: rawURL})
	return err
}

// Click clicks a selector on the fixture form.
func (f *FormDriver) Click(_ context.Context, selector string) error {
	_, err := f.page.Apply(desk.Action{Kind: desk.KindBrowserClick, Selector: selector})
	return err
}

// Fill writes a selector on the fixture form.
func (f *FormDriver) Fill(_ context.Context, selector, text string) error {
	_, err := f.page.Apply(desk.Action{Kind: desk.KindBrowserFill, Selector: selector, Text: text})
	return err
}

// Read returns fixture form text.
func (f *FormDriver) Read(_ context.Context, selector string) (string, error) {
	res, err := f.page.Apply(desk.Action{Kind: desk.KindBrowserRead, Selector: selector})
	return res.Text, err
}

// Shot returns the fixture screenshot.
func (f *FormDriver) Shot(context.Context, string) ([]byte, error) {
	res, err := f.page.Apply(desk.Action{Kind: desk.KindScreenshot})
	return res.Image, err
}

func actionFromXdotool(args []string) (desk.Action, error) {
	if len(args) >= 5 && args[0] == "mousemove" && args[1] == "--sync" && args[4] == "click" {
		x, y, err := pair(args[2], args[3])
		if err != nil {
			return desk.Action{}, err
		}
		button := "left"
		if len(args) >= 7 && args[5] == "--repeat" && args[6] == "2" {
			button = "double"
		}
		return desk.Action{Kind: desk.KindClick, X: x, Y: y, HasPoint: true, Button: button}, nil
	}
	if len(args) >= 4 && args[0] == "mousemove" && args[1] == "--sync" && len(args) == 4 {
		x, y, err := pair(args[2], args[3])
		if err != nil {
			return desk.Action{}, err
		}
		return desk.Action{Kind: desk.KindMouseMove, X: x, Y: y, HasPoint: true}, nil
	}
	if len(args) >= 6 && args[0] == "type" {
		return desk.Action{Kind: desk.KindType, Text: args[len(args)-1]}, nil
	}
	if len(args) >= 3 && args[0] == "key" {
		return desk.Action{Kind: desk.KindKey, Key: args[len(args)-1]}, nil
	}
	if len(args) >= 10 && args[0] == "mousemove" && containsArg(args, "mousedown") {
		x, y, err := pair(args[2], args[3])
		if err != nil {
			return desk.Action{}, err
		}
		toX, toY, err := pair(args[7], args[8])
		if err != nil {
			return desk.Action{}, err
		}
		return desk.Action{Kind: desk.KindDrag, X: x, Y: y, ToX: toX, ToY: toY, HasPoint: true, HasDrag: true}, nil
	}
	return desk.Action{}, desk.ErrInvalid
}

func pair(xs, ys string) (int32, int32, error) {
	x, err := strconv.ParseInt(xs, 10, 32)
	if err != nil {
		return 0, 0, desk.ErrInvalid
	}
	y, err := strconv.ParseInt(ys, 10, 32)
	if err != nil {
		return 0, 0, desk.ErrInvalid
	}
	return int32(x), int32(y), nil
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
