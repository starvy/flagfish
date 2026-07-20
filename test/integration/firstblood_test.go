//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

type fbChallenge struct {
	ID              int64  `json:"id"`
	FirstBlood      string `json:"first_blood"`
	FirstBloodBonus *int32 `json:"first_blood_bonus"`
}

func decodeFB(t *testing.T, body []byte) fbChallenge {
	t.Helper()
	var v fbChallenge
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode challenge: %v (%s)", err, body)
	}
	return v
}

// TestAdminFirstBloodCreateAndPatch: the first-blood mode and bonus are ordinary authoring fields
// now, and the pairing/positivity constraints arbitrate every combination the API can send.
func TestAdminFirstBloodCreateAndPatch(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	// bonus mode with a bonus lands and is echoed.
	res, body := f.do(http.MethodPost, "/api/v1/admin/challenges",
		map[string]any{
			"name": "fb", "category": "misc", "value": 100,
			"first_blood": "bonus", "first_blood_bonus": 50,
		}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create with bonus: got %d, want 201 (%s)", res.StatusCode, body)
	}
	ch := decodeFB(t, body)
	if ch.FirstBlood != "bonus" || ch.FirstBloodBonus == nil || *ch.FirstBloodBonus != 50 {
		t.Fatalf("echo = %+v, want bonus/50", ch)
	}

	// bonus mode without a bonus value is the pairing violation.
	res, body = f.do(http.MethodPost, "/api/v1/admin/challenges",
		map[string]any{"name": "fb2", "category": "misc", "value": 100, "first_blood": "bonus"}, auth...)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bonus without value: got %d, want 422 (%s)", res.StatusCode, body)
	}

	// a bonus value without bonus mode is the same violation from the other side.
	res, body = f.do(http.MethodPost, "/api/v1/admin/challenges",
		map[string]any{
			"name": "fb3", "category": "misc", "value": 100,
			"first_blood": "announce", "first_blood_bonus": 25,
		}, auth...)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("value without bonus mode: got %d, want 422 (%s)", res.StatusCode, body)
	}

	// zero and negative bonuses are refused at the boundary.
	for _, bonus := range []int{0, -10} {
		res, body = f.do(http.MethodPost, "/api/v1/admin/challenges",
			map[string]any{
				"name": "fb4", "category": "misc", "value": 100,
				"first_blood": "bonus", "first_blood_bonus": bonus,
			}, auth...)
		if res.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("bonus %d: got %d, want 422 (%s)", bonus, res.StatusCode, body)
		}
	}

	// Switching bonus → announce without clearing the bonus leaves a row the pairing CHECK refuses.
	res, body = f.do(http.MethodPatch, "/api/v1/admin/challenges/"+itoa(ch.ID),
		map[string]any{"first_blood": "announce"}, auth...)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("half-switch: got %d, want 422 (%s)", res.StatusCode, body)
	}

	// The same switch with an explicit null clears the bonus in the same PATCH.
	res, body = f.do(http.MethodPatch, "/api/v1/admin/challenges/"+itoa(ch.ID),
		map[string]any{"first_blood": "announce", "first_blood_bonus": nil}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("switch with clear: got %d, want 200 (%s)", res.StatusCode, body)
	}
	ch = decodeFB(t, body)
	if ch.FirstBlood != "announce" || ch.FirstBloodBonus != nil {
		t.Fatalf("after switch = %+v, want announce/nil", ch)
	}

	// The positivity rule is a constraint, not just handler validation: a write that sidesteps the
	// API entirely is refused by the database.
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE challenges SET first_blood = 'bonus', first_blood_bonus = 0 WHERE id = $1`, ch.ID); err == nil {
		t.Fatal("a zero bonus was stored — the positivity CHECK is missing")
	}
}

// TestFirstBloodAwardOnFirstSolve: an admin-authored bonus challenge pays the first solver and only
// the first solver, over the API end to end.
func TestFirstBloodAwardOnFirstSolve(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	res, body := f.do(http.MethodPost, "/api/v1/admin/challenges",
		map[string]any{
			"name": "blood", "category": "misc", "value": 100,
			"first_blood": "bonus", "first_blood_bonus": 30,
		}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: got %d (%s)", res.StatusCode, body)
	}
	chID := decodeFB(t, body).ID
	res, body = f.do(http.MethodPost, "/api/v1/admin/challenges/"+itoa(chID)+"/flags",
		map[string]any{"type": "static", "content": "flag{fb}"}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("add flag: got %d (%s)", res.StatusCode, body)
	}

	adaCookie, adaCSRF := f.register("Ada", "ada@ctf.test", "correct horse battery")
	res, body = f.do(http.MethodPost, f.attemptPath(chID),
		map[string]any{"flag": "flag{fb}"}, withCookie(adaCookie), withCSRF(adaCSRF))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("first solve: got %d (%s)", res.StatusCode, body)
	}
	if got := decodeAttempt(t, body); got.Status != "correct" || !got.FirstBlood {
		t.Fatalf("first solve = %+v, want correct with first blood", got)
	}

	var award int32
	if err := f.pool.QueryRow(context.Background(),
		`SELECT value FROM awards WHERE type = 'first_blood' AND challenge_id = $1 AND user_id = $2`,
		chID, f.userID("ada@ctf.test")).Scan(&award); err != nil {
		t.Fatalf("first-blood award row: %v", err)
	}
	if award != 30 {
		t.Errorf("award value = %d, want 30", award)
	}

	bobCookie, bobCSRF := f.register("Bob", "bob@ctf.test", "correct horse battery")
	res, body = f.do(http.MethodPost, f.attemptPath(chID),
		map[string]any{"flag": "flag{fb}"}, withCookie(bobCookie), withCSRF(bobCSRF))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("second solve: got %d (%s)", res.StatusCode, body)
	}
	if got := decodeAttempt(t, body); got.Status != "correct" || got.FirstBlood {
		t.Fatalf("second solve = %+v, want correct without first blood", got)
	}
}

// TestFirstBloodOnSolvedChallengeIsANoOp: enabling first blood after a solve is allowed — the switch
// is honest authoring — but the blood is already spent: no award is back-filled (a first-blood fact
// is stamped at solve time or never), and later solvers do not take it either.
func TestFirstBloodOnSolvedChallengeIsANoOp(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	res, body := f.do(http.MethodPost, "/api/v1/admin/challenges",
		map[string]any{"name": "late", "category": "misc", "value": 100}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: got %d (%s)", res.StatusCode, body)
	}
	chID := decodeFB(t, body).ID
	res, body = f.do(http.MethodPost, "/api/v1/admin/challenges/"+itoa(chID)+"/flags",
		map[string]any{"type": "static", "content": "flag{late}"}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("add flag: got %d (%s)", res.StatusCode, body)
	}

	adaCookie, adaCSRF := f.register("Ada", "ada@ctf.test", "correct horse battery")
	res, body = f.do(http.MethodPost, f.attemptPath(chID),
		map[string]any{"flag": "flag{late}"}, withCookie(adaCookie), withCSRF(adaCSRF))
	if res.StatusCode != http.StatusOK || decodeAttempt(t, body).Status != "correct" {
		t.Fatalf("pre-switch solve: got %d (%s)", res.StatusCode, body)
	}

	// Turning the bonus on afterwards is accepted, not refused.
	res, body = f.do(http.MethodPatch, "/api/v1/admin/challenges/"+itoa(chID),
		map[string]any{"first_blood": "bonus", "first_blood_bonus": 40}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("enable after solve: got %d, want 200 (%s)", res.StatusCode, body)
	}

	// No back-fill for the solver who was first before the switch.
	var awards int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM awards WHERE type = 'first_blood' AND challenge_id = $1`, chID).Scan(&awards); err != nil {
		t.Fatalf("count awards: %v", err)
	}
	if awards != 0 {
		t.Errorf("%d first-blood award(s) back-filled — the fact is stamped at solve time or never", awards)
	}

	// And the next solver is not "first" either: the blood was spent before the switch.
	bobCookie, bobCSRF := f.register("Bob", "bob@ctf.test", "correct horse battery")
	res, body = f.do(http.MethodPost, f.attemptPath(chID),
		map[string]any{"flag": "flag{late}"}, withCookie(bobCookie), withCSRF(bobCSRF))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("post-switch solve: got %d (%s)", res.StatusCode, body)
	}
	if got := decodeAttempt(t, body); got.FirstBlood {
		t.Fatal("a later solver took first blood on an already-solved challenge")
	}
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM awards WHERE type = 'first_blood' AND challenge_id = $1`, chID).Scan(&awards); err != nil {
		t.Fatalf("count awards: %v", err)
	}
	if awards != 0 {
		t.Errorf("%d first-blood award(s) for a non-first solver", awards)
	}
}
