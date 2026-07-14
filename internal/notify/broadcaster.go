package notify

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// subscriberBuffer is how many undelivered notifications a client may fall behind before it is
// dropped. Notifications are rare and small; a client that cannot keep up with a handful of them
// is not going to catch up, so the buffer is a slack allowance, not a queue to grow.
const subscriberBuffer = 16

// A Broadcaster owns the single dedicated LISTEN connection and fans received notifications out to
// the in-process SSE subscribers.
//
// The one rule that shapes everything here: the pump must never block on a subscriber. A stalled
// client cannot be allowed to wedge delivery for every other client, so a send that would block is
// a drop — loud, logged, and final — never a wait.
type Broadcaster struct {
	pool *pgxpool.Pool
	svc  *Service
	log  *slog.Logger

	mu      sync.Mutex
	subs    map[*subscriber]struct{}
	stopped bool
}

type subscriber struct {
	ch chan Notification
}

func NewBroadcaster(pool *pgxpool.Pool, svc *Service, log *slog.Logger) *Broadcaster {
	return &Broadcaster{pool: pool, svc: svc, log: log, subs: map[*subscriber]struct{}{}}
}

// Subscribe registers a client and returns its receive channel plus the function that unregisters
// it. The caller must call unsubscribe on disconnect — every subscriber that is added is removed,
// or the map grows without bound. The channel is closed exactly once: by whichever of a drop, a
// shutdown, or the returned unsubscribe happens first.
func (b *Broadcaster) Subscribe() (events <-chan Notification, unsubscribe func()) {
	sub := &subscriber{ch: make(chan Notification, subscriberBuffer)}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		// Shutdown is already underway: hand back a closed channel so a late connector returns at
		// once rather than blocking a drain that will never feed it.
		close(sub.ch)
		return sub.ch, func() {}
	}
	b.subs[sub] = struct{}{}
	return sub.ch, func() { b.remove(sub) }
}

// remove unregisters a subscriber and closes its channel, once. The membership check makes it
// idempotent, so the SSE handler's deferred unsubscribe is safe even after a drop or a shutdown
// already closed the channel.
func (b *Broadcaster) remove(sub *subscriber) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subs[sub]; ok {
		delete(b.subs, sub)
		close(sub.ch)
	}
}

// broadcast fans one notification out. It holds the lock for the whole pass so that a concurrent
// remove cannot close a channel between the select and the send — closing and sending are ordered
// by the same mutex, which is what keeps a send-on-closed-channel panic impossible.
func (b *Broadcaster) broadcast(n Notification) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for sub := range b.subs {
		select {
		case sub.ch <- n:
		default:
			// The client is behind. Drop it rather than stall the pump for everyone else, and say
			// so — a silently dropped stream is a bug you find out about from a user.
			b.log.Warn("dropping a notification subscriber that fell behind")
			delete(b.subs, sub)
			close(sub.ch)
		}
	}
}

// closeAll terminates every subscriber at shutdown, so the SSE handlers observe their channel
// close and return instead of hanging the server's drain. After it runs, Subscribe hands out
// already-closed channels.
func (b *Broadcaster) closeAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopped = true
	for sub := range b.subs {
		delete(b.subs, sub)
		close(sub.ch)
	}
}

// Run holds the dedicated LISTEN connection and pumps notifications until ctx is cancelled.
//
// The connection is acquired from the pool and never released back while listening: a connection
// parked in WaitForNotification is not usable for a query, and handing it to one would be a bug
// that only shows under load. It is released when Run returns, on shutdown.
func (b *Broadcaster) Run(ctx context.Context) error {
	conn, err := b.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("notify: acquire listener: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN "+channel); err != nil {
		return fmt.Errorf("notify: listen: %w", err)
	}

	for {
		note, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			// Includes ctx cancellation at shutdown; the caller unwraps to decide whether it was
			// expected.
			return fmt.Errorf("notify: wait for notification: %w", err)
		}

		id, perr := strconv.ParseInt(note.Payload, 10, 64)
		if perr != nil {
			b.log.WarnContext(ctx, "notify: unparseable payload", "payload", note.Payload)
			continue
		}

		n, gerr := b.svc.Get(ctx, id)
		if gerr != nil {
			// A row deleted between signal and reload is not an error worth stopping the pump for;
			// anything else is, but the pump's job is to keep delivering, so log and carry on.
			b.log.WarnContext(ctx, "notify: could not load a signalled notification", "id", id, "error", gerr)
			continue
		}
		b.broadcast(n)
	}
}
