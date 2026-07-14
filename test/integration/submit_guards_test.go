//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

// seedHiddenChallenge inserts a hidden challenge and returns its id. Hidden challenges must not be
// playable: attempts and hint unlocks against them are 404, indistinguishable from a missing one.
func (f *apiFix) seedHiddenChallenge(name, category string, value int) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO challenges (name, category, value, state) VALUES ($1,$2,$3,'hidden') RETURNING id`,
		name, category, value).Scan(&id); err != nil {
		f.t.Fatalf("seed hidden challenge: %v", err)
	}
	return id
}

// seedChallengeMaxAttempts inserts a visible challenge that caps wrong submissions per account.
func (f *apiFix) seedChallengeMaxAttempts(name, category string, value, maxAttempts int) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO challenges (name, category, value, max_attempts) VALUES ($1,$2,$3,$4) RETURNING id`,
		name, category, value, maxAttempts).Scan(&id); err != nil {
		f.t.Fatalf("seed max-attempts challenge: %v", err)
	}
	return id
}

// enableFirstBlood turns on first-blood detection for a challenge without a bonus payout.
func (f *apiFix) enableFirstBlood(challengeID int64) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE challenges SET first_blood = 'announce' WHERE id = $1`, challengeID); err != nil {
		f.t.Fatalf("enable first blood: %v", err)
	}
}

// seedTeam inserts a team and returns its id.
func (f *apiFix) seedTeam(name string) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO teams (name, email) VALUES ($1,$2) RETURNING id`,
		name, name+"@team.test").Scan(&id); err != nil {
		f.t.Fatalf("seed team: %v", err)
	}
	return id
}

// joinTeam attaches a registered user (by email) to a team. The principal is reloaded per request,
// so a subsequent authenticated call sees the new membership.
func (f *apiFix) joinTeam(email string, teamID int64) {
	f.t.Helper()
	tag, err := f.pool.Exec(context.Background(),
		`UPDATE users SET team_id = $1 WHERE lower(email) = lower($2)`, teamID, email)
	if err != nil {
		f.t.Fatalf("join team: %v", err)
	}
	if tag.RowsAffected() != 1 {
		f.t.Fatalf("join team: %d rows affected, want 1", tag.RowsAffected())
	}
}

func (f *apiFix) attemptPath(challengeID int64) string {
	return fmt.Sprintf("/api/v1/challenges/%d/attempt", challengeID)
}

func (f *apiFix) unlockPath(challengeID, hintID int64) string {
	return fmt.Sprintf("/api/v1/challenges/%d/hints/%d/unlock", challengeID, hintID)
}

func TestHiddenChallengeIsNotPlayable(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	chID := f.seedHiddenChallenge("Ghost", "misc", 100)
	f.seedFlag(chID, "flag{correct}")
	hintID := f.seedHint(chID, "invisible hint", 10)
	cookie, csrf := f.register("Ada", "ada@ctf.test", "correct horse battery")

	// A correct flag against a hidden challenge is a 404, not a solve.
	res, body := f.do(http.MethodPost, f.attemptPath(chID),
		map[string]any{"flag": "flag{correct}"}, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("hidden attempt: want 404, got %d (%s)", res.StatusCode, body)
	}

	res, body = f.do(http.MethodPost, f.unlockPath(chID, hintID), nil, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("hidden hint unlock: want 404, got %d (%s)", res.StatusCode, body)
	}
}

func TestMismatchedChallengeHintIs404(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	chA := f.seedChallenge("Alpha", "misc", 100)
	chB := f.seedChallenge("Bravo", "misc", 100)
	hintOnA := f.seedHint(chA, "belongs to A", 10)
	cookie, csrf := f.register("Ada", "ada@ctf.test", "correct horse battery")

	// The hint exists, but not under chB — the URL challenge id is part of its key.
	res, body := f.do(http.MethodPost, f.unlockPath(chB, hintOnA), nil, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("mismatched hint: want 404, got %d (%s)", res.StatusCode, body)
	}
}

func TestNonexistentChallengeAndHintAre404(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	chID := f.seedChallenge("Real", "misc", 100)
	cookie, csrf := f.register("Ada", "ada@ctf.test", "correct horse battery")

	res, body := f.do(http.MethodPost, f.attemptPath(999999),
		map[string]any{"flag": "flag{whatever}"}, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("attempt on missing challenge: want 404, got %d (%s)", res.StatusCode, body)
	}

	res, body = f.do(http.MethodPost, f.unlockPath(chID, 999999), nil, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unlock of missing hint: want 404, got %d (%s)", res.StatusCode, body)
	}
}

func TestInsufficientScoreUnlockIs402(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	chID := f.seedChallenge("Pricey", "misc", 100)
	hintID := f.seedHint(chID, "costs more than you have", 50)
	cookie, csrf := f.register("Ada", "ada@ctf.test", "correct horse battery")

	// Fresh account, zero points: a 50-cost hint is unaffordable.
	res, body := f.do(http.MethodPost, f.unlockPath(chID, hintID), nil, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("broke unlock: want 402, got %d (%s)", res.StatusCode, body)
	}
}

