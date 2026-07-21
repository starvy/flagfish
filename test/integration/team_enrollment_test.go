//go:build integration

package integration

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/starvy/flagfish/internal/accounts"
)

// seedTeamWithPassword inserts a team whose join secret is a known password, bypassing the create
// route — the tests below need a team to exist in a phase where the create route itself is closed.
func (f *apiFix) seedTeamWithPassword(name, password string) int64 {
	f.t.Helper()
	hash, err := accounts.Hash(password)
	if err != nil {
		f.t.Fatalf("hash: %v", err)
	}
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO teams (name, password_hash) VALUES ($1, $2) RETURNING id`, name, hash).Scan(&id); err != nil {
		f.t.Fatalf("seed team %s: %v", name, err)
	}
	return id
}

// The headline of bug A: a team must never be joinable by name alone. Creation without a real join
// secret is refused, and the two ways to slip past it — an empty password and a too-short one — are
// both rejected before a team can exist unprotected. Without the fix, the empty-password create
// returned 200 and the team admitted anyone who read its name off the scoreboard.
func TestTeamCannotBeJoinableByNameAlone(t *testing.T) {
	f := newTeamAPI(t)
	aCookie, aCSRF := f.register("Ada", "ada@ctf.test", "correct horse battery")

	for name, pw := range map[string]any{
		"empty password": "",
		"too short":      "short7!", // 7 chars
	} {
		res, body := f.do(http.MethodPost, "/api/v1/teams", map[string]any{
			"name": "Sealed-" + name, "password": pw,
		}, withCookie(aCookie), withCSRF(aCSRF))
		if res.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("create with %s: status %d, want 422 — a team without a real secret must not be created: %s",
				name, res.StatusCode, body)
		}
	}

	// A team created with a real secret does not admit an empty or a wrong password.
	f.createTeam(aCookie, aCSRF, "Sealed", "a-real-secret")
	bCookie, bCSRF := f.register("Bob", "bob@ctf.test", "correct horse battery")
	for name, pw := range map[string]string{"empty": "", "wrong": "not-the-secret"} {
		res, body := f.do(http.MethodPost, "/api/v1/teams/join", map[string]any{
			"name": "Sealed", "password": pw,
		}, withCookie(bCookie), withCSRF(bCSRF))
		if res.StatusCode != http.StatusForbidden {
			t.Fatalf("join Sealed with %s password: status %d, want 403: %s", name, res.StatusCode, body)
		}
	}
	// The real secret still lets a teammate in.
	res, body := f.do(http.MethodPost, "/api/v1/teams/join", map[string]any{
		"name": "Sealed", "password": "a-real-secret",
	}, withCookie(bCookie), withCSRF(bCSRF))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("join Sealed with the real secret: status %d, want 200: %s", res.StatusCode, body)
	}
}

// A team whose join secret was never chosen — a row that predates the requirement, or one inserted
// by a path that omitted it — carries the locking default and admits nobody until its secret is set.
func TestTeamLockedByDefaultRefusesEveryJoin(t *testing.T) {
	f := newTeamAPI(t)
	// Inserted without a password_hash, so the column default mints a locked one.
	var teamID int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO teams (name) VALUES ('Legacy') RETURNING id`).Scan(&teamID); err != nil {
		t.Fatalf("seed locked team: %v", err)
	}

	bCookie, bCSRF := f.register("Bob", "bob@ctf.test", "correct horse battery")
	for _, pw := range []string{"", "guess", "password", "a-real-secret"} {
		res, body := f.do(http.MethodPost, "/api/v1/teams/join", map[string]any{
			"name": "Legacy", "password": pw,
		}, withCookie(bCookie), withCSRF(bCSRF))
		if res.StatusCode != http.StatusForbidden {
			t.Fatalf("join a locked team with %q: status %d, want 403: %s", pw, res.StatusCode, body)
		}
	}
}

// A captain rotates the join password; the new one works and the old one does not. This is also the
// route that unlocks a team locked by the requirement's introduction.
func TestCaptainRotatesJoinPassword(t *testing.T) {
	f := newTeamAPI(t)
	aCookie, aCSRF := f.register("Ada", "ada@ctf.test", "correct horse battery")
	f.createTeam(aCookie, aCSRF, "Rotators", "first-secret")

	res, body := f.do(http.MethodPut, "/api/v1/me/team/password", map[string]any{
		"password": "second-secret",
	}, withCookie(aCookie), withCSRF(aCSRF))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("rotate join password: status %d, want 200: %s", res.StatusCode, body)
	}

	bCookie, bCSRF := f.register("Bob", "bob@ctf.test", "correct horse battery")
	res, _ = f.do(http.MethodPost, "/api/v1/teams/join", map[string]any{
		"name": "Rotators", "password": "first-secret",
	}, withCookie(bCookie), withCSRF(bCSRF))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("join with the old secret: status %d, want 403", res.StatusCode)
	}
	res, body = f.do(http.MethodPost, "/api/v1/teams/join", map[string]any{
		"name": "Rotators", "password": "second-secret",
	}, withCookie(bCookie), withCSRF(bCSRF))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("join with the rotated secret: status %d, want 200: %s", res.StatusCode, body)
	}

	// A non-captain cannot rotate it.
	res, _ = f.do(http.MethodPut, "/api/v1/me/team/password", map[string]any{
		"password": "member-tried",
	}, withCookie(bCookie), withCSRF(bCSRF))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("non-captain rotate: status %d, want 403", res.StatusCode)
	}
}

