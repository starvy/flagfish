//go:build integration

// These are the regression tests for the operational defect where a single dropped Postgres
// connection killed both LISTEN pumps for the life of the process — the config watcher and the SSE
// broadcaster — with nothing anywhere reporting it. They terminate the pump's backend server-side
// and assert recovery, that a change made during the outage is not lost, and that readiness tells
// the truth about a dead pump.
package integration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/health"
	"github.com/starvy/flagfish/internal/metrics"
)

// terminateListeners kills every backend on this database that is parked on a LISTEN — which is
// exactly the pumps' dedicated connections and nothing else, because an idle listener's last
// statement stays "LISTEN ..." in pg_stat_activity. It returns how many it killed. This is the
// server-side equivalent of a Postgres restart or an idle-connection reap.
func terminateListeners(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var killed int
	rows, err := pool.Query(ctx, `
SELECT pg_terminate_backend(pid)
  FROM pg_stat_activity
 WHERE datname = current_database()
   AND pid <> pg_backend_pid()
   AND query LIKE 'LISTEN %'`)
	if err != nil {
		t.Fatalf("terminate listeners: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		killed++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("terminate listeners: %v", err)
	}
	return killed
}

// waitUntil polls cond until it holds or the deadline passes.
func waitUntil(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(within)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out after %s waiting for: %s", within, what)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestConfigWatcherRecoversAndReloadsAfterDrop is the load-bearing one. It proves the watcher does
// not merely resubscribe but RELOADS on resubscribe — the property that keeps the pause switch
// honest. The config change during the outage is written with a raw UPDATE that fires no NOTIFY, so
// the signal genuinely never happened: the only path by which the manager can ever see it is a full
// reload after the connection comes back. Without the fix the watcher dies on the first drop and
// the change is invisible forever, so this test hangs to its deadline and fails.
func TestConfigWatcherRecoversAndReloadsAfterDrop(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	truncate(t, ctx, pool)
	seedInstance(t, ctx, pool, account.ModeUsers)
	if _, execErr := pool.Exec(ctx, `INSERT INTO config (key, value) VALUES ('ctf_name', 'Before')`); execErr != nil {
		t.Fatalf("seed config: %v", execErr)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := health.NewRegistry()
	mgr, err := config.New(ctx, config.NewPGStore(pool), log)
	if err != nil {
		t.Fatalf("config manager: %v", err)
	}
	watcher := config.NewPGWatcher(pool, log, reg)

	watchCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if runErr := mgr.Run(watchCtx, watcher); runErr != nil && watchCtx.Err() == nil {
			t.Errorf("config watcher exited: %v", runErr)
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("config watcher did not stop")
		}
	})

	listener := reg.Listener("config_changed")
	waitUntil(t, 5*time.Second, "watcher to subscribe", listener.Subscribed)
	if got := mgr.Current().CTFName; got != "Before" {
		t.Fatalf("initial ctf_name = %q, want %q", got, "Before")
	}
	firstSubs := listener.Subscribes()

	// Kill the watcher's connection, then change the table WITHOUT a NOTIFY. The signal for this
	// change is delivered to nobody: only a reload on resubscribe can surface it.
	if n := terminateListeners(t, ctx, pool); n == 0 {
		t.Fatal("no listener backend was terminated; the watcher was not subscribed as expected")
	}
	if _, err := pool.Exec(ctx, `UPDATE config SET value = 'After' WHERE key = 'ctf_name'`); err != nil {
		t.Fatalf("out-of-band config change: %v", err)
	}

	waitUntil(t, 20*time.Second, "watcher to resubscribe and reload the changed config", func() bool {
		return mgr.Current().CTFName == "After"
	})
	if resubs := listener.Subscribes(); resubs <= firstSubs {
		t.Fatalf("resubscribe count did not advance (%d -> %d): recovery came from something other than a reconnect",
			firstSubs, resubs)
	}
}

// TestNotificationPumpRecoversAndReplaysMissed proves both halves of the broadcaster fix on a
// client that stays connected across the outage: the pump reconnects, and a notification whose
// NOTIFY was lost while it was gone is replayed on resubscribe rather than stranded.
//
// The "missed" row is inserted straight into the table with no pg_notify — a signal delivered to
// nobody, exactly what a dropped connection causes — and it is inserted while the pump is still
// live and idle, so nothing delivers it before the reconnect. The only path that can surface it is
// the catch-up the pump runs after it resubscribes. Without the fix the pump never reconnects, so
// the row is never delivered and this hangs to its deadline.
func TestNotificationPumpRecoversAndReplaysMissed(t *testing.T) {
	nf := newNotifyAPI(t, account.ModeUsers)
	playerCookie, _ := nf.register("player", "player@example.com", "correct-horse-battery")
	adminCookie, adminCSRF, _ := nf.admin("root", "root@example.com")

	s := nf.openStream(playerCookie)
	defer s.close()

	// A live delivery first, which advances the pump's replay watermark to this id.
	before := nf.publish(adminCookie, adminCSRF, "Before", "delivered before the outage")
	s.waitFor(t, before, 3*time.Second)

	listener := nf.health.Listener("notifications")
	waitUntil(t, 3*time.Second, "broadcaster to be subscribed", listener.Subscribed)
	firstSubs := listener.Subscribes()

	ctx := context.Background()

	// The signal that goes missing: a committed row with no NOTIFY. It exists past the watermark,
	// but nothing wakes the still-connected pump to deliver it.
	var missed int64
	if err := nf.pool.QueryRow(
		ctx,
		`INSERT INTO notifications (title, content) VALUES ('Missed', 'its signal was lost') RETURNING id`,
	).Scan(&missed); err != nil {
		t.Fatalf("insert missed notification: %v", err)
	}

	// Now drop the pump's connection. On resubscribe its catch-up reads everything after the
	// watermark and finds the missed row.
	if n := terminateListeners(t, ctx, nf.pool); n == 0 {
		t.Fatal("no listener backend was terminated; the broadcaster was not subscribed")
	}

	got := s.waitFor(t, missed, 20*time.Second)
	if got.Title != "Missed" {
		t.Fatalf("replayed %+v, want the missed notification", got)
	}
	if resubs := listener.Subscribes(); resubs <= firstSubs {
		t.Fatalf("broadcaster resubscribe count did not advance (%d -> %d)", firstSubs, resubs)
	}

	// And live delivery works again after recovery.
	after := nf.publish(adminCookie, adminCSRF, "After", "delivered live after recovery")
	s.waitFor(t, after, 5*time.Second)
}

// TestReadyzReflectsADeadPump is the deterministic half of the readiness story: with the pool up
// but a registered listener not subscribed, /readyz must be 503. A probe that stays green while the
// thing it guards is dead is worse than no probe, so this pins the behaviour directly.
func TestReadyzReflectsADeadPump(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := health.NewRegistry()
	listener := reg.Listener("notifications")
	m := metrics.New(ctx, pool, log, reg)

	// Subscribed: pool is up and the one listener is live, so readiness is satisfied.
	listener.Up()
	if err := m.Ready(ctx); err != nil {
		t.Fatalf("Ready with a live pool and a subscribed listener: %v", err)
	}

	// The pump dies. The pool is still perfectly healthy — a ping alone would still say ready.
	listener.Down()
	if err := m.Ready(ctx); !errors.Is(err, metrics.ErrListenersDown) {
		t.Fatalf("Ready with a dead pump: got %v, want ErrListenersDown", err)
	}
	if perr := pool.Ping(ctx); perr != nil {
		t.Fatalf("pool ping should still succeed — the failure must be the pump, not the pool: %v", perr)
	}
}

// TestReadyzOverHTTPReflectsADeadPump drives the real /readyz handler: it returns 200 while the
// broadcaster is subscribed and flips to 503 the moment the pump's connection is terminated, before
// it has reconnected.
func TestReadyzOverHTTPReflectsADeadPump(t *testing.T) {
	nf := newNotifyAPI(t, account.ModeUsers)

	listener := nf.health.Listener("notifications")
	waitUntil(t, 3*time.Second, "broadcaster to be subscribed", listener.Subscribed)

	res, _ := nf.do(http.MethodGet, "/readyz", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("/readyz with a live pump: status %d, want 200", res.StatusCode)
	}

	// Terminate and check readiness before the pump can climb back — poll for the 503 rather than
	// racing a single request against the reconnect backoff.
	terminateListeners(t, context.Background(), nf.pool)
	waitUntil(t, 2*time.Second, "/readyz to report the dead pump", func() bool {
		r, _ := nf.do(http.MethodGet, "/readyz", nil)
		return r.StatusCode == http.StatusServiceUnavailable
	})

	// And it recovers on its own.
	waitUntil(t, 20*time.Second, "/readyz to recover after the pump reconnects", func() bool {
		r, _ := nf.do(http.MethodGet, "/readyz", nil)
		return r.StatusCode == http.StatusOK
	})
}
