//go:build integration

// Package security pins the authorization properties the product is sold on.
//
// Every test here is written so that removing the guard makes it fail. That is the only
// property that distinguishes a security test from a security-flavoured comment, and it
// is verified by mutation testing.
//
// Real Postgres, real HTTP, real cookies. The middleware chain under test is the one the
// server actually runs — assembled by httpapi.New, not a hand-rolled subset, because a
// chain assembled differently in the test is a chain that is not the one shipping.
package security

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/db"

	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/domain/policy"
	"github.com/starvy/flagfish/internal/httpapi"
)

type fixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	acct   *accounts.Service
	server *httptest.Server
	client *http.Client
}

func setup(t *testing.T, opts ...func(*fixOpts)) *fixture {
	t.Helper()

	var o fixOpts
	for _, m := range opts {
		m(&o)
	}

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL is not set — run `task test-security`")
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	truncate(t, ctx, pool)
	seedInstance(t, ctx, pool)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfg, err := config.New(ctx, config.NewPGStore(pool), log)
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	acct := accounts.NewService(pool, account.ModeUsers, log)

	limit := 1000 // generous by default; the rate-limit test builds its own fixture
	if o.limit > 0 {
		limit = o.limit
	}

	srv := httpapi.New(httpapi.Options{
		Config:  cfg,
		Auth:    acct,
		Limiter: accounts.NewLimiter(pool, limit, time.Minute),
		Log:     log,
	})

	// Probe routes, registered through the real Huma path with a real RouteClass — so
	// they run the whole chain the server runs (authenticate → ban wall → CSRF → rate
	// limit → policy gate). A hand-rolled handler wired to a subset of the chain would be
	// testing a chain that does not ship.
	registerProbes(srv)

	ts := httptest.NewServer(srv.Router)
	t.Cleanup(ts.Close)

	return &fixture{
		t: t, pool: pool, q: db.New(pool), acct: acct, server: ts,
		// Redirects are not followed: a 302 to /login is a denial, and following it would
		// turn a denial into a 200 and pass a test that should fail.
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// fixOpts tunes the fixture. Only the rate-limit test needs a real limit.
type fixOpts struct{ limit int }

func withLimit(n int) func(*fixOpts) { return func(o *fixOpts) { o.limit = n } }

// registerProbes adds two operations: a safe read and an unsafe write, both requiring
// auth, so the chain's guards have something to guard.
func registerProbes(srv *httpapi.Server) {
	type out struct {
		Body struct {
			OK bool `json:"ok"`
		}
	}
	ok := func(context.Context, *struct{}) (*out, error) {
		var o out
		o.Body.OK = true
		return &o, nil
	}

	httpapi.Register(srv.Public, policy.ClassAccountSelf, huma.Operation{
		OperationID: "probe-read", Method: http.MethodGet, Path: "/probe",
	}, ok)

	httpapi.Register(srv.Public, policy.ClassAccountSelf, huma.Operation{
		OperationID: "probe-write", Method: http.MethodPost, Path: "/probe",
	}, ok)
}

func truncate(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	const stmt = `
TRUNCATE TABLE
    sessions, rate_limits, api_tokens,
    hint_unlocks, awards, solves, submissions,
    flag_issues, challenge_instances,
    hints, flags, tags, files, challenges,
    tracking, field_entries, fields,
    users, teams, brackets, config, instance,
    notifications, tasks, audit_log
RESTART IDENTITY CASCADE`
	if _, err := pool.Exec(ctx, stmt); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func seedInstance(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO instance (user_mode, version) VALUES ('users','test')`); err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	// Without this the policy layer redirects every route to /setup, and every test below
	// would pass for the wrong reason.
	if _, err := pool.Exec(ctx,
		`INSERT INTO config (key, value) VALUES ('setup','true')`); err != nil {
		t.Fatalf("seed config: %v", err)
	}
}

// user seeds a user with a real Argon2id password and returns its id.
func (f *fixture) user(name, password string, mut ...func(*userOpts)) int64 {
	f.t.Helper()

	o := userOpts{}
	for _, m := range mut {
		m(&o)
	}

	hash, err := accounts.Hash(password)
	if err != nil {
		f.t.Fatalf("hash: %v", err)
	}
	stored := hash
	if o.rawHash != "" {
		stored = o.rawHash // e.g. an imported bcrypt hash
	}

	role := "user"
	if o.admin {
		role = "admin"
	}

	var id int64
	err = f.pool.QueryRow(context.Background(), `
        INSERT INTO users (name, email, password_hash, role, verified, banned)
        VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		name, name+"@ctf.test", stored, role, !o.unverified, o.banned).Scan(&id)
	if err != nil {
		f.t.Fatalf("seed user: %v", err)
	}
	return id
}

type userOpts struct {
	admin      bool
	banned     bool
	unverified bool
	rawHash    string
}

func asAdmin(o *userOpts) { o.admin = true }
func withHash(h string) func(*userOpts) {
	return func(o *userOpts) { o.rawHash = h }
}

// ban flips the ban after a credential has been minted, which is the interesting
// direction: the credential is already in the attacker's hand.
func (f *fixture) ban(userID int64) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE users SET banned = true WHERE id = $1`, userID); err != nil {
		f.t.Fatalf("ban: %v", err)
	}
}

func (f *fixture) count(query string, args ...any) int64 {
	f.t.Helper()
	var n int64
	if err := f.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		f.t.Fatalf("count: %v", err)
	}
	return n
}

// resp is what a probe returns: the status, and the body drained and closed so the
// connection is reusable and the linter is satisfied.
type resp struct{ StatusCode int }

// do issues a request against the real server with whatever credential is attached.
func (f *fixture) do(method, path string, mut ...func(*http.Request)) resp {
	f.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, f.server.URL+path, http.NoBody)
	if err != nil {
		f.t.Fatalf("request: %v", err)
	}
	for _, m := range mut {
		m(req)
	}
	r, err := f.client.Do(req)
	if err != nil {
		f.t.Fatalf("do: %v", err)
	}
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		f.t.Fatalf("drain body: %v", err)
	}
	_ = r.Body.Close()
	return resp{StatusCode: r.StatusCode}
}

func withToken(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

func withCookie(sid string) func(*http.Request) {
	return func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: accounts.SessionCookie, Value: sid})
	}
}

func withCSRF(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("CSRF-Token", tok) }
}
