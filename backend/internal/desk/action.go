// SPDX-License-Identifier: AGPL-3.0-only

// Package desk is the shared computer-use action model.
// The sandbox agent and the API runner both parse model tool calls into these
// actions so coordinate rules and limits cannot drift between gRPC and MCP.
package desk

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Fixed desktop size. Screenshots may be smaller; coordinates are scaled back
// onto this grid before xdotool runs (Application.md §4.7).
const (
	Width  = 1280
	Height = 800

	// MaxTypeBytes matches the agent input cap so a model cannot stream
	// an unbounded keyboard payload into the sandbox.
	MaxTypeBytes = 4096
	MaxKeyBytes  = 128
	// MaxWait bounds a single wait so a takeover can still land inside one second
	// once the control gate cancels the call, and so a model cannot park a turn.
	MaxWait = 10 * time.Second
	// MaxScrollTicks caps one wheel gesture.
	MaxScrollTicks = 20
)

// Action kinds executed by the sandbox desktop and browser tools.
const (
	KindScreenshot        = "screenshot"
	KindCursor            = "cursor_position"
	KindMouseMove         = "mouse_move"
	KindClick             = "click"
	KindDrag              = "drag"
	KindScroll            = "scroll"
	KindType              = "type"
	KindKey               = "key"
	KindWait              = "wait"
	KindBrowserNavigate   = "browser_navigate"
	KindBrowserClick      = "browser_click"
	KindBrowserFill       = "browser_fill"
	KindBrowserRead       = "browser_read"
	KindBrowserScreenshot = "browser_screenshot"
)

// ErrInvalid is returned for malformed computer-use arguments.
var ErrInvalid = errors.New("invalid desktop action")

// IsTool reports whether name is a desktop or browser tool, including the
// provider computer-use alias.
func IsTool(name string) bool {
	switch name {
	case "computer", KindScreenshot, KindCursor, KindMouseMove, KindClick, KindDrag, KindScroll, KindType, KindKey, KindWait, KindBrowserNavigate, KindBrowserClick, KindBrowserFill, KindBrowserRead, KindBrowserScreenshot:
		return true
	default:
		return false
	}
}

// Action is one desktop or browser operation in desktop coordinates unless
// ImageWidth and ImageHeight say the point was chosen on a downscaled shot.
type Action struct {
	Kind        string
	X, Y        int32
	ToX, ToY    int32
	Button      string
	Text        string
	Key         string
	ImageWidth  int32
	ImageHeight int32
	DX, DY      int32
	Wait        time.Duration
	URL         string
	Selector    string
	Format      string
	MaxWidth    int32
	Quality     int
	DelayMS     int32
	HasPoint    bool
	HasDrag     bool
}

// Result is the tool output. Image is omitted from logs by callers.
type Result struct {
	Text   string
	Image  []byte
	Mime   string
	Width  int32
	Height int32
}

// ScalePoint maps a point on a width-by-height image onto the 1280×800 desktop.
func ScalePoint(x, y, imageW, imageH int32) (int32, int32, error) {
	if imageW <= 0 || imageH <= 0 || x < 0 || y < 0 || x >= imageW || y >= imageH {
		return 0, 0, fmt.Errorf("%w: point (%d,%d) outside %dx%d image", ErrInvalid, x, y, imageW, imageH)
	}
	if imageW == Width && imageH == Height {
		return x, y, nil
	}
	sx := int32(int64(x) * Width / int64(imageW))  // #nosec G115 -- x is inside the image and Width is 1280, so the quotient is below 1280
	sy := int32(int64(y) * Height / int64(imageH)) // #nosec G115 -- y is inside the image and Height is 800, so the quotient is below 800
	if sx >= Width {
		sx = Width - 1
	}
	if sy >= Height {
		sy = Height - 1
	}
	return sx, sy, nil
}

// Parse turns a tool name plus JSON arguments into an action.
// The name "computer" accepts Anthropic and OpenAI computer-use action objects.
func Parse(name string, raw json.RawMessage) (Action, error) {
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	if !json.Valid(raw) {
		return Action{}, fmt.Errorf("%w: arguments are not json", ErrInvalid)
	}
	if name == "computer" {
		return parseComputer(raw)
	}
	return parseNamed(name, raw)
}

