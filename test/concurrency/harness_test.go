//go:build integration

// Package concurrency pins the invariants that only break under contention: a solve is
// awarded once, first blood is announced once, a hint is charged once, a pooled flag is
// issued to exactly one account, registration and attempt caps are exact, and the submit
// hot path takes the challenge lock only on the correct-flag path. Each case drives N
// goroutines at the same row and asserts the outcome the schema is meant to guarantee —
// demonstrated, not claimed.
//
// Real Postgres, never a mock: every invariant here is enforced by a UNIQUE index, an ON
// CONFLICT, or a row lock, and a mock cannot fail the way a database can.
//
//	task test-concurrency     # -race -count=1 -p 1, against :5433
package concurrency

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/gameplay"
	"github.com/starvy/flagfish/internal/migrate"
)

// N is the concurrency for every case that takes one. High enough that a race which only
// reproduces occasionally reproduces every run.
const N = 100

// fixture is one test's world: a pool, a service, and the ids it seeded.
type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *db.Queries
	svc  *gameplay.Service
	mode account.Mode
}

// setup builds a fixture against a real, freshly truncated database.
//
// Truncate rather than transaction-rollback: these tests are about concurrent
// transactions, so they cannot themselves live inside one — an outer transaction would
// serialize the very thing under test.
func setup(t *testing.T, mode account.Mode) *fixture {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		// Skipping would be a false green on the most load-bearing suite in the repo, and
		// this package only builds under -tags=integration: the caller already said they
		// intend to hit Postgres.
		t.Fatal("TEST_DATABASE_URL is not set — run `task test-concurrency`")
	}

	ctx := context.Background()

	// The submit path enqueues the first-blood announcement inside the solve transaction,
	// so River's tables must exist or the first-blood case fails for reasons unrelated to
	// concurrency.
	if err := migrate.RunRiver(ctx, dsn, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("river migrations: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	// A pool smaller than N would serialize the workload, and the tests would pass by
	// never actually racing.
	cfg.MaxConns = N + 10

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	truncate(t, ctx, pool)
	seedInstance(t, ctx, pool, mode)

	// A real River client on the real river_job table: the first-blood case counts enqueued
	// announcements, and a stub would only be asserting on our own bookkeeping.
	rc, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatalf("river client: %v", err)
	}

	return &fixture{
		t:    t,
		pool: pool,
		q:    db.New(pool),
		svc:  gameplay.New(pool, rc, mode),
		mode: mode,
	}
}

// truncate resets the world. RESTART IDENTITY so ids are stable across tests and a
// failure message means the same thing twice.
func truncate(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	const stmt = `
TRUNCATE TABLE
    hint_unlocks, awards, solves, submissions,
    flag_issues, challenge_instances,
    hints, flags, tags, files, challenges,
    tracking, api_tokens, field_entries, fields,
    users, teams, brackets, config, instance,
    notifications, tasks, audit_log,
    river_job
RESTART IDENTITY CASCADE`
	if _, err := pool.Exec(ctx, stmt); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func seedInstance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, mode account.Mode) {
	t.Helper()
	_, err := pool.Exec(ctx,
		`INSERT INTO instance (user_mode, version) VALUES ($1, 'test')`, mode.String())
	if err != nil {
		t.Fatalf("seed instance: %v", err)
	}
}

// ── seeding ────────────────────────────────────────────────────────────────────────

// seedUser creates a user (and in teams mode, its team). It returns the Actor the service
// takes and the account id the database keys on.
func (f *fixture) seedUser(name string) (actor gameplay.Actor, accountID int64) {
	f.t.Helper()
	ctx := context.Background()

	var teamID *int64
	if f.mode == account.ModeTeams {
		var id int64
		err := f.pool.QueryRow(ctx,
			`INSERT INTO teams (name, email) VALUES ($1, $2) RETURNING id`,
			name+"-team", name+"-team@ctf.test").Scan(&id)
		if err != nil {
			f.t.Fatalf("seed team: %v", err)
		}
		teamID = &id
	}

	var userID int64
	err := f.pool.QueryRow(ctx,
		`INSERT INTO users (name, email, team_id) VALUES ($1, $2, $3) RETURNING id`,
		name, name+"@ctf.test", teamID).Scan(&userID)
	if err != nil {
		f.t.Fatalf("seed user: %v", err)
	}

	actor = gameplay.Actor{UserID: userID, TeamID: teamID}
	accountID = userID
	if f.mode == account.ModeTeams {
		accountID = *teamID
	}
	return actor, accountID
}

// setVisibility flips hidden/banned on the account the mode keys on — the two flags that
// decide first-blood eligibility and decay's solve count.
func (f *fixture) setVisibility(actor gameplay.Actor, hidden, banned bool) {
	f.t.Helper()
	table, id := "users", actor.UserID
	if f.mode == account.ModeTeams {
		table, id = "teams", *actor.TeamID
	}
	_, err := f.pool.Exec(context.Background(),
		fmt.Sprintf(`UPDATE %s SET hidden = $1, banned = $2 WHERE id = $3`, table),
		hidden, banned, id)
	if err != nil {
		f.t.Fatalf("set visibility: %v", err)
	}
}

// challengeSpec is the knobs a concurrency case actually turns.
type challengeSpec struct {
	Name       string
	Value      int32
	Function   string // static | linear | logarithmic
	Initial    *int32
	Minimum    *int32
	Decay      *int32
	FirstBlood string // none | announce | bonus
	Bonus      *int32
	FlagMode   string // static | unique
	Flag       string // the correct flag, for FlagMode=static
}

