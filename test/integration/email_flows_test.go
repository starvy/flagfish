//go:build integration

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
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/board"
	"github.com/starvy/flagfish/internal/catalog"
	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/gameplay"
	"github.com/starvy/flagfish/internal/httpapi"
	"github.com/starvy/flagfish/internal/jobs"
	"github.com/starvy/flagfish/internal/migrate"
)

// capturedMail is one message the fake mailer was handed. The email flows are the seam's
// only reason to exist, so a test's whole view of "did the mail go out" is this list.
type capturedMail struct{ to, subject, body string }

// fakeMailer stands in for real SMTP at the mail.Mailer seam. It records every message and
// signals a channel so a test can block until the async worker has actually delivered one,
// rather than sleeping and hoping.
type fakeMailer struct {
	mu   sync.Mutex
	sent []capturedMail
	ch   chan capturedMail
}

func newFakeMailer() *fakeMailer { return &fakeMailer{ch: make(chan capturedMail, 16)} }

func (m *fakeMailer) Send(_ context.Context, to, subject, body string) error {
	m.mu.Lock()
	m.sent = append(m.sent, capturedMail{to, subject, body})
	m.mu.Unlock()
	m.ch <- capturedMail{to, subject, body}
	return nil
}

// waitMail blocks for the next delivered message. A timeout fails the test rather than
// hanging it: a verification email that never arrives is the bug under test.
func (m *fakeMailer) waitMail(t *testing.T) capturedMail {
	t.Helper()
	select {
	case c := <-m.ch:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("expected an email, none was delivered")
		return capturedMail{}
	}
}

// expectNoMail asserts that nothing is delivered within a short window — the property that
// makes the reset endpoint safe: an unknown address queues no mail to leak its absence.
func (m *fakeMailer) expectNoMail(t *testing.T) {
	t.Helper()
	select {
	case c := <-m.ch:
		t.Fatalf("expected no email, got one to %s", c.to)
	case <-time.After(500 * time.Millisecond):
	}
}

var tokenRe = regexp.MustCompile(`[0-9a-f]{64}`)

func tokenFrom(t *testing.T, m capturedMail) string {
	t.Helper()
	tok := tokenRe.FindString(m.body)
	if tok == "" {
		t.Fatalf("no token in mail body: %q", m.body)
	}
	return tok
}

// newEmailAPI builds the same server the shared harness does, but wired to a REAL River
// inserter and an in-process worker whose mailer is the fake above — so a request that
// enqueues mail is observable end to end. It cannot reuse newAPI, whose Service has no
// queue behind it and would refuse to enqueue.
func newEmailAPI(t *testing.T, mode account.Mode, cfgKV ...[2]string) (*apiFix, *fakeMailer) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := migrate.RunRiver(ctx, dsn, log); err != nil {
		t.Fatalf("river migrate: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	truncate(t, ctx, pool)
	// A worker running against leftover jobs from an earlier test would deliver mail this
	// test never sent; start each case from an empty queue.
	if _, execErr := pool.Exec(ctx, `TRUNCATE river_job RESTART IDENTITY`); execErr != nil {
		t.Fatalf("truncate river_job: %v", execErr)
	}
	seedInstance(t, ctx, pool, mode)
	// A mailer, because verify_emails without one is a config that refuses to boot —
	// an instance that gates gameplay on an email it can never send. Delivery here is
	// the fake above; these rows only make the config a legal one.
	for _, kv := range [][2]string{
		{"mail_server", "smtp.ctf.test"},
		{"mail_port", "587"},
		{"mailfrom_addr", "noreply@ctf.test"},
	} {
		if _, execErr := pool.Exec(ctx, `INSERT INTO config (key, value) VALUES ($1,$2)`, kv[0], kv[1]); execErr != nil {
			t.Fatalf("seed mail config %s: %v", kv[0], execErr)
		}
	}
	for _, kv := range cfgKV {
		if _, execErr := pool.Exec(ctx, `INSERT INTO config (key, value) VALUES ($1,$2)`, kv[0], kv[1]); execErr != nil {
			t.Fatalf("seed config %s: %v", kv[0], execErr)
		}
	}

	cfg, err := config.New(ctx, config.NewPGStore(pool), log)
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	inserter, err := jobs.NewInsertOnly(pool)
	if err != nil {
		t.Fatalf("inserter: %v", err)
	}
	fake := newFakeMailer()
	worker, err := jobs.NewWorker(pool, jobs.WorkerDeps{
		Mailer: fake,
		Config: cfg,
		Poster: jobs.NewHTTPPoster(),
		Log:    log,
	})
	if err != nil {
		t.Fatalf("worker: %v", err)
	}
	if err := worker.Start(ctx); err != nil {
		t.Fatalf("worker start: %v", err)
	}
	t.Cleanup(func() {
		if err := worker.Stop(context.Background()); err != nil {
			t.Logf("worker stop: %v", err)
		}
	})

	acct := accounts.NewService(pool, mode, log, accounts.WithJobs(inserter))
	srv := httpapi.New(httpapi.Options{
		Config:   cfg,
		Auth:     acct,
		Limiter:  accounts.NewLimiter(pool, 1000, 60_000_000_000),
		Log:      log,
		Accounts: acct,
		Gameplay: gameplay.New(pool, stubInserter{}, mode),
		Catalog:  catalog.New(pool),
		Board:    board.New(pool, mode),
	})
	ts := httptest.NewServer(srv.Router)
	t.Cleanup(ts.Close)

	return &apiFix{
		t: t, pool: pool, q: db.New(pool), acct: acct, server: ts,
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, fake
}

// verified reports the users.verified flag for an address.
func userVerified(t *testing.T, f *apiFix, email string) bool {
	t.Helper()
	var v bool
	if err := f.pool.QueryRow(context.Background(),
		`SELECT verified FROM users WHERE email = $1`, email).Scan(&v); err != nil {
		t.Fatalf("read verified: %v", err)
	}
	return v
}

func TestEmailVerificationConfirmFlow(t *testing.T) {
	f, mail := newEmailAPI(t, account.ModeUsers, [2]string{"verify_emails", "true"}, [2]string{"ctf_name", "Testik"})

	const email = "player@ctf.test"
	f.register("Player", email, "hunter2pass")
	if userVerified(t, f, email) {
		t.Fatal("a fresh registration under verify_emails should start unverified")
	}

	m := mail.waitMail(t)
	if m.to != email {
		t.Fatalf("verification mail went to %q, want %q", m.to, email)
	}
	token := tokenFrom(t, m)

	// Bad token: indistinguishable 400, no state change.
	if res, _ := f.do(http.MethodPost, "/api/v1/verify/confirm", map[string]any{"token": "deadbeef"}); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("confirm bogus token: status %d, want 400", res.StatusCode)
	}

	res, body := f.do(http.MethodPost, "/api/v1/verify/confirm", map[string]any{"token": token})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("confirm: status %d: %s", res.StatusCode, body)
	}
	if !userVerified(t, f, email) {
		t.Fatal("confirm did not mark the user verified")
	}

	// Single-use: the same token spends exactly once.
	if res, _ := f.do(http.MethodPost, "/api/v1/verify/confirm", map[string]any{"token": token}); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("re-confirm: status %d, want 400 (token already spent)", res.StatusCode)
	}
}

