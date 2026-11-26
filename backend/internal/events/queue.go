// SPDX-License-Identifier: AGPL-3.0-only

package events

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/bots"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
)

type natsHeaderCarrier nats.Header

func (c natsHeaderCarrier) Get(key string) string {
	return nats.Header(c).Get(key)
}

func (c natsHeaderCarrier) Set(key, value string) {
	nats.Header(c).Set(key, value)
}

func (c natsHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

// TurnNotification contains identifiers only; private message text stays in Postgres.
type TurnNotification struct {
	WorkspaceID uuid.UUID
	BotID       uuid.UUID
	TurnID      uuid.UUID
}

// Queue delivers durable per-bot notifications using JetStream work-queue retention.
type Queue struct {
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	closed   bool
	inflight sync.WaitGroup
	conn     *nats.Conn
	js       nats.JetStreamContext
	sub      *nats.Subscription
}

// OpenQueue starts a durable worker. process must make duplicate deliveries harmless
// and must not replay a running turn's side effects after a worker failure.
func OpenQueue(url string, process func(context.Context, TurnNotification) error, interrupt ...func(uuid.UUID, uuid.UUID)) (*Queue, error) {
	return OpenQueueWithConcurrency(url, 4, process, interrupt...)
}

// OpenQueueWithConcurrency bounds active deliveries; the worker owns per-bot ordering.
func OpenQueueWithConcurrency(url string, concurrency int, process func(context.Context, TurnNotification) error, interrupt ...func(uuid.UUID, uuid.UUID)) (*Queue, error) {
	if process == nil {
		return nil, errors.New("turn processor is required")
	}
	if concurrency < 1 || concurrency > 64 {
		return nil, errors.New("turn concurrency must be 1..64")
	}
	nc, err := nats.Connect(url, nats.Timeout(5*time.Second))
	if err != nil {
		return nil, err
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, err
	}
	if _, err = js.AddStream(&nats.StreamConfig{Name: "BOT_TURNS", Subjects: []string{"turns.*.*"}, Retention: nats.WorkQueuePolicy, Storage: nats.FileStorage}); err != nil {
		nc.Close()
		return nil, err
	}
	queueCtx, cancel := context.WithCancel(context.Background())
	q := &Queue{conn: nc, js: js, ctx: queueCtx, cancel: cancel}
	slots := make(chan struct{}, concurrency)
	q.sub, err = js.QueueSubscribe("turns.*.*", "engine", func(msg *nats.Msg) {
		// Wait before starting a goroutine so pending deliveries cannot create
		// unbounded tasks. Cancellation also releases a blocked dispatcher.
		select {
		case slots <- struct{}{}:
		case <-q.ctx.Done():
			return
		}
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			<-slots
			return
		}
		q.inflight.Add(1)
		q.mu.Unlock()
		go func() {
			defer func() { <-slots; q.inflight.Done() }()
			var job TurnNotification
			if json.Unmarshal(msg.Data, &job) != nil || job.WorkspaceID == uuid.Nil || job.BotID == uuid.Nil || job.TurnID == uuid.Nil {
				_ = msg.Term()
				return
			}
			jobCtx := q.ctx
			if len(msg.Header) > 0 {
				jobCtx = otel.GetTextMapPropagator().Extract(jobCtx, natsHeaderCarrier(msg.Header))
			}
			ctx, cancel := context.WithTimeout(jobCtx, bots.MaxTurnTimeout+5*time.Minute)
			// The message may have waited locally for a worker slot.
			_ = msg.InProgress()
			progressDone := make(chan struct{})
			go func() {
				defer close(progressDone)
				// Renew the existing durable consumer's 16-minute ack window
				// without changing its configuration on a running deployment.
				ticker := time.NewTicker(8 * time.Minute)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						_ = msg.InProgress()
					}
				}
			}()
			err := process(ctx, job)
			cancel()
			<-progressDone
			if err != nil {
				_ = msg.NakWithDelay(time.Second)
				return
			}
			_ = msg.Ack()
		}()
	}, nats.Durable("engine"), nats.ManualAck(), nats.AckWait(16*time.Minute))
	if err != nil {
		cancel()
		nc.Close()
		return nil, err
	}
	if len(interrupt) > 0 {
		_, err = nc.Subscribe("control.*.*", func(msg *nats.Msg) {
			var job TurnNotification
			if json.Unmarshal(msg.Data, &job) == nil {
				interrupt[0](job.WorkspaceID, job.BotID)
			}
		})
		if err != nil {
			nc.Close()
			return nil, err
		}
	}

	return q, nil
}

