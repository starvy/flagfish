//go:build integration

package integration

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/board"
	"github.com/starvy/flagfish/internal/catalog"
	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/gameplay"
	"github.com/starvy/flagfish/internal/httpapi"
	"github.com/starvy/flagfish/internal/jobs"
	"github.com/starvy/flagfish/internal/migrate"
	"github.com/starvy/flagfish/internal/notify"
)

// newPoolAlertAPI wires the server with a real River insert-only client and the notify service, so
// the 503 path actually enqueues the deduplicated pool-exhaustion alert.
func newPoolAlertAPI(t *testing.T) *apiFix {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL is not set — run `task test-integration`")
	}
	ctx := context.Background()

	// The insert-only client needs River's tables.
	if err := migrate.RunRiver(ctx, dsn, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("river migrations: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	truncate(t, ctx, pool)
	seedInstance(t, ctx, pool, account.ModeUsers)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, err := config.New(ctx, config.NewPGStore(pool), log)
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	inserter, err := jobs.NewInsertOnly(pool)
	if err != nil {
		t.Fatalf("inserter: %v", err)
	}

	acct := accounts.NewService(pool, account.ModeUsers, log)
	srv := httpapi.New(httpapi.Options{
		Config:   cfg,
		Auth:     acct,
		Limiter:  accounts.NewLimiter(pool, 1000, 60_000_000_000),
		Log:      log,
		Accounts: acct,
		Gameplay: gameplay.New(pool, inserter, account.ModeUsers),
		Catalog:  catalog.New(pool),
		Board:    board.New(pool, account.ModeUsers),
		Notify:   notify.NewService(pool),
		Jobs:     inserter,
	})
	ts := httptest.NewServer(srv.Router)
	t.Cleanup(ts.Close)

	return &apiFix{
		t: t, pool: pool, acct: acct, server: ts,
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

func (f *apiFix) alertJobCount() int64 {
	f.t.Helper()
	var n int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = 'pool_exhausted_alert'`).Scan(&n); err != nil {
		f.t.Fatalf("count alert jobs: %v", err)
	}
	return n
}

// A stampede of views against one empty pool enqueues exactly one alert; a second challenge gets its
// own. This pins "300 late registrants = 1 alert" at the enqueue boundary.
func TestPoolAlert_FiresOncePerChallenge(t *testing.T) {
	f := newPoolAlertAPI(t)

	// Two unique-flag challenges, both with empty pools.
	a := f.seedUniqueChallenge("pwn/heap")
	b := f.seedUniqueChallenge("rev/vm")

	cookie, csrf := f.register("late", "late@ctf.test", "correct-horse-battery")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	// Hit challenge A eight times: every view is a 503, and every 503 enqueues — but the alert is
	// deduplicated by args.
	for range 8 {
		res, body := f.do(http.MethodGet, "/api/v1/challenges/"+itoa(a), nil, auth...)
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("view A: status %d, want 503 (%s)", res.StatusCode, body)
		}
	}
	// Wait for the enqueues to land (Insert is synchronous, but be explicit about the assertion).
	if n := f.alertJobCount(); n != 1 {
		t.Fatalf("alert jobs after 8 views of A = %d, want 1 (dedup by challenge)", n)
	}

	// A different challenge is a different alert.
	for range 3 {
		res, _ := f.do(http.MethodGet, "/api/v1/challenges/"+itoa(b), nil, auth...)
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("view B: status %d, want 503", res.StatusCode)
		}
	}
	if n := f.alertJobCount(); n != 2 {
		t.Fatalf("alert jobs after also viewing B = %d, want 2 (one per challenge)", n)
	}
}
