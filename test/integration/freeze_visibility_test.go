//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
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

// The public team page is a scoreboard row with a roster attached. Served live during a freeze it
// hands an anonymous caller the post-freeze score of every team — `for id in 1..N` — and the
// per-member breakdown of who earned it, which is exactly what the freeze exists to hide. Both legs
// of the score and both member columns must clamp; drop the cutoff from either query and this reds.
func TestTeamProfileFreezeHidesPostFreezeSolves(t *testing.T) {
	freeze := time.Now().Add(-time.Hour)

	// The same team, seeded identically, read once under a freeze and once with none.
	seed := func(f *apiFix) teamView {
		aCookie, aCSRF := f.register("Ada", "ada@ctf.test", "correct horse battery")
		bCookie, bCSRF := f.register("Bob", "bob@ctf.test", "correct horse battery")
		team := f.createTeam(aCookie, aCSRF, "Bit Flippers", "hunter22")
		f.joinTeamByName(bCookie, bCSRF, "Bit Flippers", "hunter22")

		early := f.seedChallenge("Early", "rev", 100)
		late := f.seedChallenge("Late", "rev", 500)
		f.seedDatedTeamSolve(early, f.userIDByEmail("ada@ctf.test"), team.ID, 100, freeze.Add(-time.Hour))
		f.seedDatedTeamSolve(late, f.userIDByEmail("bob@ctf.test"), team.ID, 500, time.Now())
		return team
	}

	t.Run("frozen", func(t *testing.T) {
		f := newTeamAPI(t, [2]string{"freeze", strconv.FormatInt(freeze.Unix(), 10)})
		team := seed(f)

		// Anonymous, exactly as the attack would be run.
		profile := decodeTeam(t, f.get(t, fmt.Sprintf("/api/v1/teams/%d", team.ID)))
		if profile.Score != 100 {
			t.Fatalf("frozen team score = %d, want 100 (the post-freeze 500 must not count)", profile.Score)
		}
		if solves, points := memberScore(t, profile, "Ada"); solves != 1 || points != 100 {
			t.Errorf("Ada = %d solves / %d points, want 1/100", solves, points)
		}
		if solves, points := memberScore(t, profile, "Bob"); solves != 0 || points != 0 {
			t.Errorf("Bob = %d solves / %d points, want 0/0 — the freeze must hide who scored during it",
				solves, points)
		}
	})

	t.Run("no freeze", func(t *testing.T) {
		f := newTeamAPI(t)
		team := seed(f)

		profile := decodeTeam(t, f.get(t, fmt.Sprintf("/api/v1/teams/%d", team.ID)))
		if profile.Score != 600 {
			t.Fatalf("live team score = %d, want 600", profile.Score)
		}
		if solves, points := memberScore(t, profile, "Bob"); solves != 1 || points != 500 {
			t.Errorf("Bob = %d solves / %d points, want 1/500 once the freeze is lifted", solves, points)
		}
	})
}

// GET /me/team is the deliberate exception: an account always sees its own live score, freeze or
// not. Clamp this one too and a team cannot watch its own solves land.
func TestOwnTeamStaysLiveDuringFreeze(t *testing.T) {
	freeze := time.Now().Add(-time.Hour)
	f := newTeamAPI(t, [2]string{"freeze", strconv.FormatInt(freeze.Unix(), 10)})

	cookie, csrf := f.register("Ada", "ada@ctf.test", "correct horse battery")
	team := f.createTeam(cookie, csrf, "Bit Flippers", "hunter22")
	ada := f.userIDByEmail("ada@ctf.test")

	early := f.seedChallenge("Early", "rev", 100)
	late := f.seedChallenge("Late", "rev", 500)
	f.seedDatedTeamSolve(early, ada, team.ID, 100, freeze.Add(-time.Hour))
	f.seedDatedTeamSolve(late, ada, team.ID, 500, time.Now())

	mine := decodeTeam(t, f.getAuthed(t, "/api/v1/me/team", cookie))
	if mine.Score != 600 {
		t.Fatalf("own team score during a freeze = %d, want 600 (live) — own score is always visible", mine.Score)
	}
	if solves, points := memberScore(t, mine, "Ada"); solves != 2 || points != 600 {
		t.Errorf("own roster = %d solves / %d points, want 2/600 (live)", solves, points)
	}

	// And the public page for the very same team is still frozen.
	public := decodeTeam(t, f.get(t, fmt.Sprintf("/api/v1/teams/%d", team.ID)))
	if public.Score != 100 {
		t.Fatalf("public score of own team = %d, want 100 — /me/team is the only exception", public.Score)
	}
}