// Enqueue acknowledges durable publication before the caller commits its input.
func (q *Queue) Enqueue(ctx context.Context, ws, bot, turn uuid.UUID) error {
	if ws == uuid.Nil || bot == uuid.Nil || turn == uuid.Nil {
		return errors.New("turn identifiers are required")
	}
	data, err := json.Marshal(TurnNotification{ws, bot, turn})
	if err != nil {
		return err
	}
	// Secret fulfillment queues the same turn again. A turn-based message ID
	// would discard that continuation inside JetStream's duplicate window.
	// The worker's persisted claim makes repeated notifications harmless.
	msg := nats.NewMsg("turns." + ws.String() + "." + bot.String())
	msg.Data = data
	msg.Header = nats.Header{}
	otel.GetTextMapPropagator().Inject(ctx, natsHeaderCarrier(msg.Header))
	_, err = q.js.PublishMsg(msg, nats.Context(ctx))
	return err
}

// Close stops deliveries and closes the NATS connection.
func (q *Queue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cancel()
	q.conn.Close()
	q.inflight.Wait()
}

// PublishConversation emits private events; HTTP subscribers enforce DM ownership.
func (q *Queue) PublishConversation(ctx context.Context, ws, conversation uuid.UUID, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return q.conn.Publish("chat."+ws.String()+"."+conversation.String(), data)
}

// SubscribeConversation registers a bounded consumer for one conversation.
func (q *Queue) SubscribeConversation(ws, conversation uuid.UUID, deliver func([]byte)) (func(), error) {
	sub, err := q.conn.Subscribe("chat."+ws.String()+"."+conversation.String(), func(msg *nats.Msg) { deliver(msg.Data) })
	if err != nil {
		return nil, err
	}
	if err := q.conn.Flush(); err != nil {
		_ = sub.Unsubscribe()
		return nil, err
	}
	return func() { _ = sub.Unsubscribe() }, nil
}

// Interrupt propagates takeover cancellation to the API instance running the bot.
func (q *Queue) Interrupt(ctx context.Context, ws, bot uuid.UUID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(TurnNotification{WorkspaceID: ws, BotID: bot})
	if err != nil {
		return err
	}
	return q.conn.Publish("control."+ws.String()+"."+bot.String(), data)
}

// EgressInvalidationEvent notifies a bot's egress proxy to reload policy and
// drop affected passthrough tunnels for host or revision.
type EgressInvalidationEvent struct {
	WorkspaceID uuid.UUID `json:"workspaceId"`
	BotID       uuid.UUID `json:"botId"`
	Revision    int64     `json:"revision,omitempty"`
	Host        string    `json:"host,omitempty"`
}

// EgressInvalidationAck reports the highest policy revision adopted by a running proxy
// after closing affected passthrough tunnels.
type EgressInvalidationAck struct {
	AcknowledgedRevision int64 `json:"acknowledgedRevision"`
}

// PublishEgressInvalidate notifies a bot's egress proxy to drop raw passthrough tunnels
// for host so newly activated credentials take effect immediately on subsequent requests.
func (q *Queue) PublishEgressInvalidate(ctx context.Context, ws, bot uuid.UUID, host string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ws == uuid.Nil || bot == uuid.Nil || host == "" {
		return nil
	}
	payload, err := json.Marshal(EgressInvalidationEvent{
		WorkspaceID: ws,
		BotID:       bot,
		Host:        host,
	})
	if err != nil {
		return err
	}
	return q.conn.Publish("egress.invalidate."+ws.String()+"."+bot.String(), payload)
}

// PublishEgressPolicyRevision publishes a revision-aware invalidation request and
// waits within ctx for the running proxy to adopt the revision and close affected tunnels.
func (q *Queue) PublishEgressPolicyRevision(ctx context.Context, ws, bot uuid.UUID, revision int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if ws == uuid.Nil || bot == uuid.Nil {
		return 0, errors.New("workspace and bot identifiers are required")
	}
	payload, err := json.Marshal(EgressInvalidationEvent{
		WorkspaceID: ws,
		BotID:       bot,
		Revision:    revision,
	})
	if err != nil {
		return 0, err
	}
	msg, err := q.conn.RequestWithContext(ctx, "egress.invalidate."+ws.String()+"."+bot.String(), payload)
	if err != nil {
		return 0, err
	}
	var ack EgressInvalidationAck
	if err := json.Unmarshal(msg.Data, &ack); err != nil {
		return 0, err
	}
	return ack.AcknowledgedRevision, nil
}

// PublishMCPPolicyInvalidate notifies a bot's egress proxy to drop cached MCP policies.
func (q *Queue) PublishMCPPolicyInvalidate(ctx context.Context, ws, bot uuid.UUID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ws == uuid.Nil || bot == uuid.Nil {
		return nil
	}
	return q.conn.Publish("mcp.invalidate."+ws.String()+"."+bot.String(), []byte("{}"))
}

