// SPDX-License-Identifier: AGPL-3.0-only

package desk

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"sync"
)

// FormURL is the only page the deterministic form double serves.
// Tests drive it through browser tools and through pixel clicks.
const FormURL = "http://127.0.0.1:8765/form"

// FormPage is an in-memory web form used to prove a scripted model can
// finish a browser task without a display, a browser, or a model provider.
type FormPage struct {
	mu        sync.Mutex
	name      string
	email     string
	focus     string
	submitted bool
	status    string
	shot      []byte
}

// NewFormPage returns an empty form with a real PNG screenshot.
func NewFormPage() *FormPage {
	return &FormPage{status: "editing", shot: tinyPNG()}
}

// Apply runs one action against the form and returns the observable result.
func (p *FormPage) Apply(act Action) (Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch act.Kind {
	case KindScreenshot:
		return Result{Text: "screenshot", Image: append([]byte(nil), p.shot...), Mime: "image/png", Width: 32, Height: 20}, nil
	case KindBrowserNavigate:
		if act.URL != FormURL {
			return Result{}, ErrInvalid
		}
		p.name, p.email, p.focus, p.submitted = "", "", "", false
		p.status = "editing"
		return Result{Text: "navigated"}, nil
	case KindBrowserFill:
		if act.Selector != "#name" && act.Selector != "#email" {
			return Result{}, ErrInvalid
		}
		if act.Selector == "#name" {
			p.name = act.Text
			p.focus = "name"
		} else {
			p.email = act.Text
			p.focus = "email"
		}
		return Result{Text: "filled " + act.Selector}, nil
	case KindBrowserClick:
		if act.Selector != "#submit" {
			return Result{}, ErrInvalid
		}
		return p.submit()
	case KindBrowserRead:
		if act.Selector != "" && act.Selector != "#status" {
			return Result{}, ErrInvalid
		}
		return Result{Text: p.status}, nil
	case KindClick:
		switch {
		case inBox(act.X, act.Y, 80, 400, 80, 120):
			p.focus = "name"
		case inBox(act.X, act.Y, 80, 400, 150, 190):
			p.focus = "email"
		case inBox(act.X, act.Y, 80, 220, 220, 260):
			return p.submit()
		default:
			p.focus = ""
		}
		return Result{Text: "clicked"}, nil
	case KindType:
		switch p.focus {
		case "name":
			p.name += act.Text
		case "email":
			p.email += act.Text
		default:
			return Result{}, ErrInvalid
		}
		return Result{Text: "typed"}, nil
	case KindKey:
		if act.Key == "Return" && p.focus != "" {
			return p.submit()
		}
		return Result{Text: "key"}, nil
	default:
		return Result{Text: act.Kind}, nil
	}
}

// Submitted reports whether the form was submitted and the field values.
func (p *FormPage) Submitted() (bool, string, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.submitted, p.name, p.email
}

func (p *FormPage) submit() (Result, error) {
	if p.name == "" || p.email == "" {
		p.status = "missing"
		p.submitted = false
		return Result{Text: "missing"}, nil
	}
	p.submitted = true
	p.status = "saved " + p.name
	return Result{Text: p.status}, nil
}

func inBox(x, y, x0, x1, y0, y1 int32) bool {
	return x >= x0 && x < x1 && y >= y0 && y < y1
}

func tinyPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 32, 20))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	img.Set(0, 0, color.RGBA{R: 20, G: 120, B: 40, A: 255})
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
