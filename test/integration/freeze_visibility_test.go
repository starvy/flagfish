//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/starvy/flagfish/internal/domain/account"
)

// A non-admin viewing the scoreboard during a freeze must see it as it stood at the freeze — never a
// solve that landed after. Remove the clamp in the handler and the post-freeze solver appears.
func TestScoreboardFreezeHidesPostFreezeSolves(t *testing.T) {
	freeze := time.Now().Add(-time.Hour)
	f := newAPI(t, account.ModeUsers, [2]string{"freeze", strconv.FormatInt(freeze.Unix(), 10)})

	ch := f.seedChallenge("Reversing", "rev", 100)
	early := f.seedNamedUser("Early", "early@ctf.test")
	late := f.seedNamedUser("Late", "late@ctf.test")
	f.seedDatedSolve(ch, early, 100, freeze.Add(-time.Hour))
	f.seedDatedSolve(ch, late, 500, time.Now())

	board := f.decodeStandings(f.get(t, "/api/v1/scoreboard"))
	if !board.has("Early") {
		t.Fatalf("pre-freeze solver missing from frozen board: %+v", board.Standings)
	}
	if board.has("Late") {
		t.Fatalf("post-freeze solver leaked through the freeze: %+v", board.Standings)
	}
}

// The public scoreboard is frozen for EVERYONE, admins included — an admin who wants live data uses
// the admin surface. Key the freeze on IsAdmin instead of the surface and this test sees "Late" leak.
func TestScoreboardFreezeAppliesToAdmin(t *testing.T) {
	freeze := time.Now().Add(-time.Hour)
	f := newAPI(t, account.ModeUsers, [2]string{"freeze", strconv.FormatInt(freeze.Unix(), 10)})

	ch := f.seedChallenge("Reversing", "rev", 100)
	early := f.seedNamedUser("Early", "early@ctf.test")
	late := f.seedNamedUser("Late", "late@ctf.test")
	f.seedDatedSolve(ch, early, 100, freeze.Add(-time.Hour))
	f.seedDatedSolve(ch, late, 500, time.Now())

	admin := f.registerAdmin("Boss", "boss@ctf.test", "correct horse battery")

	_, body := f.do(http.MethodGet, "/api/v1/scoreboard", nil, withCookie(admin))
	board := f.decodeStandings(body)
	if !board.has("Early") {
		t.Fatalf("admin missing the pre-freeze solver from the frozen public board: %+v", board.Standings)
	}
	if board.has("Late") {
		t.Fatalf("admin saw the post-freeze solver on the PUBLIC board — the freeze must apply: %+v", board.Standings)
	}
}

// A frozen board reports the score as it stood at the freeze, not the live total. One account with a
// pre-freeze solve worth 100 and a post-freeze solve worth 500 must show exactly 100.
func TestScoreboardFreezeClampsScore(t *testing.T) {
	freeze := time.Now().Add(-time.Hour)
	f := newAPI(t, account.ModeUsers, [2]string{"freeze", strconv.FormatInt(freeze.Unix(), 10)})

	first := f.seedChallenge("First", "rev", 100)
	second := f.seedChallenge("Second", "rev", 500)
	solo := f.seedNamedUser("Solo", "solo@ctf.test")
	f.seedDatedSolve(first, solo, 100, freeze.Add(-time.Hour))
	f.seedDatedSolve(second, solo, 500, time.Now())

	board := f.decodeStandings(f.get(t, "/api/v1/scoreboard"))
	got, ok := board.scoreOf("Solo")
	if !ok {
		t.Fatalf("Solo missing from the frozen board: %+v", board.Standings)
	}
	if got != 100 {
		t.Fatalf("frozen score = %d, want 100 (the post-freeze 500 must not count)", got)
	}
}

