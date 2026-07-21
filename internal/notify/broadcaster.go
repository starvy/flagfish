package notify

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/health"
	"github.com/starvy/flagfish/internal/pglisten"
)

// subscriberBuffer is how many undelivered notifications a client may fall behind before it is
// dropped. Notifications are rare and small; a client that cannot keep up with a handful of them
// is not going to catch up, so the buffer is a slack allowance, not a queue to grow.
const subscriberBuffer = 16

// catchUpLimit caps the replay after a reconnect. A long outage must not turn one resubscribe into
// an unbounded read and a burst that overruns every subscriber's buffer; past the cap the pump says
// so and jumps to the head.
const catchUpLimit = 100

// A Broadcaster owns the single dedicated LISTEN connection and fans received notifications out to
// the in-process SSE subscribers.
//
// The one rule that shapes everything here: the pump must never block on a subscriber. A stalled
// client cannot be allowed to wedge delivery for every other client, so a send that would block is
// a drop — loud, logged, and final — never a wait.
type Broadcaster struct {
	pool   *pgxpool.Pool
	svc    *Service
	log    *slog.Logger
	health *health.Listener

	mu      sync.Mutex
	subs    map[*subscriber]struct{}
	stopped bool

	// lastID is the highest id fanned out so far — the watermark a reconnect replays from.
	// Negative means "not seeded yet", which is how the first subscribe tells itself apart from
	// every later one.
	lastID atomic.Int64
}

type subscriber struct {
	ch chan Notification
}

// NewBroadcaster builds the fan-out. reg may be nil, in which case the pump's subscription state is
// simply not published — a narrow harness that has no readiness probe to feed.
func NewBroadcaster(pool *pgxpool.Pool, svc *Service, log *slog.Logger, reg *health.Registry) *Broadcaster {
	b := &Broadcaster{
		pool: pool, svc: svc, log: log,
		health: reg.Listener(channel),
		subs:   map[*subscriber]struct{}{},
	}
	b.lastID.Store(-1)
	return b
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

// Run pumps notifications until ctx is cancelled, reconnecting for as long as that takes. It
// returns only on shutdown — a dropped connection is a reconnect, not the end of delivery, because
// a pump that gives up leaves every connected client watching a stream that will never move again.
func (b *Broadcaster) Run(ctx context.Context) error {
	p := &pglisten.Pump{
		Pool:        b.pool,
		Channel:     channel,
		Log:         b.log,
		Health:      b.health,
		OnSubscribe: b.catchUp,
		OnNotify:    b.deliver,
	}
	return p.Run(ctx)
}

// catchUp runs after every subscribe. The first one only seeds the watermark: nothing is connected
// yet, so there is nothing to be behind on. Every later one replays what the dead connection was
// never told about — signals are not queued for a listener that is gone, and the clients on the
// other side of this process stay connected across the outage and would silently never see them.
func (b *Broadcaster) catchUp(ctx context.Context) error {
	latest, err := b.svc.LatestID(ctx)
	if err != nil {
		return err
	}
	if b.lastID.Load() < 0 {
		b.lastID.Store(latest)
		return nil
	}

	missed, err := b.svc.After(ctx, b.lastID.Load(), catchUpLimit)
	if err != nil {
		return err
	}
	for _, n := range missed {
		b.advance(n.ID)
		b.broadcast(n)
	}
	if len(missed) == catchUpLimit {
		b.log.WarnContext(ctx, "notify: more was published during the outage than the pump replays; skipping the rest",
			"replayed", len(missed), "resuming_at", latest)
		b.advance(latest)
	}
	return nil
}

// deliver turns one signalled id into a fan-out.
func (b *Broadcaster) deliver(ctx context.Context, payload string) {
	id, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		b.log.WarnContext(ctx, "notify: unparseable payload", "payload", payload)
		return
	}

	n, err := b.svc.Get(ctx, id)
	if err != nil {
		// A row deleted between signal and reload is not an error worth stopping the pump for, and
		// the watermark still moves past it. Anything else keeps the watermark where it is, so a
		// reconnect's catch-up gets a second chance at the row.
		b.log.WarnContext(ctx, "notify: could not load a signalled notification", "id", id, "error", err)
		if errors.Is(err, ErrNotFound) {
			b.advance(id)
		}
		return
	}
	b.advance(id)
	b.broadcast(n)
}

// advance raises the watermark, never lowers it: a catch-up and a live notification can arrive for
// the same id, and the replay must not rewind past what was already delivered.
func (b *Broadcaster) advance(id int64) {
	for {
		cur := b.lastID.Load()
		if id <= cur || b.lastID.CompareAndSwap(cur, id) {
			return
		}
	}
}
