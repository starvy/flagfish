//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

// seedChallengeWithReqs inserts a visible challenge carrying a requirements JSON and returns its id.
func (f *apiFix) seedChallengeWithReqs(name, category string, value int, requirements string) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO challenges (name, category, value, requirements) VALUES ($1,$2,$3,$4::jsonb) RETURNING id`,
		name, category, value, requirements).Scan(&id); err != nil {
		f.t.Fatalf("seed challenge with reqs: %v", err)
	}
	return id
}

// seedHintWithReqs inserts a hint carrying a requirements JSON and returns its id.
func (f *apiFix) seedHintWithReqs(challengeID int64, content string, cost int, requirements string) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO hints (challenge_id, content, cost, requirements) VALUES ($1,$2,$3,$4::jsonb) RETURNING id`,
		challengeID, content, cost, requirements).Scan(&id); err != nil {
		f.t.Fatalf("seed hint with reqs: %v", err)
	}
	return id
}

type detailBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Locked      bool   `json:"locked"`
	Hints       []struct {
		ID     int64 `json:"id"`
		Locked bool  `json:"locked"`
	} `json:"hints"`
	Files []struct {
		ID int64 `json:"id"`
	} `json:"files"`
}

func (f *apiFix) decodeDetail(body []byte) detailBody {
	f.t.Helper()
	var d detailBody
	if err := json.Unmarshal(body, &d); err != nil {
		f.t.Fatalf("decode detail: %v (%s)", err, body)
	}
	return d
}

