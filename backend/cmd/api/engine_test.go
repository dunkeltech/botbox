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
