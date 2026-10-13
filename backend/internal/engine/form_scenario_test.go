package engine_test

import (
	"context"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/desk"
	"github.com/dunkeltech/botbox/backend/internal/engine"
)

type formController struct{ page *desk.FormPage }

func (f formController) Do(_ context.Context, act engine.DesktopAction) (desk.Result, error) {
	if act.Kind == desk.KindScreenshot {
		return f.page.Apply(desk.Action{Kind: desk.KindScreenshot})
	}
	return f.page.Apply(act)
}

func TestAPIModeFormScenario(t *testing.T) {
	t.Parallel()
	page := desk.NewFormPage()
	model := &fakeComputerUseModel{decisions: []engine.ModelDecision{
		{Action: &desk.Action{Kind: desk.KindBrowserNavigate, URL: desk.FormURL}},
		{Action: &desk.Action{Kind: desk.KindBrowserFill, Selector: "#name", Text: "Ada"}},
		{Action: &desk.Action{Kind: desk.KindBrowserFill, Selector: "#email", Text: "ada@example.com"}},
		{Action: &desk.Action{Kind: desk.KindBrowserClick, Selector: "#submit"}},
		{Action: &desk.Action{Kind: desk.KindBrowserRead, Selector: "#status"}},
		{Done: true, Summary: "saved Ada"},
	}}
	runner := &engine.ComputerUseRunner{MaxSteps: 8, Desktop: formController{page: page}, Model: model}
	summary, executed, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ok, name, email := page.Submitted()
	if summary != "saved Ada" || !ok || name != "Ada" || email != "ada@example.com" || len(executed) != 5 {
		t.Fatalf("summary=%q submitted=%v %s %s steps=%d", summary, ok, name, email, len(executed))
	}
}
