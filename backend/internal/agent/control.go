// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

// ErrControlLocked rejects desktop work while a human holds the control lock.
// Takeover cancels the action already in flight so input stops inside one second.
var ErrControlLocked = errors.New("desktop control lock held by human")

// ControlLockFile is read by every desktop call and written when the API
// pushes a takeover. One file keeps the in-process MCP server and any CLI
// helper on the same lock without giving the sandbox another network hop.
const ControlLockFile = "/tmp/botbox-desktop-control"

// ControlGate is the in-sandbox half of the Phase 2 control lock.
type ControlGate struct {
	mu       sync.Mutex
	human    atomic.Bool
	live     context.Context
	cancel   context.CancelFunc
	lockFile string
}

// NewControlGate starts with the bot allowed to drive the desktop.
func NewControlGate(lockFile string) *ControlGate {
	ctx, cancel := context.WithCancel(context.Background())
	return &ControlGate{live: ctx, cancel: cancel, lockFile: lockFile}
}

// SetHuman records who may act and cancels bot input when a human takes over.
func (g *ControlGate) SetHuman(held bool) {
	if g == nil {
		return
	}
	g.mu.Lock()
	if held == g.human.Load() && g.live.Err() == nil {
		g.mu.Unlock()
		g.writeFile(held)
		return
	}
	g.human.Store(held)
	if held {
		g.cancel()
	} else {
		g.live, g.cancel = context.WithCancel(context.Background())
	}
	g.mu.Unlock()
	g.writeFile(held)
}

// Human reports the lock, including a lock file written by another process.
func (g *ControlGate) Human() bool {
	if g == nil {
		return false
	}
	if g.human.Load() {
		return true
	}
	if g.lockFile == "" {
		return false
	}
	raw, err := os.ReadFile(g.lockFile)
	return err == nil && strings.TrimSpace(string(raw)) == "human"
}

// Bind returns a context that ends when the caller cancels or a human takes control.
func (g *ControlGate) Bind(parent context.Context) (context.Context, func(), error) {
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}
	if g == nil {
		return parent, func() {}, nil
	}
	if g.Human() {
		return nil, nil, ErrControlLocked
	}
	g.mu.Lock()
	live := g.live
	g.mu.Unlock()
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(live, cancel)
	if live.Err() != nil || g.Human() {
		stop()
		cancel()
		return nil, nil, ErrControlLocked
	}
	return ctx, func() {
		stop()
		cancel()
	}, nil
}

func (g *ControlGate) writeFile(held bool) {
	if g.lockFile == "" {
		return
	}
	value := "bot\n"
	if held {
		value = "human\n"
	}
	// Best-effort: the database lock still blocks API-mode tools if this fails.
	_ = os.WriteFile(g.lockFile, []byte(value), 0o600)
}