// A frozen per-challenge solve list hides a solver who landed after the freeze, the same way the board
// does. Drop the cutoff on the solves query and the late solver reappears.
func TestChallengeSolvesFreezeHidesLateSolver(t *testing.T) {
	freeze := time.Now().Add(-time.Hour)
	f := newAPI(t, account.ModeUsers, [2]string{"freeze", strconv.FormatInt(freeze.Unix(), 10)})

	ch := f.seedChallenge("Reversing", "rev", 100)
	early := f.seedNamedUser("Early", "early@ctf.test")
	late := f.seedNamedUser("Late", "late@ctf.test")
	f.seedDatedSolve(ch, early, 100, freeze.Add(-time.Hour))
	f.seedDatedSolve(ch, late, 500, time.Now())

	cookie, _ := f.register("Ada", "ada@ctf.test", "correct horse battery")
	_, body := f.do(http.MethodGet, "/api/v1/challenges/"+strconv.FormatInt(ch, 10)+"/solves", nil, withCookie(cookie))
	names := f.decodeSolveNames(body)
	if !contains(names, "Early") {
		t.Fatalf("pre-freeze solver missing from frozen solve list: %v", names)
	}
	if contains(names, "Late") {
		t.Fatalf("post-freeze solver leaked through the frozen solve list: %v", names)
	}
}

// solve_count leaks nothing only when BOTH scores and accounts are hidden: null under
// score_visibility=hidden + account_visibility=admins for a non-admin, present under public/public.
func TestChallengeSolveCountRedaction(t *testing.T) {
	seedOneSolve := func(f *apiFix) int64 {
		ch := f.seedChallenge("Reversing", "rev", 100)
		solver := f.seedNamedUser("Solver", "solver@ctf.test")
		f.seedDatedSolve(ch, solver, 100, time.Now())
		return ch
	}

	t.Run("hidden", func(t *testing.T) {
		f := newAPI(t, account.ModeUsers,
			[2]string{"score_visibility", "hidden"},
			[2]string{"account_visibility", "admins"})
		seedOneSolve(f)
		cookie, _ := f.register("Ada", "ada@ctf.test", "correct horse battery")

		list := f.decodeChallenges(f.getAuthed(t, "/api/v1/challenges", cookie))
		if len(list.Challenges) != 1 {
			t.Fatalf("want one challenge, got %+v", list.Challenges)
		}
		if list.Challenges[0].SolveCount != nil {
			t.Fatalf("solve_count leaked while scores and accounts are hidden: %v", *list.Challenges[0].SolveCount)
		}
	})

	t.Run("public", func(t *testing.T) {
		f := newAPI(t, account.ModeUsers)
		seedOneSolve(f)
		cookie, _ := f.register("Ada", "ada@ctf.test", "correct horse battery")

		list := f.decodeChallenges(f.getAuthed(t, "/api/v1/challenges", cookie))
		if len(list.Challenges) != 1 {
			t.Fatalf("want one challenge, got %+v", list.Challenges)
		}
		got := list.Challenges[0].SolveCount
		if got == nil || *got != 1 {
			t.Fatalf("solve_count = %v, want 1 under public visibility", got)
		}
	})
}

// The solve list of a challenge that is not visible must 404, exactly like the detail route — a 200
// with an empty list tells the caller the id exists.
func TestChallengeSolvesUnknownChallenge404(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	cookie, _ := f.register("Ada", "ada@ctf.test", "correct horse battery")

	res, _ := f.do(http.MethodGet, "/api/v1/challenges/999999/solves", nil, withCookie(cookie))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 for the solves of an unknown challenge, got %d", res.StatusCode)
	}
}

