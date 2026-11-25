// SPDX-License-Identifier: AGPL-3.0-only

package computermcp

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// Terminal reads output and writes input for one cancellable sandbox process.
type Terminal interface {
	Receive() (TerminalFrame, error)
	SendInput(data []byte, closeInput bool) error
	Close()
}

// TerminalFrame carries bounded process output or its exit status.
type TerminalFrame struct {
	Data      []byte
	Stderr    bool
	Truncated bool
	Exited    bool
	ExitCode  int32
}

const (
	maxLiveTerminals  = 8
	maxSavedTerminals = 16
	maxTerminalBytes  = 1 << 20
	pageBytes         = 64 << 10
)

type terminalJob struct {
	stream Terminal
	once   sync.Once
	done   chan struct{}

	mu        sync.Mutex
	frames    []TerminalFrame
	size      int
	truncated bool
	finished  bool
	exitCode  *int32
	failed    bool
}

func (j *terminalJob) stop() { j.once.Do(j.stream.Close) }

func (j *terminalJob) collect() {
	defer close(j.done)
	defer j.stop()
	for {
		frame, err := j.stream.Receive()
		j.mu.Lock()
		if err != nil || frame.Exited {
			j.finished = true
			if err != nil {
				j.failed = true
			} else {
				j.exitCode = &frame.ExitCode
			}
			j.mu.Unlock()
			return
		}
		j.truncated = j.truncated || frame.Truncated || len(frame.Data) > maxTerminalBytes-j.size
		size := min(maxTerminalBytes-j.size, len(frame.Data))
		if size > 0 {
			frame.Data = append([]byte(nil), frame.Data[:size]...)
			j.frames = append(j.frames, frame)
			j.size += size
		}
		j.mu.Unlock()
	}
}

type terminalOutput struct {
	ID        string `json:"terminal_id"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Next      int    `json:"next_offset"`
	Finished  bool   `json:"finished"`
	Failed    bool   `json:"failed,omitempty"`
	ExitCode  *int32 `json:"exit_code,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

func (j *terminalJob) read(id string, offset int) (terminalOutput, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if offset > len(j.frames) {
		return terminalOutput{}, errInvalid
	}
	out := terminalOutput{ID: id, Finished: j.finished, Failed: j.failed, ExitCode: j.exitCode, Truncated: j.truncated, Next: offset}
	var stdout, stderr strings.Builder
	for _, frame := range j.frames[offset:] {
		if stdout.Len()+stderr.Len()+len(frame.Data) > pageBytes && out.Next > offset {
			break
		}
		if frame.Stderr {
			stderr.Write(frame.Data)
		} else {
			stdout.Write(frame.Data)
		}
		out.Next++
	}
	out.Stdout, out.Stderr = printableOutput([]byte(stdout.String())), printableOutput([]byte(stderr.String()))
	return out, nil
}

func (s *Session) command(ctx context.Context, name string, cmd Command) (toolResult, error) {
	if !s.makeRoom() {
		return toolResult{}, errors.New("too many running terminals")
	}
	stream, err := s.backend.OpenTerminal(ctx, s.target, cmd)
	if err != nil {
		return toolResult{}, err
	}
	id := uuid.NewString()
	job := &terminalJob{stream: stream, done: make(chan struct{})}
	s.jobs[id] = job
	s.order = append(s.order, id)
	go job.collect()
	if name == toolExec {
		if err := stream.SendInput(nil, true); err != nil && !errors.Is(err, io.EOF) {
			job.stop()
			return toolResult{}, err
		}
		select {
		case <-job.done:
		case <-ctx.Done():
			job.stop()
			return toolResult{}, ctx.Err()
		}
	}
	out, err := job.read(id, 0)
	if err != nil {
		return toolResult{}, err
	}
	return jsonResult(out)
}

func (s *Session) makeRoom() bool {
	active := 0
	disposable := -1
	for index, id := range s.order {
		select {
		case <-s.jobs[id].done:
			if disposable == -1 {
				disposable = index
			}
		default:
			active++
		}
	}
	if active >= maxLiveTerminals {
		return false
	}
	if len(s.order) == maxSavedTerminals && disposable >= 0 {
		delete(s.jobs, s.order[disposable])
		s.order = append(s.order[:disposable], s.order[disposable+1:]...)
	}
	return len(s.order) < maxSavedTerminals
}

func (s *Session) terminal(name string, args terminalArguments) (toolResult, error) {
	job := s.jobs[args.ID]
	if job == nil {
		return toolResult{}, errInvalid
	}
	switch name {
	case toolTerminalSend:
		if err := job.stream.SendInput([]byte(args.Input), args.CloseInput); err != nil {
			return toolResult{}, err
		}
		return textResult("Input sent"), nil
	case toolTerminalStop:
		job.stop()
		return textResult("Terminal stopped"), nil
	default:
		out, err := job.read(args.ID, args.Offset)
		if err != nil {
			return toolResult{}, err
		}
		return jsonResult(out)
	}
}
