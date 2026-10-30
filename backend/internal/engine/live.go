package engine

import (
	"bufio"
	"bytes"
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
	var parse func(context.Context, io.Reader) ([]Event, error)
	switch cli {
	case "claude":
		parse = ParseClaudeStream
	case "codex":
		parse = ParseCodexStream
	case "agy":
		parse = func(ctx context.Context, r io.Reader) ([]Event, error) { return ParseAgyStream(ctx, r, nil) }
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
	consume := func(r io.Reader, decode func(context.Context, io.Reader) ([]Event, error)) error {
		reader := bufio.NewReaderSize(r, 64*1024)
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			line, err := readBoundedLine(reader)
			if err != nil && !errors.Is(err, io.EOF) {
				if errors.Is(err, ErrLineTooLong) {
					return WithFailureCode(FailureStreamFormat, err)
				}
				return err
			}
			if len(bytes.TrimSpace(line)) > 0 && decode != nil {
				// Reuse fixture-tested decoders on one bounded record, never a whole turn.
				events, decodeErr := decode(ctx, bytes.NewReader(line))
				if decodeErr != nil {
					return decodeErr
				}
				for _, ev := range events {
					if emitErr := publish(ev); emitErr != nil {
						return emitErr
					}
				}
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
		}
	}
	done := make(chan struct{}, 2)
	run := func(r io.Reader, decode func(context.Context, io.Reader) ([]Event, error)) {
		err := consume(r, decode)
		if err != nil {
			cancel(err)
		}
		done <- struct{}{}
	}
	go run(stdout, parse)
	count := 1
	if stderr != nil {
		count++
		var decode func(context.Context, io.Reader) ([]Event, error)
		if cli == "agy" {
			decode = parseAgyStderr
		}
		go run(stderr, decode)
	}
	for range count {
		<-done
	}
	if err := context.Cause(ctx); err != nil {
		return fmt.Errorf("CLI stream interrupted: %w", err)
	}
	return nil
}
