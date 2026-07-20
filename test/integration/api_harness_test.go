//go:build integration

// Package integration exercises the real HTTP server — the chain assembled by httpapi.New, over a real
// Postgres — the same way the security suite does, but wired with every feature service so the actual
// endpoints are reachable.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/board"
	"github.com/starvy/flagfish/internal/catalog"
	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/gameplay"
	"github.com/starvy/flagfish/internal/httpapi"
	"github.com/starvy/flagfish/internal/metrics"
)

// stubInserter stands in for the River insert-only client: the announcement enqueue is not what these
// tests assert on, and a stub keeps them independent of the job schema.
type stubInserter struct{}

func (stubInserter) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return nil, nil
}

type apiFix struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	acct   *accounts.Service
	server *httptest.Server
	client *http.Client
}

func newAPI(t *testing.T, mode account.Mode, cfgKV ...[2]string) *apiFix {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL is not set — run `task test-integration`")
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	truncate(t, ctx, pool)
	seedInstance(t, ctx, pool, mode)
	// Config is loaded once, below, so extra keys must be seeded before that read.
	for _, kv := range cfgKV {
		if _, execErr := pool.Exec(ctx, `INSERT INTO config (key, value) VALUES ($1,$2)`, kv[0], kv[1]); execErr != nil {
			t.Fatalf("seed config %s: %v", kv[0], execErr)
		}
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, err := config.New(ctx, config.NewPGStore(pool), log)
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	acct := accounts.NewService(pool, mode, log)
	srv := httpapi.New(httpapi.Options{
		Config:   cfg,
		Auth:     acct,
		Limiter:  accounts.NewLimiter(pool, 1000, 60_000_000_000),
		Log:      log,
		Accounts: acct,
		Gameplay: gameplay.New(pool, stubInserter{}, mode),
		Catalog:  catalog.New(pool),
		Board:    board.New(pool, mode),
		Metrics:  metrics.New(ctx, pool, log),
	})

	ts := httptest.NewServer(srv.Router)
	t.Cleanup(ts.Close)

	return &apiFix{
		t: t, pool: pool, q: db.New(pool), acct: acct, server: ts,
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// truncate wipes every application table. The list is derived from the catalog rather than
// hand-maintained so a new migration cannot leave a table un-truncated and leak rows between
// tests; goose's bookkeeping and River's own tables are left alone.
// (The concurrency and security suites still carry their own hand-written copies.)
func truncate(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	rows, err := pool.Query(ctx, `
SELECT quote_ident(tablename)
FROM pg_tables
WHERE schemaname = 'public'
  AND tablename <> 'goose_db_version'
  AND tablename NOT LIKE 'river%'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("collect tables: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("no tables to truncate — is the database migrated?")
	}
	stmt := "TRUNCATE TABLE " + strings.Join(tables, ", ") + " RESTART IDENTITY CASCADE"
	if _, err := pool.Exec(ctx, stmt); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func seedInstance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, mode account.Mode) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO instance (user_mode, version) VALUES ($1,'test')`, mode.String()); err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO config (key, value) VALUES ('setup','true')`); err != nil {
		t.Fatalf("seed config: %v", err)
	}
}

// seedChallenge inserts a visible static challenge and returns its id.
func (f *apiFix) seedChallenge(name, category string, value int) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO challenges (name, category, value) VALUES ($1,$2,$3) RETURNING id`,
		name, category, value).Scan(&id); err != nil {
		f.t.Fatalf("seed challenge: %v", err)
	}
	return id
}

// seedFlag attaches a static, case-sensitive flag to a challenge.
func (f *apiFix) seedFlag(challengeID int64, content string) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO flags (challenge_id, type, content) VALUES ($1,'static',$2)`,
		challengeID, content); err != nil {
		f.t.Fatalf("seed flag: %v", err)
	}
}

// seedHint attaches a hint and returns its id.
func (f *apiFix) seedHint(challengeID int64, content string, cost int) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO hints (challenge_id, content, cost) VALUES ($1,$2,$3) RETURNING id`,
		challengeID, content, cost).Scan(&id); err != nil {
		f.t.Fatalf("seed hint: %v", err)
	}
	return id
}

// register hits the real endpoint and returns the session cookie value and CSRF token.
func (f *apiFix) register(name, email, password string) (cookie, csrf string) {
	f.t.Helper()
	res, body := f.do(http.MethodPost, "/api/v1/register", map[string]any{
		"name": name, "email": email, "password": password,
	})
	if res.StatusCode != http.StatusOK {
		f.t.Fatalf("register %s: status %d: %s", email, res.StatusCode, body)
	}
	cookie = sessionCookie(res)
	if cookie == "" {
		f.t.Fatalf("register %s: no session cookie set", email)
	}
	return cookie, decodeCSRF(f.t, body)
}

// login hits the real endpoint and returns the session cookie value and CSRF token.
func (f *apiFix) login(email, password string) (cookie, csrf string) {
	f.t.Helper()
	res, body := f.do(http.MethodPost, "/api/v1/login", map[string]any{
		"email": email, "password": password,
	})
	if res.StatusCode != http.StatusOK {
		f.t.Fatalf("login %s: status %d: %s", email, res.StatusCode, body)
	}
	cookie = sessionCookie(res)
	if cookie == "" {
		f.t.Fatalf("login %s: no session cookie set", email)
	}
	return cookie, decodeCSRF(f.t, body)
}

// apiResp is the drained result of a request: the body is already read and closed, so tests hold
// bytes and a status, never a live response they must remember to close.
type apiResp struct {
	StatusCode int
	Cookies    []*http.Cookie
}

// do issues a JSON request. Attach credentials with the withCookie/withCSRF/withToken mutators.
func (f *apiFix) do(method, path string, jsonBody any, mut ...func(*http.Request)) (resp apiResp, respBody []byte) {
	f.t.Helper()
	var rdr io.Reader = http.NoBody
	if jsonBody != nil {
		b, err := json.Marshal(jsonBody)
		if err != nil {
			f.t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, f.server.URL+path, rdr)
	if err != nil {
		f.t.Fatalf("request: %v", err)
	}
	if jsonBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, m := range mut {
		m(req)
	}
	res, err := f.client.Do(req)
	if err != nil {
		f.t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		f.t.Fatalf("read body: %v", err)
	}
	return apiResp{StatusCode: res.StatusCode, Cookies: res.Cookies()}, body
}

func withCookie(sid string) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: accounts.SessionCookie, Value: sid}) }
}

func withCSRF(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("CSRF-Token", tok) }
}

func withToken(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

func sessionCookie(res apiResp) string {
	for _, c := range res.Cookies {
		if c.Name == accounts.SessionCookie {
			return c.Value
		}
	}
	return ""
}

func decodeCSRF(t *testing.T, body []byte) string {
	t.Helper()
	var v struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode csrf: %v (%s)", err, body)
	}
	return v.CSRFToken
}
