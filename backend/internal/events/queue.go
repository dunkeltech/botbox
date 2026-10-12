package events

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

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
	if process == nil {
		return nil, errors.New("turn processor is required")
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
	q.sub, err = js.QueueSubscribe("turns.*.*", "engine", func(msg *nats.Msg) {
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return
		}
		q.inflight.Add(1)
		q.mu.Unlock()
		defer q.inflight.Done()
		var job TurnNotification
		if json.Unmarshal(msg.Data, &job) != nil || job.WorkspaceID == uuid.Nil || job.BotID == uuid.Nil || job.TurnID == uuid.Nil {
			_ = msg.Term()
			return
		}
		ctx, cancel := context.WithTimeout(q.ctx, 15*time.Minute)
		defer cancel()
		if err := process(ctx, job); err != nil {
			_ = msg.NakWithDelay(time.Second)
			return
		}
		_ = msg.Ack()
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
	_, err = q.js.Publish("turns."+ws.String()+"."+bot.String(), data, nats.Context(ctx), nats.MsgId(turn.String()))
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

// Healthy reports whether durable scheduling can reach NATS.
func (q *Queue) Healthy() error {
	if !q.conn.IsConnected() {
		return errors.New("turn queue unavailable")
	}
	return nil
}
