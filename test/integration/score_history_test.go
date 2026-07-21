//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/starvy/flagfish/internal/domain/account"
)

type historyView struct {
	AccountID int64 `json:"account_id"`
	Points    []struct {
		Date  time.Time `json:"date"`
		Delta int64     `json:"delta"`
		Score int64     `json:"score"`
	} `json:"points"`
}

func (f *apiFix) decodeHistory(body []byte) historyView {
	f.t.Helper()
	var v historyView
	if err := json.Unmarshal(body, &v); err != nil {
		f.t.Fatalf("decode score history: %v (%s)", err, body)
	}
	return v
}

func historyPath(id int64) string { return "/api/v1/scoreboard/" + strconv.FormatInt(id, 10) }

func historyAt(id int64, asOf time.Time) string {
	v := url.Values{}
	v.Set("as_of", asOf.UTC().Format(time.RFC3339Nano))
	return historyPath(id) + "?" + v.Encode()
}

// last returns the final cumulative score on the curve, or 0 for an empty curve.
func (h historyView) last() int64 {
	if len(h.Points) == 0 {
		return 0
	}
	return h.Points[len(h.Points)-1].Score
}

// The curve is the running total of the ledger: two solves and one award accumulate in date order,
// and each point's delta is that event's own value.
func TestScoreHistoryCumulative(t *testing.T) {
	f := newInsightAPI(t, account.ModeUsers)
	ch1 := f.seedChallenge("A", "web", 100)
	ch2 := f.seedChallenge("B", "pwn", 250)
	ada := f.seedNamedUser("Ada", "ada@ctf.test")
	base := time.Now().Add(-3 * time.Hour)
	f.seedDatedSolve(ch1, ada, 100, base)
	f.seedDatedSolve(ch2, ada, 250, base.Add(time.Hour))
	if _, err := f.pool.Exec(f.t.Context(),
		`INSERT INTO awards (user_id, type, name, value, date) VALUES ($1,'standard','bonus',50,$2)`,
		ada, base.Add(2*time.Hour)); err != nil {
		t.Fatalf("seed award: %v", err)
	}

	h := f.decodeHistory(f.get(t, historyPath(ada)))
	if len(h.Points) != 3 {
		t.Fatalf("want 3 points, got %d: %+v", len(h.Points), h.Points)
	}
	wantDeltas := []int64{100, 250, 50}
	wantScores := []int64{100, 350, 400}
	for i, p := range h.Points {
		if p.Delta != wantDeltas[i] || p.Score != wantScores[i] {
			t.Fatalf("point %d: got delta=%d score=%d, want delta=%d score=%d",
				i, p.Delta, p.Score, wantDeltas[i], wantScores[i])
		}
	}
}

// The graph is freeze-safe: a non-admin viewer's curve stops at the freeze horizon, so a solve that
// landed after the freeze never appears — the public-profile leak the freeze exists to prevent.
func TestScoreHistoryFrozenForNonAdmin(t *testing.T) {
	freeze := time.Now().Add(-time.Hour)
	f := newInsightAPI(t, account.ModeUsers, withFreeze(freeze))
	ch1 := f.seedChallenge("A", "web", 100)
	ch2 := f.seedChallenge("B", "pwn", 200)
	ada := f.seedNamedUser("Ada", "ada@ctf.test")
	f.seedDatedSolve(ch1, ada, 100, freeze.Add(-30*time.Minute)) // before freeze: visible
	f.seedDatedSolve(ch2, ada, 200, freeze.Add(30*time.Minute))  // after freeze: hidden

	h := f.decodeHistory(f.get(t, historyPath(ada)))
	if h.last() != 100 {
		t.Fatalf("frozen curve leaked the post-freeze solve: final score %d, want 100: %+v", h.last(), h.Points)
	}
}

// An admin on the PUBLIC endpoint is still frozen — the freeze exemption for this class is the admin
// surface, not the role. The same request that leaks on the admin surface must not leak here.
func TestScoreHistoryFrozenEvenForAdminOnPublic(t *testing.T) {
	freeze := time.Now().Add(-time.Hour)
	f := newInsightAPI(t, account.ModeUsers, withFreeze(freeze))
	ch1 := f.seedChallenge("A", "web", 100)
	ch2 := f.seedChallenge("B", "pwn", 200)
	ada := f.seedNamedUser("Ada", "ada@ctf.test")
	f.seedDatedSolve(ch1, ada, 100, freeze.Add(-30*time.Minute))
	f.seedDatedSolve(ch2, ada, 200, freeze.Add(30*time.Minute))
	admin := f.registerAdmin("Boss", "boss@ctf.test", "correct-horse-battery")

	h := f.decodeHistory(f.getAuthed(t, historyPath(ada), admin))
	if h.last() != 100 {
		t.Fatalf("admin on the public curve saw past the freeze: final score %d, want 100", h.last())
	}
}