func parseNamed(name string, raw json.RawMessage) (Action, error) {
	var in struct {
		X           *int32 `json:"x"`
		Y           *int32 `json:"y"`
		ToX         *int32 `json:"to_x"`
		ToY         *int32 `json:"to_y"`
		Button      string `json:"button"`
		Text        string `json:"text"`
		Key         string `json:"key"`
		ImageWidth  int32  `json:"image_width"`
		ImageHeight int32  `json:"image_height"`
		DX          int32  `json:"dx"`
		DY          int32  `json:"dy"`
		MS          *int32 `json:"ms"`
		URL         string `json:"url"`
		Selector    string `json:"selector"`
		Format      string `json:"format"`
		MaxWidth    int32  `json:"max_width"`
		Quality     int    `json:"quality"`
		DelayMS     int32  `json:"delay_ms"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return Action{}, fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	act := Action{
		Kind: name, Button: in.Button, Text: in.Text, Key: in.Key,
		ImageWidth: in.ImageWidth, ImageHeight: in.ImageHeight,
		DX: in.DX, DY: in.DY, URL: in.URL, Selector: in.Selector,
		Format: in.Format, MaxWidth: in.MaxWidth, Quality: in.Quality, DelayMS: in.DelayMS,
	}
	if in.X != nil || in.Y != nil {
		if in.X == nil || in.Y == nil {
			return Action{}, fmt.Errorf("%w: x and y are both required", ErrInvalid)
		}
		act.X, act.Y, act.HasPoint = *in.X, *in.Y, true
	}
	if in.ToX != nil || in.ToY != nil {
		if in.ToX == nil || in.ToY == nil {
			return Action{}, fmt.Errorf("%w: drag end requires to_x and to_y", ErrInvalid)
		}
		act.ToX, act.ToY, act.HasDrag = *in.ToX, *in.ToY, true
	}
	if in.MS != nil {
		if *in.MS < 0 {
			return Action{}, fmt.Errorf("%w: negative wait", ErrInvalid)
		}
		act.Wait = time.Duration(*in.MS) * time.Millisecond
	}
	if err := validate(act); err != nil {
		return Action{}, err
	}
	return act, nil
}

func parseComputer(raw json.RawMessage) (Action, error) {
	var in struct {
		Action          string   `json:"action"`
		Type            string   `json:"type"`
		Coordinate      []int    `json:"coordinate"`
		StartCoordinate []int    `json:"start_coordinate"`
		X               *int     `json:"x"`
		Y               *int     `json:"y"`
		ToX             *int     `json:"to_x"`
		ToY             *int     `json:"to_y"`
		Text            string   `json:"text"`
		Key             string   `json:"key"`
		Keys            []string `json:"keys"`
		Button          string   `json:"button"`
		ScrollDirection string   `json:"scroll_direction"`
		ScrollAmount    int      `json:"scroll_amount"`
		ScrollX         int      `json:"scroll_x"`
		ScrollY         int      `json:"scroll_y"`
		Path            []struct {
			X int `json:"x"`
			Y int `json:"y"`
		} `json:"path"`
		Duration any    `json:"duration"`
		URL      string `json:"url"`
		Selector string `json:"selector"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return Action{}, fmt.Errorf("%w: computer action", ErrInvalid)
	}
	name := in.Action
	if name == "" {
		name = in.Type
	}
	act := Action{Text: in.Text, Key: in.Key, Button: in.Button, URL: in.URL, Selector: in.Selector}
	if len(in.Keys) > 0 {
		act.Key = strings.ToLower(strings.Join(in.Keys, "+"))
	}
	switch name {
	case "screenshot":
		act.Kind = KindScreenshot
	case "cursor_position":
		act.Kind = KindCursor
	case "mouse_move", "move":
		act.Kind = KindMouseMove
	case "left_click", "right_click", "middle_click", "double_click", "triple_click", "click":
		act.Kind = KindClick
		if act.Button == "" {
			act.Button = strings.TrimSuffix(strings.TrimSuffix(name, "_click"), "click")
		}
		if name == "click" && act.Button == "" {
			act.Button = "left"
		}
		if name == "left_click" {
			act.Button = "left"
		}
		if name == "double_click" {
			act.Button = "double"
		}
		if name == "triple_click" {
			act.Button = "triple"
		}
	case "left_click_drag", "drag":
		act.Kind = KindDrag
		act.Button = "left"
	case "scroll":
		act.Kind = KindScroll
	case "type":
		act.Kind = KindType
	case "key", "keypress":
		act.Kind = KindKey
	case "wait":
		act.Kind = KindWait
	default:
		return Action{}, fmt.Errorf("%w: unsupported computer action %q", ErrInvalid, name)
	}
	if len(in.Coordinate) > 0 {
		if len(in.Coordinate) != 2 {
			return Action{}, fmt.Errorf("%w: coordinate must be [x,y]", ErrInvalid)
		}
		x, y, err := checkedPair(in.Coordinate[0], in.Coordinate[1])
		if err != nil {
			return Action{}, err
		}
		act.X, act.Y, act.HasPoint = x, y, true
	}
	if in.X != nil && in.Y != nil {
		x, y, err := checkedPair(*in.X, *in.Y)
		if err != nil {
			return Action{}, err
		}
		act.X, act.Y, act.HasPoint = x, y, true
	}
	if len(in.StartCoordinate) == 2 && act.HasPoint {
		x, y, err := checkedPair(in.StartCoordinate[0], in.StartCoordinate[1])
		if err != nil {
			return Action{}, err
		}
		act.ToX, act.ToY = act.X, act.Y
		act.X, act.Y = x, y
		act.HasDrag = true
	}
	if len(in.Path) >= 2 {
		x, y, err := checkedPair(in.Path[0].X, in.Path[0].Y)
		if err != nil {
			return Action{}, err
		}
		last := in.Path[len(in.Path)-1]
		toX, toY, err := checkedPair(last.X, last.Y)
		if err != nil {
			return Action{}, err
		}
		act.X, act.Y = x, y
		act.ToX, act.ToY = toX, toY
		act.HasPoint, act.HasDrag = true, true
	}
	if in.ToX != nil && in.ToY != nil {
		x, y, err := checkedPair(*in.ToX, *in.ToY)
		if err != nil {
			return Action{}, err
		}
		act.ToX, act.ToY, act.HasDrag = x, y, true
	}
	switch strings.ToLower(in.ScrollDirection) {
	case "up":
		act.DY = -amount(in.ScrollAmount)
	case "down":
		act.DY = amount(in.ScrollAmount)
	case "left":
		act.DX = -amount(in.ScrollAmount)
	case "right":
		act.DX = amount(in.ScrollAmount)
	}
	if in.ScrollX != 0 {
		dx, err := checkedInt32(in.ScrollX)
		if err != nil {
			return Action{}, err
		}
		act.DX = dx
	}
	if in.ScrollY != 0 {
		dy, err := checkedInt32(in.ScrollY)
		if err != nil {
			return Action{}, err
		}
		act.DY = dy
	}
	if act.Kind == KindWait {
		ms, err := waitMillis(in.Duration)
		if err != nil {
			return Action{}, err
		}
		act.Wait = time.Duration(ms) * time.Millisecond
	}
	if err := validate(act); err != nil {
		return Action{}, err
	}
	return act, nil
}

