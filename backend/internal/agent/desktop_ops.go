package agent

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/desk"
)

// Perform runs one parsed computer-use action, scaling image coordinates onto
// the fixed desktop before anything is sent to X11.
func (d *DesktopService) Perform(ctx context.Context, act desk.Action) (desk.Result, error) {
	scaled, err := scaleAction(act)
	if err != nil {
		d.note(act.Kind, actionParams(act), "", err)
		return desk.Result{}, err
	}
	switch scaled.Kind {
	case desk.KindScreenshot:
		return d.capture(ctx, scaled)
	case desk.KindCursor:
		x, y, err := d.CursorPosition(ctx)
		if err != nil {
			return desk.Result{}, err
		}
		return desk.Result{Text: fmt.Sprintf("cursor %d %d", x, y), Width: x, Height: y}, nil
	case desk.KindMouseMove:
		if err := d.MouseMove(ctx, scaled.X, scaled.Y); err != nil {
			return desk.Result{}, err
		}
		return desk.Result{Text: "moved"}, nil
	case desk.KindClick:
		if err := d.Click(ctx, scaled.X, scaled.Y, scaled.Button); err != nil {
			return desk.Result{}, err
		}
		return desk.Result{Text: "clicked"}, nil
	case desk.KindDrag:
		if err := d.Drag(ctx, scaled.X, scaled.Y, scaled.ToX, scaled.ToY); err != nil {
			return desk.Result{}, err
		}
		return desk.Result{Text: "dragged"}, nil
	case desk.KindScroll:
		if err := d.Scroll(ctx, scaled.X, scaled.Y, scaled.HasPoint, scaled.DX, scaled.DY); err != nil {
			return desk.Result{}, err
		}
		return desk.Result{Text: "scrolled"}, nil
	case desk.KindType:
		if err := d.typeText(ctx, scaled.Text, scaled.DelayMS); err != nil {
			return desk.Result{}, err
		}
		return desk.Result{Text: "typed"}, nil
	case desk.KindKey:
		if err := d.Key(ctx, scaled.Key); err != nil {
			return desk.Result{}, err
		}
		return desk.Result{Text: "pressed"}, nil
	case desk.KindWait:
		if err := d.Wait(ctx, scaled.Wait); err != nil {
			return desk.Result{}, err
		}
		return desk.Result{Text: "waited"}, nil
	case desk.KindBrowserNavigate, desk.KindBrowserClick, desk.KindBrowserFill, desk.KindBrowserRead, desk.KindBrowserScreenshot:
		return d.browserAct(ctx, scaled)
	default:
		err := fmt.Errorf("%w: unsupported action", ErrInvalidDesktopInput)
		d.note(scaled.Kind, nil, "", err)
		return desk.Result{}, err
	}
}

func scaleAction(act desk.Action) (desk.Action, error) {
	if act.ImageWidth == 0 && act.ImageHeight == 0 {
		return act, nil
	}
	if act.HasPoint {
		x, y, err := desk.ScalePoint(act.X, act.Y, act.ImageWidth, act.ImageHeight)
		if err != nil {
			return desk.Action{}, fmt.Errorf("%w: %w", ErrInvalidDesktopInput, err)
		}
		act.X, act.Y = x, y
	}
	if act.HasDrag {
		x, y, err := desk.ScalePoint(act.ToX, act.ToY, act.ImageWidth, act.ImageHeight)
		if err != nil {
			return desk.Action{}, fmt.Errorf("%w: %w", ErrInvalidDesktopInput, err)
		}
		act.ToX, act.ToY = x, y
	}
	act.ImageWidth, act.ImageHeight = 0, 0
	return act, nil
}

func (d *DesktopService) capture(ctx context.Context, act desk.Action) (desk.Result, error) {
	ctx, release, err := d.guard(ctx)
	if err != nil {
		d.note(desk.KindScreenshot, actionParams(act), "", err)
		return desk.Result{}, err
	}
	defer release()
	raw, err := d.runner.Run(ctx, "scrot", "--silent", "-o", "-")
	if err != nil {
		wrapped := fmt.Errorf("capture X11 screenshot: %w", err)
		d.note(desk.KindScreenshot, actionParams(act), "", wrapped)
		return desk.Result{}, wrapped
	}
	encoded, mime, w, h, err := encodeShot(raw, act.Format, act.MaxWidth, act.Quality)
	if err != nil {
		d.note(desk.KindScreenshot, actionParams(act), "", err)
		return desk.Result{}, err
	}
	d.record(desk.KindScreenshot, map[string]any{"format": mime, "width": w, "height": h}, "captured", encoded, mime, 0, 0, false, nil)
	return desk.Result{Text: fmt.Sprintf("Captured %dx%d screenshot", w, h), Image: encoded, Mime: mime, Width: w, Height: h}, nil
}

