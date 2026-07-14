//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/starvy/flagfish/internal/domain/account"
)

// --- local helpers --------------------------------------------------------

func (f *apiFix) seedBracket(name, appliesTo string) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO brackets (name, applies_to) VALUES ($1,$2) RETURNING id`,
		name, appliesTo).Scan(&id); err != nil {
		f.t.Fatalf("seed bracket %s: %v", name, err)
	}
	return id
}

func (f *apiFix) seedUserInBracket(name, email string, bracketID int64) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO users (name, email, verified, bracket_id) VALUES ($1,$2,true,$3) RETURNING id`,
		name, email, bracketID).Scan(&id); err != nil {
		f.t.Fatalf("seed user %s: %v", name, err)
	}
	return id
}

func (f *apiFix) userBracket(userID int64) *int64 {
	f.t.Helper()
	var b *int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT bracket_id FROM users WHERE id = $1`, userID).Scan(&b); err != nil {
		f.t.Fatalf("read bracket for user %d: %v", userID, err)
	}
	return b
}

type bracketList struct {
	Brackets []struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		AppliesTo string `json:"applies_to"`
	} `json:"brackets"`
}

func (bl bracketList) has(name string) bool {
	for _, b := range bl.Brackets {
		if b.Name == name {
			return true
		}
	}
	return false
}

// --- tests ----------------------------------------------------------------

// A bracket narrows the same ranking: the overall board is untouched, and each bracket board carries
// only its own members, ranked positionally among themselves.
func TestScoreboardBracketFilter(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	students := f.createBracket(t, auth, "Students", "users")
	pros := f.createBracket(t, auth, "Pros", "users")

	ch := f.seedChallenge("Warmup", "misc", 100)
	alice := f.seedNamedUser("Alice", "alice@ctf.test")
	bob := f.seedNamedUser("Bob", "bob@ctf.test")
	carol := f.seedNamedUser("Carol", "carol@ctf.test")
	f.seedSolve(ch, alice, 300)
	f.seedSolve(ch, bob, 100)
	f.seedSolve(ch, carol, 500)

	// Assignment through the real admin endpoint, so this also exercises assign + its audit rows.
	f.assignBracket(t, auth, alice, &students, http.StatusOK)
	f.assignBracket(t, auth, bob, &students, http.StatusOK)
	f.assignBracket(t, auth, carol, &pros, http.StatusOK)

	// Overall board: every account, unchanged by the assignments.
	overall := f.decodeScoreboard(f.get(t, "/api/v1/scoreboard"))
	if len(overall.Standings) != 3 {
		t.Fatalf("overall board should list all 3 accounts: %+v", overall.Standings)
	}
	if overall.Standings[0].AccountID != carol || overall.Standings[1].AccountID != alice || overall.Standings[2].AccountID != bob {
		t.Fatalf("overall order should be Carol(500), Alice(300), Bob(100): %+v", overall.Standings)
	}

	// Students board: only Alice and Bob, ranked 1 and 2 among themselves (Carol absent).
	stu := f.decodeScoreboard(f.get(t, "/api/v1/scoreboard?bracket="+itoa(students)))
	if len(stu.Standings) != 2 {
		t.Fatalf("students board should have exactly 2 rows: %+v", stu.Standings)
	}
	if stu.Standings[0].AccountID != alice || stu.Standings[0].Rank != 1 || stu.Standings[0].Score != 300 {
		t.Fatalf("students rank 1 should be Alice/300: %+v", stu.Standings[0])
	}
	if stu.Standings[1].AccountID != bob || stu.Standings[1].Rank != 2 {
		t.Fatalf("students rank 2 should be Bob: %+v", stu.Standings[1])
	}

	// Pros board: only Carol.
	pro := f.decodeScoreboard(f.get(t, "/api/v1/scoreboard?bracket="+itoa(pros)))
	if len(pro.Standings) != 1 || pro.Standings[0].AccountID != carol {
		t.Fatalf("pros board should be Carol only: %+v", pro.Standings)
	}

	// Creating the brackets and assigning accounts both left audit rows against the admin.
	if got := f.auditCount("brackets", "INSERT", adminID); got == 0 {
		t.Error("no INSERT audit row for the created brackets")
	}
	if got := f.auditCount("users", "UPDATE", adminID); got == 0 {
		t.Error("no UPDATE audit row for the bracket assignment")
	}
}

// A garbage bracket id is rejected at the edge; a well-formed id that matches nothing is an empty
// board, not an error.
func TestScoreboardBracketValidation(t *testing.T) {
	f := newAPI(t, account.ModeUsers)

	res, body := f.do(http.MethodGet, "/api/v1/scoreboard?bracket=not-a-number", nil)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("garbage bracket must be 422, got %d: %s", res.StatusCode, body)
	}
	res, body = f.do(http.MethodGet, "/api/v1/scoreboard?bracket=0", nil)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bracket=0 must be 422, got %d: %s", res.StatusCode, body)
	}

	// A positive id that matches no bracket is a valid, empty board.
	res, body = f.do(http.MethodGet, "/api/v1/scoreboard?bracket=999999", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unknown bracket must be 200, got %d: %s", res.StatusCode, body)
	}
	if got := f.decodeScoreboard(body); len(got.Standings) != 0 {
		t.Fatalf("unknown bracket must yield an empty board: %+v", got.Standings)
	}
}

// The freeze horizon still clamps a bracket board: a post-freeze solve stays hidden for a non-admin,
// and the bracket filter narrows what remains. The two properties compose, neither bypasses the other.
func TestScoreboardBracketComposesWithFreeze(t *testing.T) {
	freeze := time.Now().Add(-time.Hour)
	f := newAPI(t, account.ModeUsers, withFreeze(freeze))

	a := f.seedBracket("A", "users")
	b := f.seedBracket("B", "users")
	ch := f.seedChallenge("Reversing", "rev", 100)

	earlyA := f.seedUserInBracket("EarlyA", "earlya@ctf.test", a)
	lateA := f.seedUserInBracket("LateA", "latea@ctf.test", a)
	earlyB := f.seedUserInBracket("EarlyB", "earlyb@ctf.test", b)
	f.seedDatedSolve(ch, earlyA, 100, freeze.Add(-2*time.Hour))
	f.seedDatedSolve(ch, lateA, 500, time.Now()) // after the freeze
	f.seedDatedSolve(ch, earlyB, 200, freeze.Add(-30*time.Minute))

	// Bracket A, as a non-admin: EarlyA stays, the post-freeze LateA is clamped out, and EarlyB is
	// filtered out as a different bracket.
	boardA := f.decodeStandings(f.get(t, "/api/v1/scoreboard?bracket="+itoa(a)))
	if !boardA.has("EarlyA") {
		t.Fatalf("pre-freeze member of A must appear: %+v", boardA.Standings)
	}
	if boardA.has("LateA") {
		t.Fatalf("freeze clamp bypassed under a bracket filter: post-freeze solver leaked: %+v", boardA.Standings)
	}
	if boardA.has("EarlyB") {
		t.Fatalf("bracket B member leaked into bracket A board: %+v", boardA.Standings)
	}

	// The overall frozen board keeps the same freeze behaviour: both pre-freeze solvers, no LateA.
	overall := f.decodeStandings(f.get(t, "/api/v1/scoreboard"))
	if !overall.has("EarlyA") || !overall.has("EarlyB") {
		t.Fatalf("overall frozen board must show both pre-freeze solvers: %+v", overall.Standings)
	}
	if overall.has("LateA") {
		t.Fatalf("overall frozen board must clamp the post-freeze solver: %+v", overall.Standings)
	}
}

func TestAdminBracketRoutesRejectNonAdmin(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf := f.register("mallory", "mallory@example.com", "correct-horse-battery")

	cases := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/v1/admin/brackets", map[string]any{"name": "x", "applies_to": "users"}},
		{http.MethodGet, "/api/v1/admin/brackets", nil},
		{http.MethodPut, "/api/v1/admin/accounts/1/bracket", map[string]any{"bracket_id": 1}},
	}
	for _, c := range cases {
		res, body := f.do(c.method, c.path, c.body, withCookie(cookie), withCSRF(csrf))
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s as user: got %d, want 403 (%s)", c.method, c.path, res.StatusCode, body)
		}
		resA, _ := f.do(c.method, c.path, c.body)
		if resA.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s anonymous: got %d, want 403", c.method, c.path, resA.StatusCode)
		}
	}
}

// The full admin lifecycle: create, list (public list is mode-scoped), update, assign with its
// guards, clear, and delete (which unassigns members rather than blocking).
func TestAdminBracketManagement(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	usersBr := f.createBracket(t, auth, "Division", "users")
	teamsBr := f.createBracket(t, auth, "TeamDivision", "teams")

	// The admin list shows both kinds; the public list is scoped to the instance's account kind.
	var adminBrackets bracketList
	resList, adminBody := f.do(http.MethodGet, "/api/v1/admin/brackets", nil, auth...)
	if resList.StatusCode != http.StatusOK {
		t.Fatalf("admin list brackets: got %d (%s)", resList.StatusCode, adminBody)
	}
	if err := json.Unmarshal(adminBody, &adminBrackets); err != nil {
		t.Fatalf("decode admin brackets: %v (%s)", err, adminBody)
	}
	if !adminBrackets.has("Division") || !adminBrackets.has("TeamDivision") {
		t.Fatalf("admin list should show both brackets: %+v", adminBrackets.Brackets)
	}
	var pub bracketList
	pubBody := f.get(t, "/api/v1/brackets")
	if err := json.Unmarshal(pubBody, &pub); err != nil {
		t.Fatalf("decode public brackets: %v (%s)", err, pubBody)
	}
	if !pub.has("Division") || pub.has("TeamDivision") {
		t.Fatalf("public list (users mode) should show only the users bracket: %+v", pub.Brackets)
	}

	// Partial update.
	res, body := f.do(http.MethodPatch, "/api/v1/admin/brackets/"+itoa(usersBr),
		map[string]any{"name": "Renamed"}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("update bracket: got %d, want 200 (%s)", res.StatusCode, body)
	}

	member := f.seedUserInBracket("Member", "member@ctf.test", usersBr)

	// A bracket of the wrong account kind is refused, not silently applied.
	res, body = f.do(http.MethodPut, "/api/v1/admin/accounts/"+itoa(member)+"/bracket",
		map[string]any{"bracket_id": teamsBr}, auth...)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("cross-kind assign should be 422, got %d (%s)", res.StatusCode, body)
	}
	// An unknown bracket is a 404.
	res, body = f.do(http.MethodPut, "/api/v1/admin/accounts/"+itoa(member)+"/bracket",
		map[string]any{"bracket_id": 999999}, auth...)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("assign to unknown bracket should be 404, got %d (%s)", res.StatusCode, body)
	}
	// Clearing the assignment is a null bracket_id.
	res, body = f.do(http.MethodPut, "/api/v1/admin/accounts/"+itoa(member)+"/bracket",
		map[string]any{"bracket_id": nil}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("clear assignment: got %d, want 200 (%s)", res.StatusCode, body)
	}
	if got := f.userBracket(member); got != nil {
		t.Fatalf("clear should null the bracket, got %v", *got)
	}

	// Re-assign, then delete the bracket: the member is unassigned, not blocked.
	f.assignBracket(t, auth, member, &usersBr, http.StatusOK)
	res, body = f.do(http.MethodDelete, "/api/v1/admin/brackets/"+itoa(usersBr), nil, auth...)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete bracket: got %d, want 204 (%s)", res.StatusCode, body)
	}
	if got := f.userBracket(member); got != nil {
		t.Fatalf("deleting a bracket must unassign its members, got %v", *got)
	}
	// Deleting a bracket that is gone is a 404.
	res, _ = f.do(http.MethodDelete, "/api/v1/admin/brackets/"+itoa(usersBr), nil, auth...)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("delete of missing bracket should be 404, got %d", res.StatusCode)
	}
}

// --- request helpers over the admin endpoints -----------------------------

func (f *apiFix) createBracket(t *testing.T, auth []func(*http.Request), name, appliesTo string) int64 {
	t.Helper()
	res, body := f.do(http.MethodPost, "/api/v1/admin/brackets",
		map[string]any{"name": name, "applies_to": appliesTo}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create bracket %s: got %d, want 201 (%s)", name, res.StatusCode, body)
	}
	return decodeID(t, body)
}

func (f *apiFix) assignBracket(t *testing.T, auth []func(*http.Request), accountID int64, bracketID *int64, want int) {
	t.Helper()
	res, body := f.do(http.MethodPut, "/api/v1/admin/accounts/"+itoa(accountID)+"/bracket",
		map[string]any{"bracket_id": bracketID}, auth...)
	if res.StatusCode != want {
		t.Fatalf("assign account %d: got %d, want %d (%s)", accountID, res.StatusCode, want, body)
	}
}

func (f *apiFix) decodeScoreboard(body []byte) scoreboardBody {
	f.t.Helper()
	var s scoreboardBody
	if err := json.Unmarshal(body, &s); err != nil {
		f.t.Fatalf("decode scoreboard: %v (%s)", err, body)
	}
	return s
}
