//go:build integration

package security

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// GET /challenges/{id}/solves is the one public route that names accounts one row at a time. Three
// separate walls have to hold on it and each is pinned below: the route's account-visibility gate,
// the redaction of the list itself, and the bound on how much of it one request can take.

// The redaction is omission rather than anonymised rows on purpose: the length of this list IS the
// solve count, which is nulled under the same condition, so N anonymous rows would give back by
// difference exactly what the count withholds.
func TestSolveListRespectsVisibility(t *testing.T) {
	cases := []struct {
		name       string
		cfg        []func(*fixOpts)
		asAdmin    bool
		wantStatus int
		wantNames  []string
	}{
		{
			name:       "public instance: a player reads the roll of honour",
			wantStatus: http.StatusOK,
			wantNames:  []string{"Ada", "Bob"},
		},
		{
			// The gate, not the redactor: accounts that hide their existence 404 rather than 403.
			name:       "account_visibility=admins: a verified player gets nothing",
			cfg:        []func(*fixOpts){withConfig("account_visibility", "admins")},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "account_visibility=admins: an admin still sees every solver",
			cfg:        []func(*fixOpts){withConfig("account_visibility", "admins")},
			asAdmin:    true,
			wantStatus: http.StatusOK,
			wantNames:  []string{"Ada", "Bob"},
		},
		{
			// The redactor, not the gate: accounts are public here, so the route is open and the
			// list still has to be withheld — the count is nulled while scores are hidden, and a
			// list of rows is a count.
			name:       "score_visibility=admins: the route is open, the list is not",
			cfg:        []func(*fixOpts){withConfig("score_visibility", "admins")},
			wantStatus: http.StatusOK,
		},
		{
			name:       "score_visibility=admins: an admin still sees every solver",
			cfg:        []func(*fixOpts){withConfig("score_visibility", "admins")},
			asAdmin:    true,
			wantStatus: http.StatusOK,
			wantNames:  []string{"Ada", "Bob"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t, tc.cfg...)
			ch := f.challenge("Reversing", 100)
			f.solveAt(ch, f.user("Ada", pw), 100, time.Now().Add(-2*time.Hour))
			f.solveAt(ch, f.user("Bob", pw), 100, time.Now().Add(-time.Hour))

			// The caller is never one of the solvers: their own name in the body would make a
			// "no names leaked" assertion pass for the wrong reason.
			res := f.do(http.MethodGet, solvesPath(ch), withCookie(f.session(t, tc.asAdmin)))
			if res.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", res.StatusCode, tc.wantStatus, res.Body)
			}
			if got := decodeSolveNames(t, res.Body); !equalNames(got, tc.wantNames) {
				t.Errorf("solver names = %v, want %v", got, tc.wantNames)
			}
			// Belt and braces: a name that leaked through some other field is still a leak.
			if len(tc.wantNames) == 0 {
				for _, name := range []string{"Ada", "Bob"} {
					if strings.Contains(res.Body, name) {
						t.Errorf("solver %q leaked in the body: %s", name, res.Body)
					}
				}
			}
		})
	}
}

// The freeze horizon is the other half of this route's contract, and the page bound must not have
// cost it: a non-admin sees the list as it stood at the freeze, an admin sees it live.
func TestSolveListStillHonoursTheFreeze(t *testing.T) {
	freeze := time.Now().Add(-time.Hour)
	f := setup(t, withConfig("freeze", strconv.FormatInt(freeze.Unix(), 10)))

	ch := f.challenge("Reversing", 100)
	f.solveAt(ch, f.user("Early", pw), 100, freeze.Add(-time.Hour))
	f.solveAt(ch, f.user("Late", pw), 100, time.Now())

	player := f.do(http.MethodGet, solvesPath(ch), withCookie(f.session(t, false)))
	if got := decodeSolveNames(t, player.Body); !equalNames(got, []string{"Early"}) {
		t.Errorf("player sees %v, want only Early — the post-freeze solver must stay hidden", got)
	}

	admin := f.do(http.MethodGet, solvesPath(ch), withCookie(f.session(t, true)))
	if got := decodeSolveNames(t, admin.Body); !equalNames(got, []string{"Early", "Late"}) {
		t.Errorf("admin sees %v, want both — admins are exempt from the freeze on this route", got)
	}
}

