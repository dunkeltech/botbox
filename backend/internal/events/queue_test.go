// SPDX-License-Identifier: AGPL-3.0-only

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

	"github.com/dunkeltech/botbox/backend/internal/bots"
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
	minimumDeadline := time.Now().Add(bots.MaxTurnTimeout)
	queue, err := events.OpenQueue(fmt.Sprintf("nats://127.0.0.1:%d", port), func(ctx context.Context, job events.TurnNotification) error {
		if deadline, ok := ctx.Deadline(); !ok || deadline.Before(minimumDeadline) {
			t.Errorf("delivery deadline %s cannot fit a maximum-length turn", deadline)
		}
		jobs <- job
		return nil
	}, func(_ uuid.UUID, bot uuid.UUID) { interrupted <- bot })
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
	// A fulfilled secret resumes the same turn. JetStream must deliver that
	// notification even inside its duplicate window; the worker owns claiming.
	if err = queue.Enqueue(ctx, ws, bot, turn); err != nil {
		t.Fatal(err)
	}
	select {
	case job := <-jobs:
		if job.WorkspaceID != ws || job.BotID != bot || job.TurnID != turn {
			t.Fatalf("resumed job=%+v", job)
		}
	case <-ctx.Done():
		t.Fatal("resumed turn not delivered")
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

func TestEgressPolicyRevisionInvalidationAndAck(t *testing.T) {
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

	natsURL := fmt.Sprintf("nats://127.0.0.1:%d", port)
	queue, err := events.OpenQueue(natsURL, func(context.Context, events.TurnNotification) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()

	pub, err := events.OpenPublisher(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()

	ws, bot := uuid.New(), uuid.New()
	var currentAck int64
	unsub, err := pub.SubscribeEgressPolicyInvalidate(ws, bot, func(_ context.Context, ev events.EgressInvalidationEvent) (int64, error) {
		if ev.Revision > currentAck {
			currentAck = ev.Revision
		}
		return currentAck, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()

	ack1, err := queue.PublishEgressPolicyRevision(ctx, ws, bot, 2)
	if err != nil || ack1 != 2 {
		t.Fatalf("ack1=%d err=%v, want 2", ack1, err)
	}
	// Stale/duplicate revision keeps highest acknowledged revision.
	ack2, err := queue.PublishEgressPolicyRevision(ctx, ws, bot, 1)
	if err != nil || ack2 != 2 {
		t.Fatalf("ack2=%d err=%v, want 2", ack2, err)
	}
}

func startQueueTestNATS(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
	return fmt.Sprintf("nats://127.0.0.1:%d", port)
}

func TestQueueRunsDifferentBotsConcurrentlyAndCancelsOnClose(t *testing.T) {
	url := startQueueTestNATS(t)
	started := make(chan uuid.UUID, 3)
	finished := make(chan struct{}, 3)
	queue, err := events.OpenQueue(url, func(ctx context.Context, job events.TurnNotification) error {
		started <- job.BotID
		<-ctx.Done()
		finished <- struct{}{}
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ws := uuid.New()
	for range 3 {
		if err := queue.Enqueue(ctx, ws, uuid.New(), uuid.New()); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("bots did not start concurrently")
		}
	}
	queue.Close()
	for range 3 {
		select {
		case <-finished:
		default:
			t.Fatal("close returned before processor stopped")
		}
	}
}

func TestQueueRejectsInvalidConcurrency(t *testing.T) {
	for _, limit := range []int{-1, 0, 65} {
		if _, err := events.OpenQueueWithConcurrency("invalid", limit, func(context.Context, events.TurnNotification) error { return nil }); err == nil {
			t.Errorf("accepted concurrency %d", limit)
		}
	}
}