func checkedInt32(v int) (int32, error) {
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, fmt.Errorf("%w: coordinate", ErrInvalid)
	}
	return int32(v), nil // #nosec G115 -- rejected values outside the int32 range above
}

func checkedPair(x, y int) (int32, int32, error) {
	sx, err := checkedInt32(x)
	if err != nil {
		return 0, 0, err
	}
	sy, err := checkedInt32(y)
	if err != nil {
		return 0, 0, err
	}
	return sx, sy, nil
}

func amount(v int) int32 {
	if v == 0 {
		return 3
	}
	if v < 0 {
		v = -v
	}
	if v > MaxScrollTicks {
		v = MaxScrollTicks
	}
	return int32(v)
}

func waitMillis(v any) (int64, error) {
	switch n := v.(type) {
	case nil:
		return 1000, nil
	case float64:
		if n < 0 || n > 10 {
			return 0, fmt.Errorf("%w: wait duration out of range", ErrInvalid)
		}
		return int64(n * 1000), nil
	default:
		return 0, fmt.Errorf("%w: wait duration", ErrInvalid)
	}
}

func validate(act Action) error {
	switch act.Kind {
	case KindScreenshot, KindCursor:
		return nil
	case KindMouseMove, KindClick:
		if !act.HasPoint {
			return fmt.Errorf("%w: coordinates are required", ErrInvalid)
		}
		return checkPoint(act.X, act.Y, act.ImageWidth, act.ImageHeight)
	case KindDrag:
		if !act.HasPoint || !act.HasDrag {
			return fmt.Errorf("%w: drag requires a start and end", ErrInvalid)
		}
		if err := checkPoint(act.X, act.Y, act.ImageWidth, act.ImageHeight); err != nil {
			return err
		}
		return checkPoint(act.ToX, act.ToY, act.ImageWidth, act.ImageHeight)
	case KindScroll:
		if act.DX == 0 && act.DY == 0 {
			return fmt.Errorf("%w: scroll delta is required", ErrInvalid)
		}
		if abs(act.DX) > MaxScrollTicks || abs(act.DY) > MaxScrollTicks {
			return fmt.Errorf("%w: scroll delta exceeds %d", ErrInvalid, MaxScrollTicks)
		}
		if act.HasPoint {
			return checkPoint(act.X, act.Y, act.ImageWidth, act.ImageHeight)
		}
		return nil
	case KindType:
		if act.Text == "" || len(act.Text) > MaxTypeBytes || strings.ContainsRune(act.Text, 0) {
			return fmt.Errorf("%w: text", ErrInvalid)
		}
		if act.DelayMS < 0 || act.DelayMS > 100 {
			return fmt.Errorf("%w: typing delay", ErrInvalid)
		}
		return nil
	case KindKey:
		key := strings.TrimSpace(act.Key)
		if key == "" || len(key) > MaxKeyBytes || strings.ContainsAny(key, "\n\r\t \x00") {
			return fmt.Errorf("%w: key", ErrInvalid)
		}
		return nil
	case KindWait:
		if act.Wait <= 0 || act.Wait > MaxWait {
			return fmt.Errorf("%w: wait must be 1ms..10s", ErrInvalid)
		}
		return nil
	case KindBrowserNavigate:
		if !validNavURL(act.URL) {
			return fmt.Errorf("%w: navigate url", ErrInvalid)
		}
		return nil
	case KindBrowserClick, KindBrowserRead, KindBrowserScreenshot:
		// Read and element screenshots may omit the selector; click and fill may not.
		allowsEmpty := (act.Kind == KindBrowserRead || act.Kind == KindBrowserScreenshot) && act.Selector == ""
		if !allowsEmpty && !validSelector(act.Selector) {
			return fmt.Errorf("%w: selector", ErrInvalid)
		}
		return nil
	case KindBrowserFill:
		if !validSelector(act.Selector) || act.Text == "" || len(act.Text) > MaxTypeBytes || strings.ContainsRune(act.Text, 0) {
			return fmt.Errorf("%w: fill", ErrInvalid)
		}
		return nil
	default:
		return fmt.Errorf("%w: unknown tool %q", ErrInvalid, act.Kind)
	}
}

