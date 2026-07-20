//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/board"
	"github.com/starvy/flagfish/internal/catalog"
	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/gameplay"
	"github.com/starvy/flagfish/internal/httpapi"
)

// newAdminAPI mirrors newAPI but wires the adminops service, so the /api/v1/admin routes are actually
// registered. The base harness leaves AdminOps unset on purpose; the admin slice brings its own.
func newAdminAPI(t *testing.T, mode account.Mode) *apiFix {
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
		AdminOps: adminops.New(pool),
	})

	ts := httptest.NewServer(srv.Router)
	t.Cleanup(ts.Close)

	f := &apiFix{
		t: t, pool: pool, q: nil, acct: acct, server: ts,
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	return f
}

// promoteAdmin flips a seeded user to the admin role directly in SQL. The role is read fresh on every
// request, so an already-issued cookie starts authenticating as an admin on its next call.
func (f *apiFix) promoteAdmin(email string) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE users SET role = 'admin' WHERE email = $1`, email); err != nil {
		f.t.Fatalf("promote %s: %v", email, err)
	}
}

func (f *apiFix) userID(email string) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT id FROM users WHERE email = $1`, email).Scan(&id); err != nil {
		f.t.Fatalf("user id for %s: %v", email, err)
	}
	return id
}

// auditCount counts audit rows attributed to an actor for a table+action. It is the direct evidence
// that the mutation and its audit row committed together.
func (f *apiFix) auditCount(table, action string, actorID int64) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE target_table = $1 AND action = $2 AND actor_id = $3`,
		table, action, actorID).Scan(&n); err != nil {
		f.t.Fatalf("audit count: %v", err)
	}
	return n
}

func (f *apiFix) sessionCount(userID int64) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM sessions WHERE user_id = $1`, userID).Scan(&n); err != nil {
		f.t.Fatalf("session count: %v", err)
	}
	return n
}

// admin registers an admin, promotes it, and returns its cookie, csrf token, and user id.
func (f *apiFix) admin(name, email string) (cookie, csrf string, id int64) {
	f.t.Helper()
	cookie, csrf = f.register(name, email, "correct-horse-battery")
	f.promoteAdmin(email)
	return cookie, csrf, f.userID(email)
}

func decodeID(t *testing.T, body []byte) int64 {
	t.Helper()
	var v struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode id: %v (%s)", err, body)
	}
	return v.ID
}

func TestAdminRoutesRejectNonAdmin(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf := f.register("mallory", "mallory@example.com", "correct-horse-battery")

	// Every admin route must refuse a plain user. A GET and a body-bearing mutation each, so both the
	// read and write halves of the surface are covered.
	cases := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/v1/admin/challenges", map[string]any{"name": "x", "category": "y", "value": 100}},
		{http.MethodGet, "/api/v1/admin/users", nil},
		{http.MethodPut, "/api/v1/admin/users/1/ban", map[string]any{"banned": true}},
		{http.MethodGet, "/api/v1/admin/config", nil},
		{http.MethodPatch, "/api/v1/admin/config", map[string]any{"name": "hijack"}},
		{http.MethodPost, "/api/v1/admin/awards", map[string]any{"account_id": 1, "value": 100, "reason": "x"}},
		{http.MethodGet, "/api/v1/admin/awards?account_id=1", nil},
		{http.MethodDelete, "/api/v1/admin/awards/1", nil},
	}
	for _, c := range cases {
		// authenticated non-admin
		res, body := f.do(c.method, c.path, c.body, withCookie(cookie), withCSRF(csrf))
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s as user: got %d, want 403 (%s)", c.method, c.path, res.StatusCode, body)
		}
		// fully anonymous
		resA, _ := f.do(c.method, c.path, c.body)
		if resA.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s anonymous: got %d, want 403", c.method, c.path, resA.StatusCode)
		}
	}
}