func imageSpan(width, height int) (int32, int32, error) {
	if width <= 0 || height <= 0 || width > math.MaxInt32 || height > math.MaxInt32 {
		return 0, 0, fmt.Errorf("%w: screenshot size", ErrInvalidDesktopInput)
	}
	return int32(width), int32(height), nil // #nosec G115 -- rejected sizes outside the positive int32 range above
}

func encodeShot(raw []byte, format string, maxWidth int32, quality int) ([]byte, string, int32, int32, error) {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "png"
	}
	if format != "png" && format != "jpeg" {
		return nil, "", 0, 0, fmt.Errorf("%w: screenshot format", ErrInvalidDesktopInput)
	}
	if maxWidth < 0 || maxWidth > DesktopWidth {
		return nil, "", 0, 0, fmt.Errorf("%w: max width", ErrInvalidDesktopInput)
	}
	if quality < 0 || quality > 95 {
		return nil, "", 0, 0, fmt.Errorf("%w: jpeg quality", ErrInvalidDesktopInput)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		// Callers that pass a pre-encoded fixture still need a byte-preserving
		// path when they did not ask for a transform.
		if format == "png" && maxWidth == 0 {
			return append([]byte(nil), raw...), "image/png", DesktopWidth, DesktopHeight, nil
		}
		return nil, "", 0, 0, fmt.Errorf("%w: screenshot is not a png", ErrInvalidDesktopInput)
	}
	bounds := img.Bounds()
	w, h, err := imageSpan(bounds.Dx(), bounds.Dy())
	if err != nil {
		return nil, "", 0, 0, err
	}
	if maxWidth > 0 && w > maxWidth {
		img = downscale(img, int(maxWidth))
		bounds = img.Bounds()
		w, h, err = imageSpan(bounds.Dx(), bounds.Dy())
		if err != nil {
			return nil, "", 0, 0, err
		}
	}
	var buf bytes.Buffer
	mime := "image/png"
	if format == "jpeg" {
		if quality == 0 {
			quality = 60
		}
		if err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
			return nil, "", 0, 0, fmt.Errorf("encode jpeg screenshot: %w", err)
		}
		mime = "image/jpeg"
	} else if err = png.Encode(&buf, img); err != nil {
		return nil, "", 0, 0, fmt.Errorf("encode png screenshot: %w", err)
	}
	return buf.Bytes(), mime, w, h, nil
}

