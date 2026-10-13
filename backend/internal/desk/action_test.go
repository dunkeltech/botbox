package desk_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/desk"
)

func TestScalePoint(t *testing.T) {
	t.Parallel()
	sx, sy, err := desk.ScalePoint(639, 399, 640, 400)
	if err != nil || sx != 1278 || sy != 798 {
		t.Fatalf("scaled = (%d,%d) err=%v", sx, sy, err)
	}
	if _, _, err = desk.ScalePoint(0, 0, desk.Width, desk.Height); err != nil {
		t.Fatal(err)
	}
	for _, pt := range [][4]int32{{0, 0, 0, 400}, {-1, 0, 640, 400}, {640, 0, 640, 400}, {1, 1, 0, 10}} {
		if _, _, err = desk.ScalePoint(pt[0], pt[1], pt[2], pt[3]); !errors.Is(err, desk.ErrInvalid) {
			t.Fatalf("point %v err=%v", pt, err)
		}
	}
}

func TestParseActions(t *testing.T) {
	t.Parallel()
	click, err := desk.Parse("computer", json.RawMessage(`{"action":"left_click","coordinate":[10,20]}`))
	if err != nil || click.Kind != desk.KindClick || click.X != 10 || click.Button != "left" {
		t.Fatalf("anthropic click: %+v %v", click, err)
	}
	openAI, err := desk.Parse("computer", json.RawMessage(`{"type":"click","x":4,"y":5,"button":"right"}`))
	if err != nil || openAI.Button != "right" || openAI.X != 4 {
		t.Fatalf("openai click: %+v %v", openAI, err)
	}
	drag, err := desk.Parse("drag", json.RawMessage(`{"x":1,"y":2,"to_x":3,"to_y":4}`))
	if err != nil || !drag.HasDrag || drag.ToX != 3 {
		t.Fatalf("drag: %+v %v", drag, err)
	}
	shot, err := desk.Parse("screenshot", json.RawMessage(`{"format":"jpeg","max_width":640}`))
	if err != nil || shot.Format != "jpeg" || shot.MaxWidth != 640 {
		t.Fatalf("shot: %+v %v", shot, err)
	}
	for _, tc := range []struct{ name, raw string }{
		{"click", `{"x":1}`},
		{"click", `{"x":-1,"y":1}`},
		{"click", `{"x":2000,"y":1}`},
		{"type", `{"text":""}`},
		{"type", `{"text":"a\u0000b"}`},
		{"key", `{"key":"bad key"}`},
		{"scroll", `{}`},
		{"scroll", `{"dy":100}`},
		{"wait", `{"ms":0}`},
		{"wait", `{"ms":11000}`},
		{"browser_navigate", `{"url":"file:///etc/passwd"}`},
		{"browser_navigate", `{"url":"javascript:alert(1)"}`},
		{"browser_fill", `{"selector":"","text":"a"}`},
		{"computer", `{"action":"explode"}`},
		{"nope", `{}`},
		{"click", `not-json`},
	} {
		if _, err = desk.Parse(tc.name, json.RawMessage(tc.raw)); !errors.Is(err, desk.ErrInvalid) {
			t.Fatalf("parse %s %s err=%v", tc.name, tc.raw, err)
		}
	}
	wait, err := desk.Parse("computer", json.RawMessage(`{"action":"wait","duration":1}`))
	if err != nil || wait.Wait != time.Second {
		t.Fatalf("wait %+v %v", wait, err)
	}
}

func TestFormScenarioBrowserAndPixels(t *testing.T) {
	t.Parallel()
	page := desk.NewFormPage()
	script := []desk.Action{
		{Kind: desk.KindBrowserNavigate, URL: desk.FormURL},
		{Kind: desk.KindBrowserFill, Selector: "#name", Text: "Ada"},
		{Kind: desk.KindBrowserFill, Selector: "#email", Text: "ada@example.com"},
		{Kind: desk.KindBrowserClick, Selector: "#submit"},
		{Kind: desk.KindBrowserRead, Selector: "#status"},
	}
	var last desk.Result
	for _, act := range script {
		res, err := page.Apply(act)
		if err != nil {
			t.Fatal(err)
		}
		last = res
	}
	ok, name, email := page.Submitted()
	if !ok || name != "Ada" || email != "ada@example.com" || last.Text != "saved Ada" {
		t.Fatalf("browser form = %v %s %s %q", ok, name, email, last.Text)
	}

	page = desk.NewFormPage()
	pixels := []desk.Action{
		{Kind: desk.KindClick, X: 100, Y: 100, HasPoint: true, Button: "left"},
		{Kind: desk.KindType, Text: "Ada"},
		{Kind: desk.KindClick, X: 100, Y: 160, HasPoint: true, Button: "left"},
		{Kind: desk.KindType, Text: "ada@example.com"},
		{Kind: desk.KindClick, X: 100, Y: 230, HasPoint: true, Button: "left"},
	}
	for _, act := range pixels {
		if _, err := page.Apply(act); err != nil {
			t.Fatal(err)
		}
	}
	ok, name, email = page.Submitted()
	if !ok || name != "Ada" || email != "ada@example.com" {
		t.Fatalf("pixel form = %v %s %s", ok, name, email)
	}
	if _, err := page.Apply(desk.Action{Kind: desk.KindBrowserNavigate, URL: "http://evil.example/form"}); !errors.Is(err, desk.ErrInvalid) {
		t.Fatalf("foreign url err=%v", err)
	}
	empty := desk.NewFormPage()
	res, err := empty.Apply(desk.Action{Kind: desk.KindBrowserClick, Selector: "#submit"})
	if err != nil || res.Text != "missing" {
		t.Fatalf("empty submit %+v %v", res, err)
	}
}