func i32(v int32) *int32 { return &v }

// seedChallenge inserts a challenge and (for static mode) its flag.
func (f *fixture) seedChallenge(spec challengeSpec) int64 {
	f.t.Helper()
	ctx := context.Background()

	if spec.Function == "" {
		spec.Function = "static"
	}
	if spec.FirstBlood == "" {
		spec.FirstBlood = "none"
	}
	if spec.FlagMode == "" {
		spec.FlagMode = "static"
	}
	if spec.Name == "" {
		spec.Name = "chal"
	}

	var id int64
	err := f.pool.QueryRow(ctx, `
        INSERT INTO challenges
            (name, category, value, function, initial, minimum, decay,
             first_blood, first_blood_bonus, flag_mode)
        VALUES ($1,'pwn',$2,$3,$4,$5,$6,$7,$8,$9)
        RETURNING id`,
		spec.Name, spec.Value, spec.Function, spec.Initial, spec.Minimum, spec.Decay,
		spec.FirstBlood, spec.Bonus, spec.FlagMode).Scan(&id)
	if err != nil {
		f.t.Fatalf("seed challenge: %v", err)
	}

	if spec.FlagMode == "static" && spec.Flag != "" {
		_, err = f.pool.Exec(ctx,
			`INSERT INTO flags (challenge_id, type, content) VALUES ($1,'static',$2)`,
			id, spec.Flag)
		if err != nil {
			f.t.Fatalf("seed flag: %v", err)
		}
	}
	return id
}

// seedInstances fills a unique-flag challenge's pool. The plaintext flags it returns
// exist only in the test's memory: the database stores sha256 and nothing else.
func (f *fixture) seedInstances(challengeID int64, n int) []string {
	f.t.Helper()
	out := make([]string, 0, n)
	for i := range n {
		flag := fmt.Sprintf("flagfish{pool_%d_%d}", challengeID, i)
		sum := sha256.Sum256([]byte(flag))
		_, err := f.pool.Exec(context.Background(),
			`INSERT INTO challenge_instances (challenge_id, value_hash) VALUES ($1,$2)`,
			challengeID, sum[:])
		if err != nil {
			f.t.Fatalf("seed instance: %v", err)
		}
		out = append(out, flag)
	}
	return out
}

func (f *fixture) seedHint(challengeID int64, cost int32) int64 {
	f.t.Helper()
	var id int64
	err := f.pool.QueryRow(context.Background(),
		`INSERT INTO hints (challenge_id, title, content, cost) VALUES ($1,'h','the hint',$2) RETURNING id`,
		challengeID, cost).Scan(&id)
	if err != nil {
		f.t.Fatalf("seed hint: %v", err)
	}
	return id
}

// grantPoints gives an account a starting balance as a standard award. Awards and solves
// are one ledger, so this is exactly as real as points earned by solving.
func (f *fixture) grantPoints(actor gameplay.Actor, value int32) {
	f.t.Helper()
	_, err := f.pool.Exec(context.Background(),
		`INSERT INTO awards (user_id, team_id, type, name, value) VALUES ($1,$2,'standard','seed',$3)`,
		actor.UserID, actor.TeamID, value)
	if err != nil {
		f.t.Fatalf("grant points: %v", err)
	}
}

// ── the barrier ────────────────────────────────────────────────────────────────────

// race runs fn n times concurrently, released from a single starting gun, and returns
// each call's error indexed by goroutine.
//
// The barrier is the whole method: goroutines that merely start "around the same time"
// usually finish before the last one is born, and the suite then passes for the wrong
// reason forever. Parking every goroutine on a common channel makes the contended
// statement the first thing each executes.
func race(n int, fn func(i int) error) []error {
	var (
		wg    sync.WaitGroup
		ready sync.WaitGroup
		gun   = make(chan struct{})
		errs  = make([]error, n)
	)

	wg.Add(n)
	ready.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			ready.Done()
			<-gun // every goroutine leaves the line at the same instant
			errs[i] = fn(i)
		}()
	}

	ready.Wait()
	close(gun)
	wg.Wait()
	return errs
}

// ── assertions ─────────────────────────────────────────────────────────────────────

func (f *fixture) count(query string, args ...any) int64 {
	f.t.Helper()
	var n int64
	if err := f.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		f.t.Fatalf("count (%s): %v", query, err)
	}
	return n
}

// score is the account's balance, computed the way the product computes it: one ledger,
// SUM(solves.value) + SUM(awards.value).
func (f *fixture) score(actor gameplay.Actor) int64 {
	f.t.Helper()
	col, id := "user_id", actor.UserID
	if f.mode == account.ModeTeams {
		col, id = "team_id", *actor.TeamID
	}
	q := fmt.Sprintf(`
        SELECT COALESCE((SELECT sum(value) FROM solves WHERE %[1]s = $1), 0)
             + COALESCE((SELECT sum(value) FROM awards WHERE %[1]s = $1), 0)`, col)
	return f.count(q, id)
}

func (f *fixture) challengeValue(id int64) int32 {
	f.t.Helper()
	var v int32
	if err := f.pool.QueryRow(context.Background(),
		`SELECT value FROM challenges WHERE id = $1`, id).Scan(&v); err != nil {
		f.t.Fatalf("challenge value: %v", err)
	}
	return v
}

// testCtx bounds each test, so a lock-ordering regression that deadlocks surfaces as a
// failure rather than a hung CI job.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}
