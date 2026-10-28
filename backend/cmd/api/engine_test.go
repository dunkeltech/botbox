package main

import (
	"context"
	"github.com/dunkeltech/botbox/backend/internal/compute"
	"github.com/dunkeltech/botbox/backend/internal/engine"
	"testing"
)

func TestModelToolsRejectHostBackedMemoryDriver(t *testing.T) {
	driver, err := compute.NewMemoryDriver()
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	tools := &sandboxTools{driver: driver}
	if _, err = tools.Call(context.Background(), "exec", []byte(`{"command":"must never run"}`)); err == nil {
		t.Fatal("model tool reached host-backed compute")
	}
}

func TestCLIOnSimulatedComputerHasActionableFailure(t *testing.T) {
	driver, err := compute.NewMemoryDriver()
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	err = runCLITurn(context.Background(), nil, nil, nil, driver, engine.Session{}, func(engine.Event) error { return nil })
	if engine.TurnFailureCode(err) != engine.FailureCLIUnavailable {
		t.Fatalf("failure code=%s", engine.TurnFailureCode(err))
	}
}

func TestRequestSecretAcceptsSelfHostedGitLabBrowserOnly(t *testing.T) {
	for _, tt := range []struct {
		name, args    string
		broker, valid bool
	}{
		{"custom browser", `{"kind":"browser_login","adapter_id":"gitlab_web","target_origin":"https://gitlab.example.org","reason":"Review project"}`, true, true},
		{"disabled broker", `{"kind":"browser_login","adapter_id":"gitlab_web","target_origin":"https://gitlab.example.org","reason":"Review project"}`, false, false},
		{"other adapter", `{"kind":"browser_login","adapter_id":"github_web","target_origin":"https://gitlab.example.org","reason":"Review project"}`, true, false},
		{"custom CLI", `{"kind":"cli_egress","adapter_id":"gitlab_api","target_origin":"https://gitlab.example.org","reason":"Review project"}`, true, true},
		{"invalid target type", `{"kind":"browser_login","adapter_id":"gitlab_web","target_origin":3,"reason":"Review project"}`, true, false},
		{"missing reason", `{"kind":"browser_login","adapter_id":"gitlab_web","target_origin":"https://gitlab.example.org"}`, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := handleRequestSecretTool([]byte(tt.args), tt.broker)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v err=%v", tt.valid, err)
			}
		})
	}
}