func TestMaxAttemptsCapsWrongAnswers(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	chID := f.seedChallengeMaxAttempts("Capped", "misc", 100, 3)
	f.seedFlag(chID, "flag{correct}")
	cookie, csrf := f.register("Ada", "ada@ctf.test", "correct horse battery")

	attempt := func(flag string) (apiResp, []byte) {
		return f.do(http.MethodPost, f.attemptPath(chID),
			map[string]any{"flag": flag}, withCookie(cookie), withCSRF(csrf))
	}

	// The first three wrong answers are recorded normally.
	for i := range 3 {
		res, body := attempt("flag{nope}")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("wrong attempt %d: want 200, got %d (%s)", i, res.StatusCode, body)
		}
		if got := decodeAttempt(t, body); got.Status != "incorrect" {
			t.Fatalf("wrong attempt %d: want incorrect, got %q", i, got.Status)
		}
	}

	// At the cap the fourth submission is rejected before the flag is even compared —
	// even a correct flag cannot get through.
	res, body := attempt("flag{correct}")
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("attempt at cap: want 403, got %d (%s)", res.StatusCode, body)
	}
}

func TestMaxAttemptsCorrectWithinCapSucceeds(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	chID := f.seedChallengeMaxAttempts("Capped", "misc", 100, 3)
	f.seedFlag(chID, "flag{correct}")
	cookie, csrf := f.register("Ada", "ada@ctf.test", "correct horse battery")

	attempt := func(flag string) (apiResp, []byte) {
		return f.do(http.MethodPost, f.attemptPath(chID),
			map[string]any{"flag": flag}, withCookie(cookie), withCSRF(csrf))
	}

	// Two wrong answers stay under the cap of three.
	for i := range 2 {
		if res, body := attempt("flag{nope}"); res.StatusCode != http.StatusOK {
			t.Fatalf("wrong attempt %d: want 200, got %d (%s)", i, res.StatusCode, body)
		}
	}
	res, body := attempt("flag{correct}")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("correct within cap: want 200, got %d (%s)", res.StatusCode, body)
	}
	if got := decodeAttempt(t, body); got.Status != "correct" {
		t.Fatalf("correct within cap: want correct, got %q (%s)", got.Status, body)
	}
}

func TestFirstBloodOnlyForFirstSolver(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	chID := f.seedChallenge("Blood", "misc", 100)
	f.seedFlag(chID, "flag{correct}")
	f.enableFirstBlood(chID)

	attemptAs := func(cookie, csrf string) attemptResult {
		res, body := f.do(http.MethodPost, f.attemptPath(chID),
			map[string]any{"flag": "flag{correct}"}, withCookie(cookie), withCSRF(csrf))
		if res.StatusCode != http.StatusOK {
			t.Fatalf("solve: want 200, got %d (%s)", res.StatusCode, body)
		}
		return decodeAttempt(t, body)
	}

	c1, x1 := f.register("Ada", "ada@ctf.test", "correct horse battery")
	c2, x2 := f.register("Bob", "bob@ctf.test", "correct horse battery")

	if got := attemptAs(c1, x1); !got.FirstBlood {
		t.Fatalf("first solver: want first_blood true, got false")
	}
	if got := attemptAs(c2, x2); got.FirstBlood {
		t.Fatalf("second solver: want first_blood false, got true")
	}
}

func TestTeamsModeAttribution(t *testing.T) {
	// The gameplay/accounts services take the mode directly; the request-time actor derives it from
	// config, so the config key has to agree or a member reads back as teamless.
	f := newAPI(t, account.ModeTeams, [2]string{"user_mode", "teams"})
	chID := f.seedChallenge("Team chal", "misc", 100)
	f.seedFlag(chID, "flag{correct}")
	teamID := f.seedTeam("Wolves")

	// A team member's solve is attributed to the team account.
	cookie, csrf := f.register("Member", "member@ctf.test", "correct horse battery")
	f.joinTeam("member@ctf.test", teamID)

	res, body := f.do(http.MethodPost, f.attemptPath(chID),
		map[string]any{"flag": "flag{correct}"}, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("member solve: want 200, got %d (%s)", res.StatusCode, body)
	}
	if got := decodeAttempt(t, body); got.Status != "correct" {
		t.Fatalf("member solve: want correct, got %q (%s)", got.Status, body)
	}
	var solveTeam *int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT team_id FROM solves WHERE challenge_id = $1`, chID).Scan(&solveTeam); err != nil {
		t.Fatalf("read solve attribution: %v", err)
	}
	if solveTeam == nil || *solveTeam != teamID {
		t.Fatalf("solve attribution: want team %d, got %v", teamID, solveTeam)
	}

	// A teamless user in teams mode has no account to play as: 403.
	tc, tx := f.register("Loner", "loner@ctf.test", "correct horse battery")
	res, body = f.do(http.MethodPost, f.attemptPath(chID),
		map[string]any{"flag": "flag{correct}"}, withCookie(tc), withCSRF(tx))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("teamless attempt: want 403, got %d (%s)", res.StatusCode, body)
	}
}