func checkPoint(x, y, imageW, imageH int32) error {
	w, h := int32(Width), int32(Height)
	if imageW > 0 || imageH > 0 {
		w, h = imageW, imageH
	}
	if x < 0 || y < 0 || x >= w || y >= h {
		return fmt.Errorf("%w: coordinates (%d,%d) outside %dx%d", ErrInvalid, x, y, w, h)
	}
	return nil
}

func validSelector(sel string) bool {
	if sel == "" || len(sel) > 300 || strings.ContainsRune(sel, 0) || strings.ContainsAny(sel, "\n\r") {
		return false
	}
	return !strings.Contains(strings.ToLower(sel), "javascript:")
}

func validNavURL(raw string) bool {
	if len(raw) == 0 || len(raw) > 2000 || strings.ContainsRune(raw, 0) {
		return false
	}
	lower := strings.ToLower(raw)
	var rest string
	switch {
	case strings.HasPrefix(lower, "https://"):
		rest = raw[len("https://"):]
	case strings.HasPrefix(lower, "http://"):
		rest = raw[len("http://"):]
	default:
		return false
	}
	if rest == "" || strings.ContainsAny(rest, " \t") {
		return false
	}
	host := rest
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	host = strings.ToLower(host)
	if host == "" {
		return false
	}
	// Localhost is the sandbox itself, where the recommended browser tool
	// reaches a page without leaving the egress proxy.
	return strings.Contains(host, ".") || strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1")
}

func abs(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