// A dynamic challenge's `value` is the solve count in disguise. solve_count is clamped to the freeze,
// but RecalcChallengeValue rewrites challenges.value on every solve with no time predicate — and the
// decay curve is public, deterministic and integer-exact, so n inverts straight out of the value. A
// frozen viewer who snapshots the price and polls the board counts the solves the freeze is hiding.
//
// So a frozen viewer is quoted the price at the FROZEN count: the value must not move when a
// post-freeze solve lands. A freeze-exempt admin sees it move, because for them nothing is hidden.
func TestDynamicChallengeValueFreezeHidesSolveCount(t *testing.T) {
	freeze := time.Now().Add(-time.Hour)
	f := newAPI(t, account.ModeUsers, [2]string{"freeze", strconv.FormatInt(freeze.Unix(), 10)})

	// linear, initial 500, decay 50: the curve is flat at the first solve and drops 50 per solve
	// after it. One pre-freeze solve, so the live price is still 500.
	ch := f.seedDynamicChallenge("Decayer", "rev", 500, 100, 50)
	f.seedFlag(ch, "flag{correct}")
	f.seedDatedSolve(ch, f.seedNamedUser("Early", "early@ctf.test"), 500, freeze.Add(-time.Hour))

	admin := f.registerAdmin("Boss", "boss@ctf.test", "correct horse battery")
	viewer, _ := f.register("Ada", "ada@ctf.test", "correct horse battery")

	if got := f.boardValue(t, "Decayer", viewer, ""); got != 500 {
		t.Fatalf("value before the post-freeze solve = %d, want 500", got)
	}

	// A real solve, through the real hot path, so RecalcChallengeValue actually runs.
	zed, zedCSRF := f.register("Zed", "zed@ctf.test", "correct horse battery")
	res, body := f.do(http.MethodPost, fmt.Sprintf("/api/v1/challenges/%d/attempt", ch),
		map[string]any{"flag": "flag{correct}"}, withCookie(zed), withCSRF(zedCSRF))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("post-freeze attempt: status %d: %s", res.StatusCode, body)
	}
	if got := decodeAttempt(t, body); got.Status != "correct" {
		t.Fatalf("post-freeze attempt: want correct, got %q (%s)", got.Status, body)
	}

	// Precondition: the stored column really did move. Without this the test could pass on a
	// challenge whose price never changed, and prove nothing at all.
	var stored int32
	if err := f.pool.QueryRow(context.Background(),
		`SELECT value FROM challenges WHERE id = $1`, ch).Scan(&stored); err != nil {
		t.Fatalf("read challenges.value: %v", err)
	}
	if stored != 450 {
		t.Fatalf("precondition: stored challenges.value = %d, want 450 (2 solves on the curve)", stored)
	}

	if got := f.boardValue(t, "Decayer", viewer, ""); got != 500 {
		t.Fatalf("frozen value = %d, want 500 — a value that tracks the live count IS the solve count", got)
	}

	// The freeze-exempt admin view sees the live price, and the live count with it.
	if got := f.boardValue(t, "Decayer", admin, "?view=admin"); got != 450 {
		t.Fatalf("admin (freeze-exempt) value = %d, want 450 (live)", got)
	}
}

// --- seed + decode helpers ------------------------------------------------

// seedDynamicChallenge inserts a visible linear-decay challenge whose current price starts at
// `initial` — the value of a challenge with at most one solve.
func (f *apiFix) seedDynamicChallenge(name, category string, initial, minimum, decay int) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO challenges (name, category, type, function, value, initial, minimum, decay)
		      VALUES ($1, $2, 'dynamic', 'linear', $3, $3, $4, $5) RETURNING id`,
		name, category, initial, minimum, decay).Scan(&id); err != nil {
		f.t.Fatalf("seed dynamic challenge: %v", err)
	}
	return id
}

func (f *apiFix) seedDatedTeamSolve(challengeID, userID, teamID int64, value int, at time.Time) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO solves (challenge_id, user_id, team_id, value, date) VALUES ($1,$2,$3,$4,$5)`,
		challengeID, userID, teamID, value, at); err != nil {
		f.t.Fatalf("seed team solve: %v", err)
	}
}

func (f *apiFix) userIDByEmail(email string) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT id FROM users WHERE email = $1`, email).Scan(&id); err != nil {
		f.t.Fatalf("user id for %s: %v", email, err)
	}
	return id
}

func (f *apiFix) joinTeamByName(cookie, csrf, name, password string) teamView {
	f.t.Helper()
	res, body := f.do(http.MethodPost, "/api/v1/teams/join", map[string]any{
		"name": name, "password": password,
	}, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		f.t.Fatalf("join team %s: status %d: %s", name, res.StatusCode, body)
	}
	return decodeTeam(f.t, body)
}

// boardValue reads one challenge's quoted price off GET /challenges. query carries the surface
// selector (e.g. "?view=admin") the freeze exemption keys on.
func (f *apiFix) boardValue(t *testing.T, name, cookie, query string) int32 {
	t.Helper()
	list := f.decodeChallenges(f.getAuthed(t, "/api/v1/challenges"+query, cookie))
	for _, c := range list.Challenges {
		if c.Name == name {
			return c.Value
		}
	}
	t.Fatalf("challenge %q not on the board: %+v", name, list.Challenges)
	return 0
}

func memberScore(t *testing.T, v teamView, name string) (solveCount, points int64) {
	t.Helper()
	for _, m := range v.Members {
		if m.Name == name {
			return m.SolveCount, m.Points
		}
	}
	t.Fatalf("member %q not on the roster: %+v", name, v.Members)
	return 0, 0
}

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
		Value      int32  `json:"value"`
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