func downscale(src image.Image, maxWidth int) image.Image {
	bounds := src.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	dstW := maxWidth
	dstH := srcH * maxWidth / srcW
	if dstH < 1 {
		dstH = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	for y := 0; y < dstH; y++ {
		sy := bounds.Min.Y + y*srcH/dstH
		for x := 0; x < dstW; x++ {
			sx := bounds.Min.X + x*srcW/dstW
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}

// CursorPosition reads the X11 pointer in desktop pixels.
func (d *DesktopService) CursorPosition(ctx context.Context) (int32, int32, error) {
	ctx, release, err := d.guard(ctx)
	if err != nil {
		d.note(desk.KindCursor, nil, "", err)
		return 0, 0, err
	}
	defer release()
	out, err := d.runner.Run(ctx, "xdotool", "getmouselocation", "--shell")
	if err != nil {
		wrapped := fmt.Errorf("xdotool cursor: %w", err)
		d.note(desk.KindCursor, nil, "", wrapped)
		return 0, 0, wrapped
	}
	x, y, err := parseMouseLocation(out)
	if err != nil {
		d.note(desk.KindCursor, nil, "", err)
		return 0, 0, err
	}
	d.record(desk.KindCursor, map[string]any{"x": x, "y": y}, "cursor", nil, "", x, y, true, nil)
	return x, y, nil
}

func parseMouseLocation(out []byte) (int32, int32, error) {
	var x, y int32
	var sawX, sawY bool
	for _, line := range strings.Split(string(out), "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(val, 10, 32)
		if err != nil {
			continue
		}
		switch key {
		case "X":
			x, sawX = int32(n), true
		case "Y":
			y, sawY = int32(n), true
		}
	}
	if !sawX || !sawY || x < 0 || y < 0 || x >= DesktopWidth || y >= DesktopHeight {
		return 0, 0, fmt.Errorf("%w: cursor location", ErrInvalidDesktopInput)
	}
	return x, y, nil
}

// MouseMove moves the pointer without clicking.
func (d *DesktopService) MouseMove(ctx context.Context, x, y int32) error {
	ctx, release, err := d.guard(ctx)
	if err != nil {
		d.note(desk.KindMouseMove, map[string]any{"x": x, "y": y}, "", err)
		return err
	}
	defer release()
	if x < 0 || x >= DesktopWidth || y < 0 || y >= DesktopHeight {
		err = fmt.Errorf("%w: coordinates (%d, %d) outside %dx%d desktop", ErrInvalidDesktopInput, x, y, DesktopWidth, DesktopHeight)
		d.note(desk.KindMouseMove, map[string]any{"x": x, "y": y}, "", err)
		return err
	}
	if _, err = d.runner.Run(ctx, "xdotool", "mousemove", "--sync", strconv.Itoa(int(x)), strconv.Itoa(int(y))); err != nil {
		wrapped := fmt.Errorf("xdotool move: %w", err)
		d.note(desk.KindMouseMove, map[string]any{"x": x, "y": y}, "", wrapped)
		return wrapped
	}
	d.record(desk.KindMouseMove, map[string]any{"x": x, "y": y}, "moved", nil, "", x, y, true, nil)
	return nil
}

// Drag presses the left button and moves from the start point to the end point.
func (d *DesktopService) Drag(ctx context.Context, x, y, toX, toY int32) error {
	ctx, release, err := d.guard(ctx)
	if err != nil {
		d.note(desk.KindDrag, map[string]any{"x": x, "y": y, "to_x": toX, "to_y": toY}, "", err)
		return err
	}
	defer release()
	for _, pt := range [][2]int32{{x, y}, {toX, toY}} {
		if pt[0] < 0 || pt[0] >= DesktopWidth || pt[1] < 0 || pt[1] >= DesktopHeight {
			err = fmt.Errorf("%w: drag coordinates", ErrInvalidDesktopInput)
			d.note(desk.KindDrag, map[string]any{"x": x, "y": y, "to_x": toX, "to_y": toY}, "", err)
			return err
		}
	}
	args := []string{
		"mousemove", "--sync", strconv.Itoa(int(x)), strconv.Itoa(int(y)),
		"mousedown", "1",
		"mousemove", "--sync", strconv.Itoa(int(toX)), strconv.Itoa(int(toY)),
		"mouseup", "1",
	}
	if _, err = d.runner.Run(ctx, "xdotool", args...); err != nil {
		wrapped := fmt.Errorf("xdotool drag: %w", err)
		d.note(desk.KindDrag, map[string]any{"x": x, "y": y, "to_x": toX, "to_y": toY}, "", wrapped)
		return wrapped
	}
	d.record(desk.KindDrag, map[string]any{"x": x, "y": y, "to_x": toX, "to_y": toY}, "dragged", nil, "", toX, toY, true, nil)
	return nil
}

// Scroll moves the wheel at an optional point. Positive dy scrolls down.
func (d *DesktopService) Scroll(ctx context.Context, x, y int32, hasPoint bool, dx, dy int32) error {
	params := map[string]any{"dx": dx, "dy": dy}
	ctx, release, err := d.guard(ctx)
	if err != nil {
		d.note(desk.KindScroll, params, "", err)
		return err
	}
	defer release()
	if abs32(dx) > desk.MaxScrollTicks || abs32(dy) > desk.MaxScrollTicks || (dx == 0 && dy == 0) {
		err = fmt.Errorf("%w: scroll delta", ErrInvalidDesktopInput)
		d.note(desk.KindScroll, params, "", err)
		return err
	}
	args := []string{}
	if hasPoint {
		if x < 0 || x >= DesktopWidth || y < 0 || y >= DesktopHeight {
			err = fmt.Errorf("%w: scroll coordinates", ErrInvalidDesktopInput)
			d.note(desk.KindScroll, params, "", err)
			return err
		}
		args = append(args, "mousemove", "--sync", strconv.Itoa(int(x)), strconv.Itoa(int(y)))
		params["x"], params["y"] = x, y
	}
	args = append(args, wheelClicks(dx, dy)...)
	if _, err = d.runner.Run(ctx, "xdotool", args...); err != nil {
		wrapped := fmt.Errorf("xdotool scroll: %w", err)
		d.note(desk.KindScroll, params, "", wrapped)
		return wrapped
	}
	d.record(desk.KindScroll, params, "scrolled", nil, "", x, y, hasPoint, nil)
	return nil
}

func wheelClicks(dx, dy int32) []string {
	args := []string{}
	add := func(n int32, button string) {
		if n < 0 {
			n = -n
		}
		if n == 0 {
			return
		}
		args = append(args, "click", "--repeat", strconv.Itoa(int(n)), button)
	}
	if dy < 0 {
		add(dy, "4")
	} else {
		add(dy, "5")
	}
	if dx < 0 {
		add(dx, "6")
	} else {
		add(dx, "7")
	}
	return args
}

// Wait pauses the action until the duration elapses or the control lock cancels it.
func (d *DesktopService) Wait(ctx context.Context, delay time.Duration) error {
	ctx, release, err := d.guard(ctx)
	if err != nil {
		d.note(desk.KindWait, map[string]any{"ms": delay.Milliseconds()}, "", err)
		return err
	}
	defer release()
	if delay <= 0 || delay > desk.MaxWait {
		err = fmt.Errorf("%w: wait", ErrInvalidDesktopInput)
		d.note(desk.KindWait, map[string]any{"ms": delay.Milliseconds()}, "", err)
		return err
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		wrapped := fmt.Errorf("wait canceled: %w", ctx.Err())
		if d.control != nil && d.control.Human() {
			wrapped = fmt.Errorf("%w: %w", ErrControlLocked, ctx.Err())
		}
		d.note(desk.KindWait, map[string]any{"ms": delay.Milliseconds()}, "", wrapped)
		return wrapped
	case <-timer.C:
		d.note(desk.KindWait, map[string]any{"ms": delay.Milliseconds()}, "waited", nil)
		return nil
	}
}

func (d *DesktopService) browserAct(ctx context.Context, act desk.Action) (desk.Result, error) {
	ctx, release, err := d.guard(ctx)
	if err != nil {
		d.note(act.Kind, actionParams(act), "", err)
		return desk.Result{}, err
	}
	defer release()
	if d.browser == nil {
		err = fmt.Errorf("%w: browser debugging is unavailable", ErrInvalidDesktopInput)
		d.note(act.Kind, actionParams(act), "", err)
		return desk.Result{}, err
	}
	var text string
	var image []byte
	switch act.Kind {
	case desk.KindBrowserNavigate:
		err = d.browser.Navigate(ctx, act.URL)
		text = "navigated"
	case desk.KindBrowserClick:
		err = d.browser.Click(ctx, act.Selector)
		text = "clicked " + act.Selector
	case desk.KindBrowserFill:
		err = d.browser.Fill(ctx, act.Selector, act.Text)
		text = "filled " + act.Selector
	case desk.KindBrowserRead:
		text, err = d.browser.Read(ctx, act.Selector)
	case desk.KindBrowserScreenshot:
		image, err = d.browser.Shot(ctx, act.Selector)
		text = "browser screenshot"
	default:
		err = fmt.Errorf("%w: browser action", ErrInvalidDesktopInput)
	}
	if err != nil {
		d.note(act.Kind, actionParams(act), "", err)
		return desk.Result{}, err
	}
	mime := ""
	if len(image) > 0 {
		mime = "image/png"
	}
	d.record(act.Kind, actionParams(act), text, image, mime, 0, 0, false, nil)
	return desk.Result{Text: text, Image: image, Mime: mime}, nil
}

func actionParams(act desk.Action) map[string]any {
	params := map[string]any{"kind": act.Kind}
	if act.HasPoint {
		params["x"], params["y"] = act.X, act.Y
	}
	if act.Button != "" {
		params["button"] = act.Button
	}
	if act.Text != "" {
		params["text"] = act.Text
	}
	if act.Key != "" {
		params["key"] = act.Key
	}
	if act.URL != "" {
		params["url"] = act.URL
	}
	if act.Selector != "" {
		params["selector"] = act.Selector
	}
	return params
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
