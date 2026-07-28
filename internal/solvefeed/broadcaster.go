package solvefeed

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/health"
	"github.com/starvy/flagfish/internal/pglisten"
)

// subscriberBuffer is how far behind a client may fall before it is dropped. Solves arrive in bursts
// — a challenge unlocks and fifty teams land it inside a minute — so the buffer is sized for a burst
// rather than for the average, but it is still slack, not a queue to grow.
const subscriberBuffer = 64

// A Broadcaster owns the dedicated LISTEN connection and fans solves out to the connected SSE
// clients.
//
// One rule shapes all of it: the pump must never block on a subscriber. A stalled client cannot be
// allowed to wedge delivery for everyone else, so a send that would block is a drop — logged, and
// final — never a wait.
type Broadcaster struct {
	pool   *pgxpool.Pool
	log    *slog.Logger
	health *health.Listener

	mu      sync.Mutex
	subs    map[*subscriber]struct{}
	stopped bool
}

type subscriber struct {
	ch chan Event
}

// NewBroadcaster builds the fan-out. reg may be nil, which simply publishes no subscription state.
func NewBroadcaster(pool *pgxpool.Pool, log *slog.Logger, reg *health.Registry) *Broadcaster {
	return &Broadcaster{
		pool: pool, log: log,
		health: reg.Listener(channel),
		subs:   map[*subscriber]struct{}{},
	}
}

// Subscribe registers a client and returns its channel plus the function that unregisters it. The
// caller must call unsubscribe on disconnect, or the map grows without bound. The channel is closed
// exactly once, by whichever of a drop, a shutdown or the returned unsubscribe happens first.
func (b *Broadcaster) Subscribe() (events <-chan Event, unsubscribe func()) {
	sub := &subscriber{ch: make(chan Event, subscriberBuffer)}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		// Shutdown is underway: hand back a closed channel so a late connector returns at once
		// rather than waiting on a drain that will never feed it.
		close(sub.ch)
		return sub.ch, func() {}
	}
	b.subs[sub] = struct{}{}
	return sub.ch, func() { b.remove(sub) }
}

// remove unregisters a subscriber and closes its channel, once. The membership check is what makes
// the SSE handler's deferred unsubscribe safe after a drop or a shutdown already closed it.
func (b *Broadcaster) remove(sub *subscriber) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subs[sub]; ok {
		delete(b.subs, sub)
		close(sub.ch)
	}
}

// broadcast fans one solve out. The lock is held for the whole pass so a concurrent remove cannot
// close a channel between the select and the send — closing and sending are ordered by the same
// mutex, which is what makes a send-on-closed-channel panic impossible.
func (b *Broadcaster) broadcast(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for sub := range b.subs {
		select {
		case sub.ch <- e:
		default:
			b.log.Warn("dropping a solve-feed subscriber that fell behind", "solve_id", e.SolveID)
			delete(b.subs, sub)
			close(sub.ch)
		}
	}
}

// closeAll terminates every subscriber at shutdown so the SSE handlers observe their channel close
// and return, instead of hanging the server's drain.
func (b *Broadcaster) closeAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopped = true
	for sub := range b.subs {
		delete(b.subs, sub)
		close(sub.ch)
	}
}

// Run pumps solves until ctx is cancelled, reconnecting for as long as that takes.
//
// There is no OnSubscribe catch-up, and that is the deliberate difference from the notifications
// pump. Solves that landed while the connection was down were delivered to nobody and are gone; the
// board and the scoreboard are where a client learns the state of the world, and this feed only ever
// claims to say what is happening right now.
func (b *Broadcaster) Run(ctx context.Context) error {
	p := &pglisten.Pump{
		Pool:     b.pool,
		Channel:  channel,
		Log:      b.log,
		Health:   b.health,
		OnNotify: b.deliver,
	}
	return p.Run(ctx)
}

// deliver turns one signalled payload into a fan-out.
func (b *Broadcaster) deliver(ctx context.Context, payload string) {
	var e Event
	if err := json.Unmarshal([]byte(payload), &e); err != nil {
		// Never silent: a payload this process cannot read means a publisher and a listener
		// disagree about the wire format, and the visible symptom is only a feed that stopped
		// pulsing.
		b.log.ErrorContext(ctx, "solvefeed: unparseable payload", "payload", payload, "error", err)
		return
	}
	b.broadcast(e)
}