// PublishGuardrailsInvalidate notifies all running proxies in a workspace to drop
// cached LLM guardrail policies and custom rules.
func (q *Queue) PublishGuardrailsInvalidate(ctx context.Context, ws uuid.UUID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ws == uuid.Nil {
		return nil
	}
	return q.conn.Publish("guardrails.invalidate."+ws.String(), []byte("{}"))
}

// Publisher emits conversation signals without consuming the turn queue.
// The egress proxy uses it so a blocked host can ping the conversation
// channel without starting a second worker.
type Publisher struct {
	conn *nats.Conn
}

// OpenPublisher connects for PublishConversation only.
func OpenPublisher(url string) (*Publisher, error) {
	nc, err := nats.Connect(url, nats.Timeout(5*time.Second))
	if err != nil {
		return nil, err
	}
	return &Publisher{conn: nc}, nil
}

// PublishConversation emits private events; HTTP subscribers enforce membership.
func (p *Publisher) PublishConversation(ctx context.Context, ws, conversation uuid.UUID, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.conn.Publish("chat."+ws.String()+"."+conversation.String(), data)
}

// SubscribeEgressInvalidate invokes onHost whenever an active credential rule changes for a bot.
func (p *Publisher) SubscribeEgressInvalidate(ws, bot uuid.UUID, onHost func(string)) (func(), error) {
	if p == nil || p.conn == nil || onHost == nil {
		return func() {}, nil
	}
	return p.SubscribeEgressPolicyInvalidate(ws, bot, func(_ context.Context, ev EgressInvalidationEvent) (int64, error) {
		if ev.Host != "" {
			onHost(ev.Host)
		}
		return ev.Revision, nil
	})
}

// SubscribeEgressPolicyInvalidate subscribes to revision-aware egress invalidations and
// replies with the proxy's acknowledged revision once adoption and tunnel closure finish.
func (p *Publisher) SubscribeEgressPolicyInvalidate(
	ws, bot uuid.UUID,
	onEvent func(context.Context, EgressInvalidationEvent) (int64, error),
) (func(), error) {
	if p == nil || p.conn == nil || onEvent == nil {
		return func() {}, nil
	}
	sub, err := p.conn.Subscribe("egress.invalidate."+ws.String()+"."+bot.String(), func(msg *nats.Msg) {
		var ev EgressInvalidationEvent
		if err := json.Unmarshal(msg.Data, &ev); err != nil {
			ev = EgressInvalidationEvent{
				WorkspaceID: ws,
				BotID:       bot,
				Host:        strings.TrimSpace(string(msg.Data)),
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ackRev, handleErr := onEvent(ctx, ev)
		if handleErr == nil && msg.Reply != "" {
			if ackBytes, err := json.Marshal(EgressInvalidationAck{AcknowledgedRevision: ackRev}); err == nil {
				_ = msg.Respond(ackBytes)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if err := p.conn.Flush(); err != nil {
		_ = sub.Unsubscribe()
		return nil, err
	}
	return func() { _ = sub.Unsubscribe() }, nil
}

// SubscribeMCPPolicyInvalidate invokes onInvalidate whenever a bot's MCP policy changes.
func (p *Publisher) SubscribeMCPPolicyInvalidate(ws, bot uuid.UUID, onInvalidate func()) (func(), error) {
	if p == nil || p.conn == nil || onInvalidate == nil {
		return func() {}, nil
	}
	sub, err := p.conn.Subscribe("mcp.invalidate."+ws.String()+"."+bot.String(), func(_ *nats.Msg) {
		onInvalidate()
	})
	if err != nil {
		return nil, err
	}
	if err := p.conn.Flush(); err != nil {
		_ = sub.Unsubscribe()
		return nil, err
	}
	return func() { _ = sub.Unsubscribe() }, nil
}

// SubscribeGuardrailsInvalidate invokes onInvalidate whenever a workspace's LLM guardrail policy or rules change.
func (p *Publisher) SubscribeGuardrailsInvalidate(ws uuid.UUID, onInvalidate func()) (func(), error) {
	if p == nil || p.conn == nil || ws == uuid.Nil || onInvalidate == nil {
		return func() {}, nil
	}
	sub, err := p.conn.Subscribe("guardrails.invalidate."+ws.String(), func(_ *nats.Msg) {
		onInvalidate()
	})
	if err != nil {
		return nil, err
	}
	if err := p.conn.Flush(); err != nil {
		_ = sub.Unsubscribe()
		return nil, err
	}
	return func() { _ = sub.Unsubscribe() }, nil
}

// Close closes the NATS connection.
func (p *Publisher) Close() {
	if p != nil && p.conn != nil {
		p.conn.Close()
	}
}

// Healthy reports whether durable scheduling can reach NATS.
func (q *Queue) Healthy() error {
	if !q.conn.IsConnected() {
		return errors.New("turn queue unavailable")
	}
	return nil
}