func TestEmailVerificationResend(t *testing.T) {
	f, mail := newEmailAPI(t, account.ModeUsers, [2]string{"verify_emails", "true"})

	const email = "resend@ctf.test"
	cookie, csrf := f.register("Resend", email, "hunter2pass")
	first := mail.waitMail(t) // from registration
	_ = first

	res, body := f.do(http.MethodPost, "/api/v1/verify/resend", nil, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("resend: status %d: %s", res.StatusCode, body)
	}
	resent := mail.waitMail(t)
	token := tokenFrom(t, resent)

	if res, _ := f.do(http.MethodPost, "/api/v1/verify/confirm", map[string]any{"token": token}); res.StatusCode != http.StatusOK {
		t.Fatalf("confirm resent token: status %d", res.StatusCode)
	}
	if !userVerified(t, f, email) {
		t.Fatal("resent token did not verify the user")
	}

	// A verified user asking again is a conflict, not a silent success.
	if res, _ := f.do(http.MethodPost, "/api/v1/verify/resend", nil, withCookie(cookie), withCSRF(csrf)); res.StatusCode != http.StatusConflict {
		t.Fatalf("resend when already verified: status %d, want 409", res.StatusCode)
	}
}

func TestPasswordResetNoOracle(t *testing.T) {
	f, mail := newEmailAPI(t, account.ModeUsers)

	const known = "known@ctf.test"
	f.register("Known", known, "hunter2pass")

	resKnown, bodyKnown := f.do(http.MethodPost, "/api/v1/reset-password", map[string]any{"email": known})
	// The known-address branch queues mail; drain it so it cannot bleed into the next assertion.
	got := mail.waitMail(t)
	if got.to != known {
		t.Fatalf("reset mail went to %q, want %q", got.to, known)
	}

	resUnknown, bodyUnknown := f.do(http.MethodPost, "/api/v1/reset-password", map[string]any{"email": "nobody@ctf.test"})

	if resKnown.StatusCode != http.StatusOK || resUnknown.StatusCode != http.StatusOK {
		t.Fatalf("reset request not always-200: known=%d unknown=%d", resKnown.StatusCode, resUnknown.StatusCode)
	}
	if !bytes.Equal(bodyKnown, bodyUnknown) {
		t.Fatalf("response body reveals existence: known=%q unknown=%q", bodyKnown, bodyUnknown)
	}
	// The unknown address must not have queued anything.
	mail.expectNoMail(t)
}