// ?as_of pointed at the live instant during a freeze is clamped to the horizon for a non-admin, so it
// can never be used to draw the post-freeze curve.
func TestScoreHistoryAsOfClampedForNonAdmin(t *testing.T) {
	freeze := time.Now().Add(-time.Hour)
	f := newInsightAPI(t, account.ModeUsers, withFreeze(freeze))
	ch1 := f.seedChallenge("A", "web", 100)
	ch2 := f.seedChallenge("B", "pwn", 200)
	ada := f.seedNamedUser("Ada", "ada@ctf.test")
	f.seedDatedSolve(ch1, ada, 100, freeze.Add(-30*time.Minute))
	f.seedDatedSolve(ch2, ada, 200, freeze.Add(30*time.Minute))

	h := f.decodeHistory(f.get(t, historyAt(ada, time.Now().Add(time.Minute))))
	if h.last() != 100 {
		t.Fatalf("as_of=now was not clamped to the freeze: final score %d, want 100", h.last())
	}
}

// ?as_of before the freeze is a plain history query and returns the curve as it stood then.
func TestScoreHistoryAsOfBeforeFreezeIsHistory(t *testing.T) {
	freeze := time.Now().Add(-time.Hour)
	f := newInsightAPI(t, account.ModeUsers, withFreeze(freeze))
	ch1 := f.seedChallenge("A", "web", 100)
	ch2 := f.seedChallenge("B", "pwn", 200)
	ada := f.seedNamedUser("Ada", "ada@ctf.test")
	f.seedDatedSolve(ch1, ada, 100, freeze.Add(-40*time.Minute))
	f.seedDatedSolve(ch2, ada, 200, freeze.Add(-20*time.Minute))

	// as_of sits between the two pre-freeze solves.
	h := f.decodeHistory(f.get(t, historyAt(ada, freeze.Add(-30*time.Minute))))
	if h.last() != 100 {
		t.Fatalf("as_of before freeze must show only the earlier solve: final score %d, want 100", h.last())
	}
}

// A malformed as_of is rejected at the edge, never silently treated as now.
func TestScoreHistoryAsOfMalformed422(t *testing.T) {
	f := newInsightAPI(t, account.ModeUsers)
	ada := f.seedNamedUser("Ada", "ada@ctf.test")
	res, body := f.do(http.MethodGet, historyPath(ada)+"?as_of=not-a-timestamp", nil)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("malformed as_of: want 422, got %d: %s", res.StatusCode, body)
	}
}

// A hidden account's curve is empty for a non-admin — exactly as it is absent from the board — but an
// admin (widening visibility) sees it. Freeze is orthogonal and off here.
func TestScoreHistoryHiddenAccount(t *testing.T) {
	f := newInsightAPI(t, account.ModeUsers)
	ch := f.seedChallenge("A", "web", 100)
	ada := f.seedNamedUser("Ada", "ada@ctf.test")
	f.seedDatedSolve(ch, ada, 100, time.Now().Add(-time.Hour))
	if _, err := f.pool.Exec(f.t.Context(), `UPDATE users SET hidden=true WHERE id=$1`, ada); err != nil {
		t.Fatalf("hide user: %v", err)
	}

	pub := f.decodeHistory(f.get(t, historyPath(ada)))
	if len(pub.Points) != 0 {
		t.Fatalf("hidden account's curve must be empty for the public, got %+v", pub.Points)
	}
	admin := f.registerAdmin("Boss", "boss@ctf.test", "correct-horse-battery")
	seen := f.decodeHistory(f.getAuthed(t, historyPath(ada), admin))
	if seen.last() != 100 {
		t.Fatalf("admin must see the hidden account's curve: final score %d, want 100", seen.last())
	}
}

// Teams mode keys the curve on the team, proving the account model is resolved from the instance.
func TestScoreHistoryTeamsMode(t *testing.T) {
	f := newInsightAPI(t, account.ModeTeams)
	ch := f.seedChallenge("A", "web", 100)
	var teamID int64
	if err := f.pool.QueryRow(f.t.Context(),
		`INSERT INTO teams (name) VALUES ('Racoons') RETURNING id`).Scan(&teamID); err != nil {
		t.Fatalf("seed team: %v", err)
	}
	var userID int64
	if err := f.pool.QueryRow(f.t.Context(),
		`INSERT INTO users (name, email, verified, team_id) VALUES ('Ada','ada@ctf.test',true,$1) RETURNING id`,
		teamID).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := f.pool.Exec(f.t.Context(),
		`INSERT INTO solves (challenge_id, user_id, team_id, value, date) VALUES ($1,$2,$3,100,$4)`,
		ch, userID, teamID, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("seed team solve: %v", err)
	}

	h := f.decodeHistory(f.get(t, historyPath(teamID)))
	if h.last() != 100 {
		t.Fatalf("teams-mode curve keyed on team wrong: final score %d, want 100", h.last())
	}
}