func TestAdminChallengeCRUD(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	// create
	res, body := f.do(http.MethodPost, "/api/v1/admin/challenges",
		map[string]any{"name": "pwn", "category": "binary", "value": 500}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: got %d, want 201 (%s)", res.StatusCode, body)
	}
	chID := decodeID(t, body)

	// add flag
	res, body = f.do(http.MethodPost, "/api/v1/admin/challenges/"+itoa(chID)+"/flags",
		map[string]any{"type": "static", "content": "flag{win}"}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("add flag: got %d, want 201 (%s)", res.StatusCode, body)
	}
	flagID := decodeID(t, body)

	// add hint
	res, body = f.do(http.MethodPost, "/api/v1/admin/challenges/"+itoa(chID)+"/hints",
		map[string]any{"content": "look harder", "cost": 50}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("add hint: got %d, want 201 (%s)", res.StatusCode, body)
	}
	hintID := decodeID(t, body)

	// partial update
	res, body = f.do(http.MethodPatch, "/api/v1/admin/challenges/"+itoa(chID),
		map[string]any{"value": 300}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("update: got %d, want 200 (%s)", res.StatusCode, body)
	}

	// set state hidden
	res, _ = f.do(http.MethodPut, "/api/v1/admin/challenges/"+itoa(chID)+"/state",
		map[string]any{"state": "hidden"}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("set state: got %d, want 200", res.StatusCode)
	}

	// delete flag and hint, then the challenge (which has no solves)
	res, _ = f.do(http.MethodDelete, "/api/v1/admin/challenges/"+itoa(chID)+"/flags/"+itoa(flagID), nil, auth...)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete flag: got %d, want 204", res.StatusCode)
	}
	res, _ = f.do(http.MethodDelete, "/api/v1/admin/challenges/"+itoa(chID)+"/hints/"+itoa(hintID), nil, auth...)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete hint: got %d, want 204", res.StatusCode)
	}
	res, body = f.do(http.MethodDelete, "/api/v1/admin/challenges/"+itoa(chID), nil, auth...)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete challenge: got %d, want 204 (%s)", res.StatusCode, body)
	}

	// Every mutation above stamped an audit row against the admin.
	if got := f.auditCount("challenges", "INSERT", adminID); got == 0 {
		t.Error("no INSERT audit row for the created challenge")
	}
	if got := f.auditCount("challenges", "DELETE", adminID); got == 0 {
		t.Error("no DELETE audit row for the deleted challenge")
	}
	if got := f.auditCount("flags", "INSERT", adminID); got == 0 {
		t.Error("no INSERT audit row for the flag")
	}
	if got := f.auditCount("hints", "INSERT", adminID); got == 0 {
		t.Error("no INSERT audit row for the hint")
	}
}

func TestAdminDeleteChallengeWithSolvesConflicts(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	res, body := f.do(http.MethodPost, "/api/v1/admin/challenges",
		map[string]any{"name": "solved", "category": "misc", "value": 100}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: got %d (%s)", res.StatusCode, body)
	}
	chID := decodeID(t, body)

	// A recorded solve is append-only scoreboard history; the FK RESTRICT must refuse the delete.
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO solves (challenge_id, user_id, value) VALUES ($1, $2, 100)`, chID, adminID); err != nil {
		t.Fatalf("seed solve: %v", err)
	}

	res, body = f.do(http.MethodDelete, "/api/v1/admin/challenges/"+itoa(chID), nil, auth...)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("delete with solves: got %d, want 409 (%s)", res.StatusCode, body)
	}
}

// A wrong attempt is anticheat evidence, not debris: a challenge that was never solved but was
// attempted can no longer be deleted. This pins the reversal of the old carve-out that cascaded
// failed attempts away with the challenge.
func TestAdminDeleteChallengeWithAttemptsConflicts(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	res, body := f.do(http.MethodPost, "/api/v1/admin/challenges",
		map[string]any{"name": "attempted", "category": "misc", "value": 100}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: got %d (%s)", res.StatusCode, body)
	}
	chID := decodeID(t, body)

	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO submissions (challenge_id, user_id, type, provided) VALUES ($1, $2, 'incorrect', 'flag{nope}')`,
		chID, adminID); err != nil {
		t.Fatalf("seed submission: %v", err)
	}

	res, body = f.do(http.MethodDelete, "/api/v1/admin/challenges/"+itoa(chID), nil, auth...)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("delete with attempts: got %d, want 409 (%s)", res.StatusCode, body)
	}
}

