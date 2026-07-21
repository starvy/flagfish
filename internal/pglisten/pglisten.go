// Package pglisten is the reconnecting half of Postgres LISTEN/NOTIFY.
//
// A subscription is not a pool resource. It lives on one backend connection, and a restart, a
// failover or an idle-connection reap takes it away without telling anyone: the pool opens a fresh
// connection for the next query, every probe stays green, and the process quietly stops hearing the
// fleet. So anything that LISTENs needs three things this package supplies — a retry loop, a health
// signal a probe can read, and a catch-up for what was announced while it was gone.
package pglisten

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/health"
)

const (
	minBackoff = 200 * time.Millisecond
	maxBackoff = 30 * time.Second

	// A subscription that survives this long counts as a good one, and the backoff starts over.
	// Without the threshold, a server that accepts LISTEN and then drops the connection turns the
	// retry loop into a hot loop that never backs off at all.
	settledAfter = 30 * time.Second
)

// A Pump holds one dedicated LISTEN connection and delivers what arrives on it, forever, across
// however many connections that takes.
type Pump struct {
	Pool    *pgxpool.Pool
	Channel string
	Log     *slog.Logger

	// Health is where the subscription's state is published. Nil is allowed and reports nothing.
	Health *health.Listener

	// OnSubscribe runs after every successful LISTEN, including the first, and before any
	// notification is awaited. NOTIFY has no replay and no queue: whatever fired while the
	// connection was down was delivered to nobody and is gone. A pump that only resubscribes
	// therefore serves stale state until the next unrelated change happens to wake it, which is
	// how a paused CTF keeps accepting flags. This is where that gap is closed. Returning an error
	// fails the whole attempt — the subscription is not honoured until the catch-up succeeded.
	OnSubscribe func(ctx context.Context) error

	// OnNotify receives each notification's payload. It must not block for long: the pump is
	// single-threaded and nothing else is reading the connection while it runs.
	OnNotify func(ctx context.Context, payload string)
}

// Run pumps until ctx is cancelled, reconnecting in between. It returns only when ctx is done, so a
// caller that sees it return during normal operation is looking at a shutdown, not a failure.
func (p *Pump) Run(ctx context.Context) error {
	backoff := minBackoff
	for {
		started := time.Now()
		err := p.session(ctx)
		p.Health.Down()

		if ctx.Err() != nil {
			return fmt.Errorf("pglisten: %s: %w", p.Channel, ctx.Err())
		}
		if time.Since(started) >= settledAfter {
			backoff = minBackoff
		}
		// Error, not warning: between here and the next successful subscribe this process is
		// deaf to the rest of the fleet, and nothing else in the logs will say so.
		p.Log.ErrorContext(ctx, "listen subscription lost; reconnecting",
			"channel", p.Channel, "retry_in", backoff, "error", err)

		if werr := wait(ctx, jitter(backoff)); werr != nil {
			return fmt.Errorf("pglisten: %s: %w", p.Channel, werr)
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// session subscribes and pumps on one connection. It returns the error that ended it; the caller
// decides whether that was a shutdown or something to retry.
func (p *Pump) session(ctx context.Context) error {
	conn, err := p.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire listener: %w", err)
	}
	// Held, never handed back mid-subscription: a connection parked in WaitForNotification cannot
	// serve a query, and giving it to one is a bug that only shows under load.
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{p.Channel}.Sanitize()); err != nil {
		return fmt.Errorf("listen %s: %w", p.Channel, err)
	}
	// Subscribe first, catch up second. The other order leaves a hole exactly the size of the gap
	// between the two: a change committed after the catch-up read but before the LISTEN is
	// announced to a connection that is not listening yet, and then never read again.
	if p.OnSubscribe != nil {
		if err := p.OnSubscribe(ctx); err != nil {
			return fmt.Errorf("catch up on %s: %w", p.Channel, err)
		}
	}
	p.Health.Up()

	for {
		note, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("wait for notification on %s: %w", p.Channel, err)
		}
		p.OnNotify(ctx, note.Payload)
	}
}

// jitter spreads reconnects over [d/2, d] so a fleet that lost one Postgres does not return as a
// thundering herd. The spread is seeded off the wall clock rather than a PRNG — this is a backoff
// nudge, not a secret, and separate processes have different nanosecond clocks, which is all the
// decorrelation a herd needs.
func jitter(d time.Duration) time.Duration {
	half := int64(d / 2)
	if half <= 0 {
		return d
	}
	return d/2 + time.Duration(time.Now().UnixNano()%(half+1))
}

func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err() //nolint:wrapcheck // the caller names the channel
	case <-t.C:
		return nil
	}
}
