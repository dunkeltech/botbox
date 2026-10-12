package enginetest

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/dunkeltech/botbox/backend/internal/engine"
)

// CLI replays recorded stdout/stderr through the production live adapters.
// Gate can hold a turn for queue, interrupt and takeover tests without a sleep.
type CLI struct {
	Name     string
	Stdout   string
	Stderr   string
	ExitCode int
	Gate     <-chan struct{}
}

// Run implements engine.ExecuteTurn and never launches a process.
func (c CLI) Run(ctx context.Context, _ engine.Session, emit func(engine.Event) error) error {
	if c.Gate != nil {
		select {
		case <-c.Gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := engine.Stream(ctx, c.Name, io.NopCloser(strings.NewReader(c.Stdout)), io.NopCloser(strings.NewReader(c.Stderr)), emit); err != nil {
		return err
	}
	if c.ExitCode != 0 {
		return errors.New("fake CLI exited unsuccessfully")
	}
	return ctx.Err()
}
