//go:build integration

// The reaper's regression test. It existed as three unreferenced queries; nothing deleted anything,
// and rate_limits — read on the submit hot path — grew a row per bucket per window forever. These
// assert the sweep deletes what is dead, spares what is live, and stays bounded so a first run on a
// huge table cannot take the hot path down with one giant DELETE.
package jobs

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// seedUser inserts one user and returns its id, for the session and token FKs.
func seedUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(
		ctx,
		`INSERT INTO users (name, email) VALUES ('reaped', 'reaped@example.com') RETURNING id`,
	).Scan(&id); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func runReaper(t *testing.T, ctx context.Context, w *ReapExpiredWorker) {
	t.Helper()
	if err := w.Work(ctx, &river.Job[ReapExpired]{Args: ReapExpired{}}); err != nil {
		t.Fatalf("reaper: %v", err)
	}
}

// TestReaperDeletesExpiredSparesLive is the core property: expired rows go, live rows stay, in all
// three tables. The rate-limit retention is derived from the window, so a row inside the retention
// horizon must survive even though its window has technically passed.
func TestReaperDeletesExpiredSparesLive(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	truncateReap(t, ctx, p)

	userID := seedUser(t, ctx, p)
	now := time.Now()

	// Sessions: one expired, one live.
	mustExec(t, ctx, p, `INSERT INTO sessions (id_hash, user_id, pw_fingerprint, csrf_token, expires_at)
	                     VALUES ('\x01', $1, '\x00', 'a', $2)`, userID, now.Add(-time.Hour))
	mustExec(t, ctx, p, `INSERT INTO sessions (id_hash, user_id, pw_fingerprint, csrf_token, expires_at)
	                     VALUES ('\x02', $1, '\x00', 'b', $2)`, userID, now.Add(time.Hour))

	// API tokens: one expired, one live.
	mustExec(t, ctx, p, `INSERT INTO api_tokens (user_id, token_hash, expires_at) VALUES ($1, '\x11', $2)`,
		userID, now.Add(-time.Hour))
	mustExec(t, ctx, p, `INSERT INTO api_tokens (user_id, token_hash, expires_at) VALUES ($1, '\x12', $2)`,
		userID, now.Add(time.Hour))

	// Rate limits: one ancient (dead), one from the current window (live). With a one-minute window
	// the retention floor keeps anything younger than 15 minutes, so "1 hour ago" is dead and "now"
	// is live.
	window := time.Minute
	mustExec(t, ctx, p, `INSERT INTO rate_limits (bucket, window_start, n) VALUES ('old', $1, 3)`,
		now.Add(-time.Hour).Truncate(window))
	mustExec(t, ctx, p, `INSERT INTO rate_limits (bucket, window_start, n) VALUES ('fresh', $1, 3)`,
		now.Truncate(window))

	w := &ReapExpiredWorker{Pool: p, Log: testLog(), RateWindow: window}
	runReaper(t, ctx, w)

	if got := countRows(t, ctx, p, "sessions"); got != 1 {
		t.Errorf("sessions: %d rows after reap, want 1 (the live one)", got)
	}
	if got := countRows(t, ctx, p, "api_tokens"); got != 1 {
		t.Errorf("api_tokens: %d rows after reap, want 1 (the live one)", got)
	}
	if got := countRows(t, ctx, p, "rate_limits"); got != 1 {
		t.Errorf("rate_limits: %d rows after reap, want 1 (the current window)", got)
	}

	// The survivors are the live ones, by name.
	var bucket string
	if err := p.QueryRow(ctx, `SELECT bucket FROM rate_limits`).Scan(&bucket); err != nil {
		t.Fatalf("read surviving rate_limit: %v", err)
	}
	if bucket != "fresh" {
		t.Errorf("surviving rate_limit bucket = %q, want %q", bucket, "fresh")
	}
}

// TestReaperRecentRateLimitSurvives pins the retention floor directly: a rate-limit row whose window
// has passed but is younger than the floor is still live — a straddling request can still refund
// against it — and must not be deleted.
func TestReaperRecentRateLimitSurvives(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	truncateReap(t, ctx, p)

	window := time.Minute
	// Five minutes old: its one-minute window is long closed, but it is well inside the 15-minute
	// floor, so it must survive.
	mustExec(t, ctx, p, `INSERT INTO rate_limits (bucket, window_start, n) VALUES ('recent', $1, 1)`,
		time.Now().Add(-5*time.Minute).Truncate(window))

	w := &ReapExpiredWorker{Pool: p, Log: testLog(), RateWindow: window}
	runReaper(t, ctx, w)

	if got := countRows(t, ctx, p, "rate_limits"); got != 1 {
		t.Fatalf("rate_limits: %d rows after reap, want 1 — a row inside the retention floor was deleted", got)
	}
}

// TestReaperIsBounded proves one run deletes no more than batch*maxBatches rows, so a first sweep of
// a table that has grown unswept for a whole event cannot lock the hot path with one enormous
// DELETE. It seeds more dead rows than a bounded run can clear and asserts a remainder survives.
func TestReaperIsBounded(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	truncateReap(t, ctx, p)

	window := time.Minute
	old := time.Now().Add(-time.Hour).Truncate(window)
	// 25 dead rows, a budget of 2 batches of 5 = 10 per run.
	for i := range 25 {
		mustExec(t, ctx, p, `INSERT INTO rate_limits (bucket, window_start, n) VALUES ($1, $2, 1)`,
			"b"+strconv.Itoa(i), old.Add(time.Duration(i)*time.Millisecond))
	}

	w := &ReapExpiredWorker{Pool: p, Log: testLog(), RateWindow: window, Batch: 5, MaxBatches: 2}
	runReaper(t, ctx, w)

	if got := countRows(t, ctx, p, "rate_limits"); got != 15 {
		t.Fatalf("rate_limits: %d rows after one bounded run, want 15 (25 seeded − 10 per run)", got)
	}

	// A second run clears another batch of the backlog — the sweep makes progress across runs.
	runReaper(t, ctx, w)
	if got := countRows(t, ctx, p, "rate_limits"); got != 5 {
		t.Fatalf("rate_limits: %d rows after two bounded runs, want 5", got)
	}
}

func mustExec(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func truncateReap(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	mustExec(t, ctx, pool, `TRUNCATE sessions, api_tokens, rate_limits, users RESTART IDENTITY CASCADE`)
}
