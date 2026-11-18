// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Stream consumes CLI output incrementally and serializes calls to emit. It owns
// and closes both readers, including on cancellation or sink failure. Readers
// must unblock Read when closed; emit must return promptly when ctx is canceled.
// Stderr is drained concurrently because a full stderr pipe can stall stdout.
func Stream(ctx context.Context, cli string, stdout, stderr io.ReadCloser, emit func(Event) error) error {
	var closeOnce sync.Once
	closeReaders := func() {
		closeOnce.Do(func() {
			if stdout != nil {
				_ = stdout.Close()
			}
			if stderr != nil {
				_ = stderr.Close()
			}
		})
	}
	defer closeReaders()
	if stdout == nil || emit == nil {
		return errors.New("stdout and event sink are required")
	}
	var decode func([]byte) ([]Event, error)
	var pi piStream
	switch cli {
	case "claude":
		decode = decodeClaudeLine
	case "codex":
		decode = decodeCodexLine
	case "pi":
		// Keep Pi's state for the full turn to distinguish transient and terminal errors.
		decode = pi.parseLine
	case "agy":
		decode = decodeAgyLine
	default:
		return errors.New("unsupported CLI")
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stopClosing := context.AfterFunc(ctx, closeReaders)
	defer stopClosing()
	var mu sync.Mutex
	publish := func(ev Event) error {
		mu.Lock()
		defer mu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		return emit(ev)
	}
	done := make(chan struct{}, 2)
	run := func(source string, r io.Reader, decodeLine func([]byte) ([]Event, error)) {
		if err := consumeStreamLines(ctx, source, r, decodeLine, publish); err != nil {
			cancel(err)
		}
		done <- struct{}{}
	}
	go run(cli, stdout, decode)
	count := 1
	if stderr != nil {
		count++
		var decodeStderr func([]byte) ([]Event, error)
		if cli == "agy" {
			decodeStderr = decodeAgyStderrLine
		}
		go run(cli+" stderr", stderr, decodeStderr)
	}
	for range count {
		<-done
	}
	if err := context.Cause(ctx); err != nil {
		return fmt.Errorf("CLI stream interrupted: %w", err)
	}
	if cli == "pi" {
		return pi.finish()
	}
	return nil
}