// TestPrerequisiteBlocksSubmitUntilSolved: an attempt on a challenge whose prerequisite is unsolved
// is rejected; after the prerequisite is solved the same attempt succeeds.
func TestPrerequisiteBlocksSubmitUntilSolved(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	chA := f.seedChallenge("Alpha", "misc", 100)
	f.seedFlag(chA, "flag{a}")
	// Visible-but-locked, so the attempt is a 403 (locked), not a 404 (hidden).
	chB := f.seedChallengeWithReqs("Bravo", "misc", 200,
		fmt.Sprintf(`{"prerequisites":[%d],"anonymize":"preview"}`, chA))
	f.seedFlag(chB, "flag{b}")

	cookie, csrf := f.register("Ada", "ada@ctf.test", "correct horse battery")

	// Locked: even the correct flag for B is refused before A is solved.
	res, body := f.do(http.MethodPost, f.attemptPath(chB),
		map[string]any{"flag": "flag{b}"}, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("locked attempt: want 403, got %d (%s)", res.StatusCode, body)
	}

	// Solve the prerequisite.
	res, body = f.do(http.MethodPost, f.attemptPath(chA),
		map[string]any{"flag": "flag{a}"}, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK || decodeAttempt(t, body).Status != "correct" {
		t.Fatalf("solve prerequisite: want 200 correct, got %d (%s)", res.StatusCode, body)
	}

	// Now B is unlocked and solvable.
	res, body = f.do(http.MethodPost, f.attemptPath(chB),
		map[string]any{"flag": "flag{b}"}, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK || decodeAttempt(t, body).Status != "correct" {
		t.Fatalf("unlocked attempt: want 200 correct, got %d (%s)", res.StatusCode, body)
	}
}

// TestLockedHiddenChallengeIsInvisible: with the default (hidden) flag a locked challenge is absent
// from the board, 404s on detail, and 404s on attempt — its existence never leaks.
func TestLockedHiddenChallengeIsInvisible(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	chA := f.seedChallenge("Alpha", "misc", 100)
	f.seedFlag(chA, "flag{a}")
	chB := f.seedChallengeWithReqs("Bravo", "misc", 200,
		fmt.Sprintf(`{"prerequisites":[%d]}`, chA)) // no anonymize => hidden
	f.seedFlag(chB, "flag{b}")

	cookie, csrf := f.register("Ada", "ada@ctf.test", "correct horse battery")

	// The board shows only the unlocked challenge.
	res, body := f.do(http.MethodGet, "/api/v1/challenges", nil, withCookie(cookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list: status %d: %s", res.StatusCode, body)
	}
	list := f.decodeChallenges(body)
	if len(list.Challenges) != 1 || list.Challenges[0].Name != "Alpha" {
		t.Fatalf("board should hide the locked challenge, got %+v", list.Challenges)
	}

	// Detail and attempt are both indistinguishable from a missing challenge.
	res, _ = f.do(http.MethodGet, fmt.Sprintf("/api/v1/challenges/%d", chB), nil, withCookie(cookie))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("hidden-locked detail: want 404, got %d", res.StatusCode)
	}
	res, body = f.do(http.MethodPost, f.attemptPath(chB),
		map[string]any{"flag": "flag{b}"}, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("hidden-locked attempt: want 404, got %d (%s)", res.StatusCode, body)
	}
}

// TestLockedVisibleChallengeShowsNoSolvableContent: with a visible-but-locked flag the challenge
// appears masked on the board and its detail withholds description, files and hints; solving the
// prerequisite reveals the full challenge.
func TestLockedVisibleChallengeShowsNoSolvableContent(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	chA := f.seedChallenge("Alpha", "misc", 100)
	f.seedFlag(chA, "flag{a}")
	const secretDesc = "the description that helps you solve it"
	var chB int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO challenges (name, category, value, description, requirements)
		 VALUES ($1,$2,$3,$4,$5::jsonb) RETURNING id`,
		"Bravo", "misc", 200, secretDesc,
		fmt.Sprintf(`{"prerequisites":[%d],"anonymize":true}`, chA)).Scan(&chB); err != nil {
		t.Fatalf("seed masked challenge: %v", err)
	}
	f.seedHint(chB, "a hint on the locked challenge", 10)

	cookie, csrf := f.register("Ada", "ada@ctf.test", "correct horse battery")

	// On the board the locked challenge appears masked and flagged locked.
	res, body := f.do(http.MethodGet, "/api/v1/challenges", nil, withCookie(cookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list: status %d: %s", res.StatusCode, body)
	}
	list := f.decodeChallenges(body)
	if len(list.Challenges) != 2 {
		t.Fatalf("board should show both rows, got %+v", list.Challenges)
	}
	// The masked row must not carry the real name.
	names := map[string]bool{}
	for _, c := range list.Challenges {
		names[c.Name] = true
	}
	if names["Bravo"] {
		t.Fatalf("masked challenge leaked its real name: %+v", list.Challenges)
	}
	if !names["???"] {
		t.Fatalf("masked challenge should be shown as ???: %+v", list.Challenges)
	}

	// Detail is a locked stub: no description, no hints leaked.
	res, body = f.do(http.MethodGet, fmt.Sprintf("/api/v1/challenges/%d", chB), nil, withCookie(cookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("masked detail: want 200, got %d (%s)", res.StatusCode, body)
	}
	d := f.decodeDetail(body)
	if !d.Locked {
		t.Fatalf("masked detail must be locked: %s", body)
	}
	if d.Description == secretDesc {
		t.Fatalf("masked detail leaked the description: %s", body)
	}
	if len(d.Hints) != 0 {
		t.Fatalf("masked detail leaked hints: %s", body)
	}

	// Solving the prerequisite reveals the full challenge.
	res, body = f.do(http.MethodPost, f.attemptPath(chA),
		map[string]any{"flag": "flag{a}"}, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK || decodeAttempt(t, body).Status != "correct" {
		t.Fatalf("solve prerequisite: want 200 correct, got %d (%s)", res.StatusCode, body)
	}
	res, body = f.do(http.MethodGet, fmt.Sprintf("/api/v1/challenges/%d", chB), nil, withCookie(cookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unlocked detail: want 200, got %d", res.StatusCode)
	}
	d = f.decodeDetail(body)
	if d.Locked || d.Name != "Bravo" || d.Description != secretDesc || len(d.Hints) != 1 {
		t.Fatalf("unlocked detail should be full: %s", body)
	}
}

// score is the account's balance read the way the unlock path reads it: one sum over the ledger.
// A charge for a rejected unlock shows up here as missing points, which is the assertion below.
func (f *apiFix) score(userID int64) int64 {
	f.t.Helper()
	var n int64
	if err := f.pool.QueryRow(context.Background(), `
        SELECT COALESCE((SELECT sum(value) FROM solves WHERE user_id = $1), 0)
             + COALESCE((SELECT sum(value) FROM awards WHERE user_id = $1), 0)`, userID).Scan(&n); err != nil {
		f.t.Fatalf("score: %v", err)
	}
	return n
}

// TestChallengePrerequisiteGatesHintUnlock: a hint on a challenge whose prerequisites are unsolved
// cannot be bought — the detail view of that challenge returns no hints, so the hint does not exist
// for this account and the unlock answers exactly as a nonexistent one does. Nothing is charged for
// the refusal, and solving the prerequisite makes the same purchase go through.
//
// Hint ids are sequential, so without the gate the unlock endpoint reads out the hint content of the
// locked half of the board, and its 200-vs-404 is an existence oracle for the rest of it.
func TestChallengePrerequisiteGatesHintUnlock(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	chA := f.seedChallenge("Alpha", "misc", 100)
	f.seedFlag(chA, "flag{a}")
	// Visible-but-locked: 'visible' state, so the state filter GetHint already had says yes.
	chB := f.seedChallengeWithReqs("Bravo", "misc", 200,
		fmt.Sprintf(`{"prerequisites":[%d],"anonymize":"preview"}`, chA))
	const hintText = "the content of a hint on a challenge you may not open"
	hint := f.seedHint(chB, hintText, 30)

	// A challenge with no prerequisites, solved, so the account has points to lose.
	chC := f.seedChallenge("Charlie", "misc", 100)
	f.seedFlag(chC, "flag{c}")

	cookie, csrf := f.register("Ada", "ada@ctf.test", "correct horse battery")
	res, body := f.do(http.MethodPost, f.attemptPath(chC),
		map[string]any{"flag": "flag{c}"}, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK || decodeAttempt(t, body).Status != "correct" {
		t.Fatalf("fund the account: got %d (%s)", res.StatusCode, body)
	}
	userID := f.userID("ada@ctf.test")
	before := f.score(userID)
	if before != 100 {
		t.Fatalf("score before = %d, want 100", before)
	}

	res, body = f.do(http.MethodPost, f.unlockPath(chB, hint), nil, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unlock on a prerequisite-locked challenge: got %d, want 404 — the hint of a "+
			"challenge this account cannot open is being sold to it (%s)", res.StatusCode, body)
	}
	if strings.Contains(string(body), hintText) {
		t.Fatalf("the refusal carried the hint content: %s", body)
	}
	// The refusal is free: the gate runs before the account lock and before the charge.
	if got := f.score(userID); got != before {
		t.Errorf("score = %d after a refused unlock, want %d — a rejected purchase was charged for", got, before)
	}
	var unlocks int64
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM hint_unlocks`).Scan(&unlocks); err != nil {
		t.Fatalf("count unlocks: %v", err)
	}
	if unlocks != 0 {
		t.Errorf("hint_unlocks has %d rows after a refused unlock", unlocks)
	}

	// Solve the prerequisite and the same purchase goes through.
	res, body = f.do(http.MethodPost, f.attemptPath(chA),
		map[string]any{"flag": "flag{a}"}, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK || decodeAttempt(t, body).Status != "correct" {
		t.Fatalf("solve prerequisite: got %d (%s)", res.StatusCode, body)
	}

	res, body = f.do(http.MethodPost, f.unlockPath(chB, hint), nil, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unlock after the prerequisite is solved: got %d, want 200 (%s)", res.StatusCode, body)
	}
	u := decodeUnlock(t, body)
	if u.Content != hintText || u.Charged != 30 {
		t.Errorf("unlock = %+v, want the hint content charged 30", u)
	}
	if got := f.score(userID); got != 170 { // 100 + 100 solved, minus the 30 the hint cost
		t.Errorf("score = %d after the unlock, want 170", got)
	}
}

// TestHintPrerequisiteGatesUnlock: a hint whose prerequisite hint is not yet unlocked cannot be
// purchased; unlocking the prerequisite first lets it through.
func TestHintPrerequisiteGatesUnlock(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	ch := f.seedChallenge("Alpha", "misc", 100)
	h1 := f.seedHint(ch, "the first hint", 0)
	h2 := f.seedHintWithReqs(ch, "the gated hint", 0, fmt.Sprintf(`{"prerequisites":[%d]}`, h1))

	cookie, csrf := f.register("Ada", "ada@ctf.test", "correct horse battery")

	// h2 is locked behind h1.
	res, body := f.do(http.MethodPost, f.unlockPath(ch, h2), nil, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("locked hint unlock: want 403, got %d (%s)", res.StatusCode, body)
	}

	// Unlock the prerequisite (free), then h2 is purchasable.
	res, body = f.do(http.MethodPost, f.unlockPath(ch, h1), nil, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unlock prerequisite hint: want 200, got %d (%s)", res.StatusCode, body)
	}
	res, body = f.do(http.MethodPost, f.unlockPath(ch, h2), nil, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unlock gated hint after prerequisite: want 200, got %d (%s)", res.StatusCode, body)
	}
}