// The rotate route enforces the same minimum the create route does.
func TestRotateJoinPasswordRejectsTooShort(t *testing.T) {
	f := newTeamAPI(t)
	aCookie, aCSRF := f.register("Ada", "ada@ctf.test", "correct horse battery")
	f.createTeam(aCookie, aCSRF, "Shorties", "long-enough")

	res, body := f.do(http.MethodPut, "/api/v1/me/team/password", map[string]any{
		"password": "short7!",
	}, withCookie(aCookie), withCSRF(aCSRF))
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("rotate to a 7-char secret: status %d, want 422: %s", res.StatusCode, body)
	}
}

// Bug B: enrollment follows the event window. It is OPEN before the start (registration time) and
// during a freeze (the board is hidden, the game is not stopped), and CLOSED once the event ends.
// The three cases share one seeded team and one seeded joiner, so the only variable is the phase.
func TestTeamEnrollmentFollowsTheEventWindow(t *testing.T) {
	now := time.Now()

	for _, tc := range []struct {
		name       string
		cfg        [][2]string
		wantStatus int
	}{
		{
			name:       "before start enrollment is open",
			cfg:        [][2]string{{"start", unix(now.Add(time.Hour))}, {"end", unix(now.Add(2 * time.Hour))}},
			wantStatus: http.StatusOK,
		},
		{
			name:       "during a freeze enrollment is open",
			cfg:        [][2]string{{"freeze", unix(now.Add(-time.Minute))}},
			wantStatus: http.StatusOK,
		},
		{
			name:       "after end enrollment is closed",
			cfg:        [][2]string{{"start", unix(now.Add(-2 * time.Hour))}, {"end", unix(now.Add(-time.Hour))}},
			wantStatus: http.StatusForbidden,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTeamAPI(t, tc.cfg...)
			f.seedTeamWithPassword("Window", "window-secret")
			bCookie, bCSRF := f.register("Bob", "bob@ctf.test", "correct horse battery")

			res, body := f.do(http.MethodPost, "/api/v1/teams/join", map[string]any{
				"name": "Window", "password": "window-secret",
			}, withCookie(bCookie), withCSRF(bCSRF))
			if res.StatusCode != tc.wantStatus {
				t.Fatalf("join: status %d, want %d: %s", res.StatusCode, tc.wantStatus, body)
			}
			if tc.wantStatus == http.StatusForbidden && !bodyMentions(body, "ctf-ended") {
				t.Fatalf("after end, the denial must be the ended gate, not a join failure: %s", body)
			}
		})
	}
}

// After the event ends, leaving is closed too — a roster cannot shed a member to break an
// anti-cheat association once the standings are final.
func TestLeaveTeamClosedAfterEnd(t *testing.T) {
	now := time.Now()
	f := newTeamAPI(t,
		[2]string{"start", unix(now.Add(-2 * time.Hour))},
		[2]string{"end", unix(now.Add(-time.Hour))})

	// Seed a team and put Bob on it directly, since joining is itself closed after the end.
	teamID := f.seedTeamWithPassword("Sworn", "sworn-secret")
	bCookie, bCSRF := f.register("Bob", "bob@ctf.test", "correct horse battery")
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE users SET team_id = $1 WHERE email = 'bob@ctf.test'`, teamID); err != nil {
		t.Fatalf("assign bob: %v", err)
	}

	res, body := f.do(http.MethodPost, "/api/v1/me/team/leave", nil, withCookie(bCookie), withCSRF(bCSRF))
	if res.StatusCode != http.StatusForbidden || !bodyMentions(body, "ctf-ended") {
		t.Fatalf("leave after end: status %d, want 403 ctf-ended: %s", res.StatusCode, body)
	}
}

// Reading and profile-editing the caller's own team outlive the event: they move no one between
// rosters, so a player must still see the team they played on after the clock stops.
func TestOwnTeamReadableAfterEnd(t *testing.T) {
	now := time.Now()
	f := newTeamAPI(t,
		[2]string{"start", unix(now.Add(-2 * time.Hour))},
		[2]string{"end", unix(now.Add(-time.Hour))})

	teamID := f.seedTeamWithPassword("Alumni", "alumni-secret")
	aCookie, _ := f.register("Ada", "ada@ctf.test", "correct horse battery")
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE users SET team_id = $1 WHERE email = 'ada@ctf.test'`, teamID); err != nil {
		t.Fatalf("assign ada: %v", err)
	}

	res, body := f.do(http.MethodGet, "/api/v1/me/team", nil, withCookie(aCookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("read own team after end: status %d, want 200: %s", res.StatusCode, body)
	}
}

func unix(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

func bodyMentions(body []byte, needle string) bool {
	return strings.Contains(string(body), needle)
}
