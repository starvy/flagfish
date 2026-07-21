//go:build integration

// A reconnect loop is a classic goroutine leak: the retry backoff must honour cancellation, and the
// health signal must read down once the pump stops. These run against a real Postgres because a
// LISTEN subscription only exists on a real connection.
package pglisten

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/health"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	p, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestPumpStopsPromptlyOnCancel is the shutdown guarantee: a cancelled context returns Run at once —
// even mid-backoff — and never blocks the ordered shutdown. It also confirms Run releases without
// leaking by draining the connection back to the pool.
func TestPumpStopsPromptlyOnCancel(t *testing.T) {
	pool := testPool(t)
	reg := health.NewRegistry()
	listener := reg.Listener("pglisten_test")

	subscribed := make(chan struct{}, 1)
	p := &Pump{
		Pool:    pool,
		Channel: "pglisten_test",
		Log:     testLog(),
		Health:  listener,
		OnSubscribe: func(context.Context) error {
			select {
			case subscribed <- struct{}{}:
			default:
			}
			return nil
		},
		OnNotify: func(context.Context, string) {},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	select {
	case <-subscribed:
	case <-time.After(5 * time.Second):
		t.Fatal("pump never subscribed")
	}
	if !listener.Subscribed() {
		t.Fatal("listener not marked subscribed after OnSubscribe")
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pump did not stop promptly on cancel — the shutdown ordering would hang")
	}
	if listener.Subscribed() {
		t.Fatal("listener still reports subscribed after the pump stopped")
	}
}

// TestPumpReconnects drives the recovery path directly: terminate the subscription's backend and
// assert the pump comes back — a fresh OnSubscribe and a live health signal — without intervention.
func TestPumpReconnects(t *testing.T) {
	pool := testPool(t)
	reg := health.NewRegistry()
	listener := reg.Listener("pglisten_reconnect")

	subs := make(chan struct{}, 8)
	p := &Pump{
		Pool:    pool,
		Channel: "pglisten_reconnect",
		Log:     testLog(),
		Health:  listener,
		OnSubscribe: func(context.Context) error {
			subs <- struct{}{}
			return nil
		},
		OnNotify: func(context.Context, string) {},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	// First subscribe.
	select {
	case <-subs:
	case <-time.After(5 * time.Second):
		t.Fatal("pump never made its first subscription")
	}

	// Kill the listener's backend.
	if _, err := pool.Exec(ctx, `
SELECT pg_terminate_backend(pid)
  FROM pg_stat_activity
 WHERE datname = current_database()
   AND pid <> pg_backend_pid()
   AND query LIKE 'LISTEN %'`); err != nil {
		t.Fatalf("terminate backend: %v", err)
	}

	// Second subscribe: the reconnect.
	select {
	case <-subs:
	case <-time.After(15 * time.Second):
		t.Fatal("pump did not resubscribe after its connection was terminated")
	}
	if got := listener.Subscribes(); got < 2 {
		t.Fatalf("subscribe count = %d after a reconnect, want >= 2", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pump did not stop after cancel")
	}
}
