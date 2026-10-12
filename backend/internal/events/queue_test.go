package events_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/events"
	"github.com/google/uuid"
)

func TestJetStreamQueueAndInterrupt(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	//nolint:gosec // local test NATS uses an internally reserved port and temporary storage
	cmd := exec.CommandContext(ctx, "nats-server", "-js", "-a", "127.0.0.1", "-p", fmt.Sprint(port), "-sd", t.TempDir())
	logs, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = cmd.Wait() })
	ready := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(logs)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "Server is ready") {
				close(ready)
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("NATS did not become ready")
	}
	jobs := make(chan events.TurnNotification, 1)
	interrupted := make(chan uuid.UUID, 1)
	queue, err := events.OpenQueue(fmt.Sprintf("nats://127.0.0.1:%d", port), func(_ context.Context, job events.TurnNotification) error { jobs <- job; return nil }, func(_ uuid.UUID, bot uuid.UUID) { interrupted <- bot })
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if err := queue.Healthy(); err != nil {
		t.Fatal(err)
	}
	ws, bot, turn := uuid.New(), uuid.New(), uuid.New()
	if err = queue.Enqueue(ctx, ws, bot, turn); err != nil {
		t.Fatal(err)
	}
	select {
	case job := <-jobs:
		if job.WorkspaceID != ws || job.BotID != bot || job.TurnID != turn {
			t.Fatalf("job=%+v", job)
		}
	case <-ctx.Done():
		t.Fatal("turn not delivered")
	}
	if err = queue.Interrupt(ctx, ws, bot); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-interrupted:
		if got != bot {
			t.Fatal("wrong bot interrupted")
		}
	case <-ctx.Done():
		t.Fatal("interrupt not delivered")
	}
	if err = queue.Enqueue(ctx, uuid.Nil, bot, turn); err == nil {
		t.Fatal("invalid job accepted")
	}
	queue.Close()
	if err := queue.Healthy(); err == nil {
		t.Fatal("closed queue reported healthy")
	}

}