// An unlocked hint was paid for: neither the hint nor its challenge can be deleted, and both the
// unlock row and the charge award survive the attempts.
func TestAdminDeleteHintUnlockedConflicts(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	res, body := f.do(http.MethodPost, "/api/v1/admin/challenges",
		map[string]any{"name": "hinted", "category": "misc", "value": 100}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: got %d (%s)", res.StatusCode, body)
	}
	chID := decodeID(t, body)

	res, body = f.do(http.MethodPost, "/api/v1/admin/challenges/"+itoa(chID)+"/hints",
		map[string]any{"content": "look harder", "cost": 50}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("add hint: got %d (%s)", res.StatusCode, body)
	}
	hintID := decodeID(t, body)

	ctx := context.Background()
	var awardID int64
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO awards (user_id, type, challenge_id, name, value) VALUES ($1, 'hint_unlock', $2, 'hint', -50) RETURNING id`,
		adminID, chID).Scan(&awardID); err != nil {
		t.Fatalf("seed charge award: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO hint_unlocks (hint_id, user_id, award_id) VALUES ($1, $2, $3)`,
		hintID, adminID, awardID); err != nil {
		t.Fatalf("seed hint unlock: %v", err)
	}

	res, body = f.do(http.MethodDelete, "/api/v1/admin/challenges/"+itoa(chID)+"/hints/"+itoa(hintID), nil, auth...)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("delete unlocked hint: got %d, want 409 (%s)", res.StatusCode, body)
	}
	res, body = f.do(http.MethodDelete, "/api/v1/admin/challenges/"+itoa(chID), nil, auth...)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("delete challenge with unlocked hint: got %d, want 409 (%s)", res.StatusCode, body)
	}

	var unlocks, charges int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM hint_unlocks WHERE hint_id = $1`, hintID).Scan(&unlocks); err != nil {
		t.Fatalf("count unlocks: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM awards WHERE id = $1`, awardID).Scan(&charges); err != nil {
		t.Fatalf("count charge awards: %v", err)
	}
	if unlocks != 1 || charges != 1 {
		t.Errorf("ledger rows lost to a refused delete: unlocks=%d charges=%d, want 1 and 1", unlocks, charges)
	}
}

func TestAdminBanKillsSessions(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	adminAuth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	f.register("victim", "victim@example.com", "correct-horse-battery")
	victimID := f.userID("victim@example.com")
	if f.sessionCount(victimID) == 0 {
		t.Fatal("victim has no session before the ban — nothing to prove")
	}

	res, body := f.do(http.MethodPut, "/api/v1/admin/users/"+itoa(victimID)+"/ban",
		map[string]any{"banned": true}, adminAuth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ban: got %d, want 200 (%s)", res.StatusCode, body)
	}

	if got := f.sessionCount(victimID); got != 0 {
		t.Errorf("victim still has %d live session(s) after the ban", got)
	}
	adminID := f.userID("root@example.com")
	if got := f.auditCount("users", "UPDATE", adminID); got == 0 {
		t.Error("no UPDATE audit row for the ban")
	}
}

func TestAdminSelfBanRefused(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@example.com")

	res, body := f.do(http.MethodPut, "/api/v1/admin/users/"+itoa(adminID)+"/ban",
		map[string]any{"banned": true}, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("self-ban: got %d, want 409 (%s)", res.StatusCode, body)
	}
}

func TestAdminConfigUpdate(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	// A good PATCH lands and is attributed.
	res, body := f.do(http.MethodPatch, "/api/v1/admin/config",
		map[string]any{"name": "flagfish open", "challenge_visibility": "public"}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("config patch: got %d, want 200 (%s)", res.StatusCode, body)
	}
	var out struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode config: %v (%s)", err, body)
	}
	if out.Name != "flagfish open" {
		t.Errorf("name not applied: %q", out.Name)
	}
	if got := f.auditCount("config", "INSERT", adminID); got == 0 {
		if got2 := f.auditCount("config", "UPDATE", adminID); got2 == 0 {
			t.Error("no audit row for the config write")
		}
	}

	// An incoherent combination is refused whole — end before start — and nothing is stored.
	res, body = f.do(http.MethodPatch, "/api/v1/admin/config",
		map[string]any{"start": "2030-01-01T00:00:00Z", "end": "2020-01-01T00:00:00Z"}, auth...)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("incoherent times: got %d, want 422 (%s)", res.StatusCode, body)
	}
}

// itoa keeps the path building readable at the call sites.
func itoa(n int64) string { return strconv.FormatInt(n, 10) }