// The solve list must not reveal who solved a hidden challenge. A hidden challenge is not visible, so
// its solves route 404s just as its detail route does.
func TestSolvesHideNonVisibleChallenge(t *testing.T) {
	f := newAPI(t, account.ModeUsers)

	var hidden int64
	if err := f.pool.QueryRow(
		context.Background(),
		`INSERT INTO challenges (name, category, value, state) VALUES ('Secret','misc',100,'hidden') RETURNING id`,
	).Scan(&hidden); err != nil {
		t.Fatalf("seed hidden challenge: %v", err)
	}
	solver := f.seedNamedUser("Insider", "insider@ctf.test")
	f.seedDatedSolve(hidden, solver, 100, time.Now())

	cookie, _ := f.register("Ada", "ada@ctf.test", "correct horse battery")

	res, _ := f.do(http.MethodGet, "/api/v1/challenges/"+strconv.FormatInt(hidden, 10)+"/solves", nil, withCookie(cookie))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("solves of a hidden challenge should 404, got %d", res.StatusCode)
	}

	res, _ = f.do(http.MethodGet, "/api/v1/challenges/"+strconv.FormatInt(hidden, 10), nil, withCookie(cookie))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("hidden challenge detail should 404, got %d", res.StatusCode)
	}
}

// --- seed + decode helpers ------------------------------------------------

func (f *apiFix) seedNamedUser(name, email string) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO users (name, email, verified) VALUES ($1,$2,true) RETURNING id`, name, email).Scan(&id); err != nil {
		f.t.Fatalf("seed user %s: %v", name, err)
	}
	return id
}

func (f *apiFix) seedDatedSolve(challengeID, userID int64, value int, at time.Time) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO solves (challenge_id, user_id, value, date) VALUES ($1,$2,$3,$4)`,
		challengeID, userID, value, at); err != nil {
		f.t.Fatalf("seed solve: %v", err)
	}
}

// registerAdmin registers through the real endpoint, then promotes the account. The principal is
// resolved per request, so the returned session cookie is an admin's on the next call.
func (f *apiFix) registerAdmin(name, email, password string) string {
	f.t.Helper()
	cookie, _ := f.register(name, email, password)
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE users SET role='admin' WHERE email=$1`, email); err != nil {
		f.t.Fatalf("promote %s to admin: %v", email, err)
	}
	return cookie
}

func (f *apiFix) get(t *testing.T, path string) []byte {
	t.Helper()
	res, body := f.do(http.MethodGet, path, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", path, res.StatusCode, body)
	}
	return body
}

func (f *apiFix) getAuthed(t *testing.T, path, cookie string) []byte {
	t.Helper()
	res, body := f.do(http.MethodGet, path, nil, withCookie(cookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", path, res.StatusCode, body)
	}
	return body
}

type standings struct {
	Standings []struct {
		Name  string `json:"name"`
		Score int64  `json:"score"`
	} `json:"standings"`
}

func (s standings) has(name string) bool {
	_, ok := s.scoreOf(name)
	return ok
}

func (s standings) scoreOf(name string) (int64, bool) {
	for _, e := range s.Standings {
		if e.Name == name {
			return e.Score, true
		}
	}
	return 0, false
}

func (f *apiFix) decodeStandings(body []byte) standings {
	f.t.Helper()
	var s standings
	if err := json.Unmarshal(body, &s); err != nil {
		f.t.Fatalf("decode standings: %v (%s)", err, body)
	}
	return s
}

type challengeList struct {
	Challenges []struct {
		Name       string `json:"name"`
		Category   string `json:"category"`
		SolveCount *int64 `json:"solve_count"`
		Solved     bool   `json:"solved"`
	} `json:"challenges"`
}

func (f *apiFix) decodeChallenges(body []byte) challengeList {
	f.t.Helper()
	var l challengeList
	if err := json.Unmarshal(body, &l); err != nil {
		f.t.Fatalf("decode challenges: %v (%s)", err, body)
	}
	return l
}

func (f *apiFix) decodeSolveNames(body []byte) []string {
	f.t.Helper()
	var s struct {
		Solves []struct {
			Name string `json:"name"`
		} `json:"solves"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		f.t.Fatalf("decode solves: %v (%s)", err, body)
	}
	names := make([]string, len(s.Solves))
	for i, v := range s.Solves {
		names[i] = v.Name
	}
	return names
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
