package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dunkeltech/botbox/backend/internal/agent"
	"github.com/dunkeltech/botbox/backend/internal/desk"
	agentv1 "github.com/dunkeltech/botbox/backend/internal/gen/proto/agent/v1"
)

func TestDesktopActionScalesJournalsAndRejectsTakeover(t *testing.T) {
	t.Parallel()
	runner := &fakeCmdRunner{}
	desktop := agent.NewDesktopService(runner)
	desktop.SetJournal(agent.NewJournal("sess"))
	desktop.SetControl(agent.NewControlGate(""))
	server := agent.NewServerWithOptions(agent.ServerOptions{Desktop: desktop})

	resp, err := server.DesktopAction(context.Background(), &agentv1.DesktopActionRequest{
		Kind:        "click",
		PayloadJson: `{"x":319,"y":199,"image_width":640,"image_height":400}`,
	})
	if err != nil || resp.ErrorCode != "" {
		t.Fatalf("click resp=%v err=%v", resp, err)
	}
	runner.mu.Lock()
	joined := strings.Join(runner.calls[0].args, " ")
	runner.mu.Unlock()
	if !strings.Contains(joined, "638") || !strings.Contains(joined, "398") {
		t.Fatalf("scaled args %s", joined)
	}
	pulled, err := server.PullDesktopActions(context.Background(), &agentv1.PullDesktopActionsRequest{})
	if err != nil || len(pulled.Records) != 1 || pulled.Records[0].Kind != "click" || !pulled.Records[0].HasCoord {
		t.Fatalf("pull %+v err=%v", pulled, err)
	}
	if _, err = server.AckDesktopActions(context.Background(), &agentv1.AckDesktopActionsRequest{SessionId: pulled.SessionId, ThroughSeq: pulled.Records[0].Seq}); err != nil {
		t.Fatal(err)
	}
	pulled, err = server.PullDesktopActions(context.Background(), &agentv1.PullDesktopActionsRequest{})
	if err != nil || len(pulled.Records) != 0 {
		t.Fatalf("ack left %+v", pulled)
	}

	if _, err = server.DesktopAction(context.Background(), &agentv1.DesktopActionRequest{Kind: "click", PayloadJson: `{"x":1}`}); err != nil {
		t.Fatal(err)
	}
	bad, err := server.DesktopAction(context.Background(), &agentv1.DesktopActionRequest{Kind: "click", PayloadJson: `{"x":1}`})
	if err != nil || bad.ErrorCode != "validation" {
		t.Fatalf("invalid %+v %v", bad, err)
	}
	desktop.SetHuman(true)
	locked, err := server.DesktopAction(context.Background(), &agentv1.DesktopActionRequest{Kind: "click", PayloadJson: `{"x":1,"y":1}`})
	if err != nil || locked.ErrorCode != "control.locked" {
		t.Fatalf("locked %+v %v", locked, err)
	}
}

func TestTypeDelayIsPassedToXdotool(t *testing.T) {
	t.Parallel()
	runner := &fakeCmdRunner{}
	desktop := agent.NewDesktopService(runner)
	if _, err := desktop.Perform(context.Background(), mustType(t)); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 1 || !strings.Contains(strings.Join(runner.calls[0].args, " "), "--delay 40") {
		t.Fatalf("args %+v", runner.calls)
	}
}

func TestTypeDelayZeroUsesDefault(t *testing.T) {
	t.Parallel()
	runner := &fakeCmdRunner{}
	desktop := agent.NewDesktopService(runner)
	act, err := desk.Parse("type", []byte(`{"text":"Ada","delay_ms":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = desktop.Perform(context.Background(), act); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 1 || !strings.Contains(strings.Join(runner.calls[0].args, " "), "--delay 12") {
		t.Fatalf("args %+v", runner.calls)
	}
}