func TestPasswordResetAppliesAndKillsSessions(t *testing.T) {
	f, mail := newEmailAPI(t, account.ModeUsers)

	const email = "reset@ctf.test"
	const oldPass = "oldpassword1"
	const newPass = "newpassword2"
	cookie, _ := f.register("Reset", email, oldPass)

	// A live session, to prove the reset kills it.
	if res, _ := f.do(http.MethodGet, "/api/v1/me", nil, withCookie(cookie)); res.StatusCode != http.StatusOK {
		t.Fatalf("pre-reset /me: status %d, want 200", res.StatusCode)
	}

	if res, _ := f.do(http.MethodPost, "/api/v1/reset-password", map[string]any{"email": email}); res.StatusCode != http.StatusOK {
		t.Fatalf("reset request: status %d", res.StatusCode)
	}
	token := tokenFrom(t, mail.waitMail(t))

	res, body := f.do(http.MethodPatch, "/api/v1/reset-password", map[string]any{"token": token, "password": newPass})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("reset apply: status %d: %s", res.StatusCode, body)
	}

	// The old session was fingerprinted against the old password hash and must be dead.
	if res, _ := f.do(http.MethodGet, "/api/v1/me", nil, withCookie(cookie)); res.StatusCode == http.StatusOK {
		t.Fatal("old session still valid after password reset")
	}
	// The old password no longer authenticates; the new one does.
	if res, _ := f.do(http.MethodPost, "/api/v1/login", map[string]any{"email": email, "password": oldPass}); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("login with old password: status %d, want 401", res.StatusCode)
	}
	if res, _ := f.do(http.MethodPost, "/api/v1/login", map[string]any{"email": email, "password": newPass}); res.StatusCode != http.StatusOK {
		t.Fatalf("login with new password: status %d, want 200", res.StatusCode)
	}

	// A spent reset token is spent.
	if res, _ := f.do(http.MethodPatch, "/api/v1/reset-password", map[string]any{"token": token, "password": "third1234"}); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("re-apply spent reset token: status %d, want 400", res.StatusCode)
	}
}

// A reset is the remediation for a leaked credential, so it has to take the API tokens too.
//
// The whole flow, over the wire, with the real mailer seam: mint a token, run a reset the way a
// locked-out user runs one, and watch the bearer credential stop working. Sessions die on their
// own — they carry a fingerprint of the password hash — but nothing about a token tracks the
// password, so if the reset does not delete it, whoever stole it keeps API access, flag
// submission included, until the TTL runs out. The response says how many died, because the
// user is the only one who can re-mint them.
func TestPasswordResetRevokesAPITokens(t *testing.T) {
	f, mail := newEmailAPI(t, account.ModeUsers)

	const email = "revoke@ctf.test"
	const oldPass = "oldpassword1"
	const newPass = "newpassword2"
	cookie, csrf := f.register("Revoke", email, oldPass)

	res, body := f.do(http.MethodPost, "/api/v1/tokens", map[string]any{
		"description": "ci runner",
	}, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create token: status %d: %s", res.StatusCode, body)
	}
	var created struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode created token: %v (%s)", err, body)
	}
	res, _ = f.do(http.MethodGet, "/api/v1/me", nil, withToken(created.Token))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("token should work before the reset: status %d", res.StatusCode)
	}

	res, _ = f.do(http.MethodPost, "/api/v1/reset-password", map[string]any{"email": email})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("reset request: status %d", res.StatusCode)
	}
	token := tokenFrom(t, mail.waitMail(t))

	res, body = f.do(http.MethodPatch, "/api/v1/reset-password", map[string]any{"token": token, "password": newPass})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("reset apply: status %d: %s", res.StatusCode, body)
	}
	var applied struct {
		APITokensRevoked int64 `json:"api_tokens_revoked"`
	}
	if err := json.Unmarshal(body, &applied); err != nil {
		t.Fatalf("decode reset response: %v (%s)", err, body)
	}
	if applied.APITokensRevoked != 1 {
		t.Errorf("api_tokens_revoked = %d, want 1 — a silent revocation looks like an outage", applied.APITokensRevoked)
	}

	res, _ = f.do(http.MethodGet, "/api/v1/me", nil, withToken(created.Token))
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("the API token survived the password reset: status %d, want 401", res.StatusCode)
	}
}

// TestConsumeEmailTokenIsAtomic races two consumers on one token. The consuming UPDATE is
// conditional on consumed_at IS NULL, so the database lets exactly one win — a SELECT-then-
// UPDATE would let both through, which is the bug this guards.
func TestConsumeEmailTokenIsAtomic(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	truncate(t, ctx, pool)
	seedInstance(t, ctx, pool, account.ModeUsers)
	q := db.New(pool)

	var userID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (name, email, password_hash) VALUES ('Racer','race@ctf.test','x') RETURNING id`).
		Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	hash := make([]byte, 32)
	for i := range hash {
		hash[i] = byte(i)
	}
	if _, err := q.CreateEmailToken(ctx, db.CreateEmailTokenParams{
		TokenHash: hash,
		Purpose:   "reset",
		UserID:    userID,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("create token: %v", err)
	}

	const racers = 8
	var (
		wins  int
		mu    sync.Mutex
		start = make(chan struct{})
		wg    sync.WaitGroup
	)
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := q.ConsumeEmailToken(ctx, db.ConsumeEmailTokenParams{TokenHash: hash, Purpose: "reset"})
			if err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if wins != 1 {
		t.Fatalf("token consumed %d times, want exactly 1", wins)
	}
}