// A popular challenge at a large event has thousands of solvers. One request must never be able to
// take them all: the bound is the server's, and a caller asking for more than the cap is refused
// rather than quietly served.
func TestSolveListIsBounded(t *testing.T) {
	const (
		solvers     = 120 // more than any page the API will serve
		defaultPage = 50
		maxPage     = 100
	)

	f := setup(t)
	ch := f.challenge("Popular", 100)
	f.seedSolvers(ch, solvers)
	sess := f.session(t, false)

	res := f.do(http.MethodGet, solvesPath(ch), withCookie(sess))
	if got := len(decodeSolveNames(t, res.Body)); got != defaultPage {
		t.Errorf("unbounded request returned %d solvers, want the default page of %d", got, defaultPage)
	}

	res = f.do(http.MethodGet, solvesPath(ch)+"?limit="+strconv.Itoa(maxPage), withCookie(sess))
	if got := len(decodeSolveNames(t, res.Body)); got != maxPage {
		t.Errorf("limit=%d returned %d solvers", maxPage, got)
	}

	res = f.do(http.MethodGet, solvesPath(ch)+"?limit="+strconv.Itoa(maxPage+1), withCookie(sess))
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("limit above the cap = %d, want 422 — the cap must refuse, not silently clamp", res.StatusCode)
	}

	res = f.do(http.MethodGet, solvesPath(ch)+"?cursor=not-a-cursor", withCookie(sess))
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("malformed cursor = %d, want 422", res.StatusCode)
	}

	// The bound is a page, not a truncation: following the cursor still walks every solver, once.
	seen := map[string]bool{}
	path := solvesPath(ch) + "?limit=" + strconv.Itoa(maxPage)
	for page := 0; ; page++ {
		if page > solvers { // a cursor that never ends is a bug, not a slow test
			t.Fatalf("cursor did not terminate after %d pages", page)
		}
		res := f.do(http.MethodGet, path, withCookie(sess))
		body := decodeSolves(t, res.Body)
		for _, s := range body.Solves {
			if seen[s.Name] {
				t.Fatalf("solver %q served twice while paging", s.Name)
			}
			seen[s.Name] = true
		}
		if body.NextCursor == "" {
			break
		}
		path = solvesPath(ch) + "?limit=" + strconv.Itoa(maxPage) + "&cursor=" + body.NextCursor
	}
	if len(seen) != solvers {
		t.Errorf("paged through %d solvers, want %d", len(seen), solvers)
	}
}

func solvesPath(challengeID int64) string {
	return "/api/v1/challenges/" + strconv.FormatInt(challengeID, 10) + "/solves"
}

// session logs in a fresh non-solver account — a verified player, or an admin — and returns its
// session id.
func (f *fixture) session(t *testing.T, admin bool) string {
	t.Helper()
	name := "viewer"
	var mut []func(*userOpts)
	if admin {
		name = "root"
		mut = []func(*userOpts){asAdmin}
	}
	f.user(name, pw, mut...)
	sess, err := f.acct.Login(context.Background(), name+"@ctf.test", pw)
	if err != nil {
		t.Fatalf("login %s: %v", name, err)
	}
	return sess.ID
}

func (f *fixture) challenge(name string, value int) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO challenges (name, category, value) VALUES ($1,'rev',$2) RETURNING id`,
		name, value).Scan(&id); err != nil {
		f.t.Fatalf("seed challenge: %v", err)
	}
	return id
}

func (f *fixture) solveAt(challengeID, userID int64, value int, at time.Time) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO solves (challenge_id, user_id, value, date) VALUES ($1,$2,$3,$4)`,
		challengeID, userID, value, at); err != nil {
		f.t.Fatalf("seed solve: %v", err)
	}
}

// seedSolvers gives a challenge n distinct solvers in one round trip. The password hashes are
// junk because nothing logs in as them — hashing n times with the real KDF is seconds of Argon2
// spent on accounts that only need to exist.
func (f *fixture) seedSolvers(challengeID int64, n int) {
	f.t.Helper()
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `
        INSERT INTO users (name, email, password_hash, role, verified)
        SELECT 'solver-'||i, 'solver-'||i||'@ctf.test', 'x', 'user', true
          FROM generate_series(1, $1) AS i`, n); err != nil {
		f.t.Fatalf("seed solvers: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
        INSERT INTO solves (challenge_id, user_id, value, date)
        SELECT $1, u.id, 100, now() - (u.id * interval '1 minute')
          FROM users u WHERE u.name LIKE 'solver-%'`, challengeID); err != nil {
		f.t.Fatalf("seed solves: %v", err)
	}
}

type solvesBody struct {
	Solves []struct {
		Name  string    `json:"name"`
		Value int32     `json:"value"`
		Date  time.Time `json:"date"`
	} `json:"solves"`
	NextCursor string `json:"next_cursor"`
}

func decodeSolves(t *testing.T, body string) solvesBody {
	t.Helper()
	var out solvesBody
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode solves: %v (%s)", err, body)
	}
	return out
}

// decodeSolveNames reads the solver names out of a response, and reads a denial as no names at
// all — a 404 body is an RFC 7807 problem document, which carries none.
func decodeSolveNames(t *testing.T, body string) []string {
	t.Helper()
	var out solvesBody
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return nil
	}
	names := make([]string, 0, len(out.Solves))
	for _, s := range out.Solves {
		names = append(names, s.Name)
	}
	return names
}

func equalNames(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
