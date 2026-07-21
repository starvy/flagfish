//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

// The admin roster surface: an organizer repairing someone else's team mid-event. The property
// every one of these tests circles is that the ledger is stamped — a roster edit changes who scores
// next, never who scored already.

func rosterPath(teamID int64) string {
	return "/api/v1/admin/teams/" + itoa(teamID) + "/members"
}

func rosterMemberPath(teamID, userID int64) string {
	return rosterPath(teamID) + "/" + itoa(userID)
}

func rosterMovePath(teamID, userID int64) string {
	return rosterMemberPath(teamID, userID) + "/move"
}

type adminRosterBody struct {
	Members []struct {
		UserID  int64  `json:"user_id"`
		Name    string `json:"name"`
		Email   string `json:"email"`
		Captain bool   `json:"captain"`
	} `json:"members"`
}

// roster reads a team's roster through the admin route under test.
func (f *apiFix) roster(teamID int64, auth ...func(*http.Request)) adminRosterBody {
	f.t.Helper()
	res, body := f.do(http.MethodGet, rosterPath(teamID), nil, auth...)
	if res.StatusCode != http.StatusOK {
		f.t.Fatalf("roster %d: status %d: %s", teamID, res.StatusCode, body)
	}
	var out adminRosterBody
	if err := json.Unmarshal(body, &out); err != nil {
		f.t.Fatalf("decode roster: %v (%s)", err, body)
	}
	return out
}

// seedRosteredTeam builds a team whose first member holds the captain's seat, which is the state
// the real enrolment flow leaves behind.
func (f *apiFix) seedRosteredTeam(teamName string, emails ...string) (teamID int64, userIDs []int64) {
	f.t.Helper()
	teamID = f.seedTeam(teamName)
	for _, email := range emails {
		id := f.seedUser(email, email)
		f.joinTeam(email, teamID)
		userIDs = append(userIDs, id)
	}
	if len(userIDs) > 0 {
		if _, err := f.pool.Exec(context.Background(),
			`UPDATE teams SET captain_id = $1 WHERE id = $2`, userIDs[0], teamID); err != nil {
			f.t.Fatalf("seed captain: %v", err)
		}
	}
	return teamID, userIDs
}

// captainOf reads a team's seat; -1 means the seat is empty.
func (f *apiFix) captainOf(teamID int64) int64 {
	f.t.Helper()
	var id *int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT captain_id FROM teams WHERE id = $1`, teamID).Scan(&id); err != nil {
		f.t.Fatalf("captain of %d: %v", teamID, err)
	}
	if id == nil {
		return -1
	}
	return *id
}

// teamIDOf reads a user's current membership; -1 means teamless.
func (f *apiFix) teamIDOf(userID int64) int64 {
	f.t.Helper()
	var id *int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT team_id FROM users WHERE id = $1`, userID).Scan(&id); err != nil {
		f.t.Fatalf("team of %d: %v", userID, err)
	}
	if id == nil {
		return -1
	}
	return *id
}

func TestAdminListsTeamRoster(t *testing.T) {
	f := newAdminAPI(t, account.ModeTeams)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	alpha, members := f.seedRosteredTeam("alpha", "a@ctf.test", "b@ctf.test")

	got := f.roster(alpha, auth...)
	if len(got.Members) != 2 {
		t.Fatalf("roster = %+v, want 2 members", got.Members)
	}
	if got.Members[0].UserID != members[0] || !got.Members[0].Captain {
		t.Errorf("first member = %+v, want user %d holding the seat", got.Members[0], members[0])
	}
	if got.Members[1].Captain {
		t.Errorf("second member %+v must not be captain", got.Members[1])
	}
	// email is admin-only data and is exactly why this view exists separately from the public one.
	if got.Members[1].Email != "b@ctf.test" {
		t.Errorf("member email = %q, want b@ctf.test", got.Members[1].Email)
	}

	// A team that does not exist is a 404, never an empty roster.
	res, _ := f.do(http.MethodGet, rosterPath(424242), nil, auth...)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("roster of a missing team: got %d, want 404", res.StatusCode)
	}
}

func TestAdminRemovesTeamMember(t *testing.T) {
	f := newAdminAPI(t, account.ModeTeams)
	cookie, csrf, actorID := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	alpha, members := f.seedRosteredTeam("alpha", "a@ctf.test", "b@ctf.test")
	before := f.auditCount("users", "UPDATE", actorID)

	res, body := f.do(http.MethodDelete, rosterMemberPath(alpha, members[1]), nil, auth...)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("remove: status %d: %s", res.StatusCode, body)
	}

	if got := f.teamIDOf(members[1]); got != -1 {
		t.Errorf("removed member team_id = %d, want teamless", got)
	}
	if got := f.captainOf(alpha); got != members[0] {
		t.Errorf("captain = %d, want %d (untouched)", got, members[0])
	}
	if got := f.roster(alpha, auth...); len(got.Members) != 1 {
		t.Errorf("roster after remove = %+v, want 1 member", got.Members)
	}
	// The write and its audit row commit together, attributed to the admin who made the call.
	if after := f.auditCount("users", "UPDATE", actorID); after <= before {
		t.Errorf("audit rows for the actor = %d, want more than %d", after, before)
	}
}

// A captain who leaves must not keep the seat: the team is left with a captain who is a member, or
// with no captain at all once it is empty.
func TestAdminRemovingCaptainReassignsSeat(t *testing.T) {
	f := newAdminAPI(t, account.ModeTeams)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	alpha, members := f.seedRosteredTeam("alpha", "a@ctf.test", "b@ctf.test")

	res, body := f.do(http.MethodDelete, rosterMemberPath(alpha, members[0]), nil, auth...)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("remove captain: status %d: %s", res.StatusCode, body)
	}
	if got := f.captainOf(alpha); got != members[1] {
		t.Errorf("captain after removing the captain = %d, want the remaining member %d", got, members[1])
	}

	// Removing the last member empties the seat rather than leaving a stale one behind.
	res, body = f.do(http.MethodDelete, rosterMemberPath(alpha, members[1]), nil, auth...)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("remove last member: status %d: %s", res.StatusCode, body)
	}
	if got := f.captainOf(alpha); got != -1 {
		t.Errorf("captain of an empty team = %d, want none", got)
	}
}

func TestAdminMovesTeamMember(t *testing.T) {
	f := newAdminAPI(t, account.ModeTeams)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	alpha, members := f.seedRosteredTeam("alpha", "a@ctf.test", "b@ctf.test")
	beta, betaMembers := f.seedRosteredTeam("beta", "c@ctf.test")

	res, body := f.do(http.MethodPost, rosterMovePath(alpha, members[1]),
		map[string]any{"to_team_id": beta}, auth...)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("move: status %d: %s", res.StatusCode, body)
	}

	if got := f.teamIDOf(members[1]); got != beta {
		t.Errorf("moved member team_id = %d, want %d", got, beta)
	}
	if got := len(f.roster(alpha, auth...).Members); got != 1 {
		t.Errorf("alpha roster = %d members, want 1", got)
	}
	if got := len(f.roster(beta, auth...).Members); got != 2 {
		t.Errorf("beta roster = %d members, want 2", got)
	}
	// An arrival never unseats a sitting captain.
	if got := f.captainOf(beta); got != betaMembers[0] {
		t.Errorf("beta captain = %d, want %d", got, betaMembers[0])
	}
}

// Moving a captain has to settle two seats: the one they vacate and the one they may adopt.
func TestAdminMovingCaptainSettlesBothSeats(t *testing.T) {
	f := newAdminAPI(t, account.ModeTeams)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	alpha, members := f.seedRosteredTeam("alpha", "a@ctf.test", "b@ctf.test")
	empty := f.seedTeam("empty") // captainless, nobody on it

	res, body := f.do(http.MethodPost, rosterMovePath(alpha, members[0]),
		map[string]any{"to_team_id": empty}, auth...)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("move captain: status %d: %s", res.StatusCode, body)
	}

	if got := f.captainOf(alpha); got != members[1] {
		t.Errorf("alpha captain = %d, want the remaining member %d", got, members[1])
	}
	// A lone arrival adopts the empty seat, exactly as the self-serve join does.
	if got := f.captainOf(empty); got != members[0] {
		t.Errorf("empty-team captain = %d, want the arrival %d", got, members[0])
	}

	// Whatever happened, no team is left pointing at a captain who is not on it.
	if n := f.countRows(`SELECT count(*) FROM teams t
	                      WHERE t.captain_id IS NOT NULL
	                        AND NOT EXISTS (SELECT 1 FROM users u
	                                         WHERE u.id = t.captain_id AND u.team_id = t.id)`); n != 0 {
		t.Errorf("%d teams hold a captain who is not a member", n)
	}
}

// The load-bearing one. A move re-points the player, not the ledger: solves, awards and submissions
// stamped the team_id they were written with, so the old team's score is byte-identical afterwards.
func TestAdminMoveLeavesLedgerAndScoreAlone(t *testing.T) {
	f := newAdminAPI(t, account.ModeTeams)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	alpha, members := f.seedRosteredTeam("alpha", "a@ctf.test", "b@ctf.test")
	beta, _ := f.seedRosteredTeam("beta", "c@ctf.test")

	// Both alpha members score, so the mover carries points off with them if anything is rewritten.
	f.seedTeamSolve(alpha, members[0])
	f.seedTeamSolve(alpha, members[1])
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO awards (user_id, team_id, type, name, value) VALUES ($1,$2,'standard','bonus',25)`,
		members[1], alpha); err != nil {
		t.Fatalf("seed award: %v", err)
	}

	alphaBefore := f.scoreOf(alpha)
	betaBefore := f.scoreOf(beta)
	solvesBefore := f.countRows(`SELECT count(*) FROM solves WHERE team_id = $1`, alpha)
	moverSolves := f.countRows(`SELECT count(*) FROM solves WHERE team_id = $1 AND user_id = $2`, alpha, members[1])
	if alphaBefore != 225 || solvesBefore != 2 || moverSolves != 1 {
		t.Fatalf("setup: alpha score=%d solves=%d mover solves=%d, want 225/2/1", alphaBefore, solvesBefore, moverSolves)
	}

	res, body := f.do(http.MethodPost, rosterMovePath(alpha, members[1]),
		map[string]any{"to_team_id": beta}, auth...)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("move: status %d: %s", res.StatusCode, body)
	}

	if got := f.scoreOf(alpha); got != alphaBefore {
		t.Errorf("alpha score = %d after the move, want %d — history must not be rewritten", got, alphaBefore)
	}
	if got := f.scoreOf(beta); got != betaBefore {
		t.Errorf("beta score = %d after the move, want %d — points do not travel with a player", got, betaBefore)
	}
	if got := f.countRows(`SELECT count(*) FROM solves WHERE team_id = $1`, alpha); got != solvesBefore {
		t.Errorf("alpha solve rows = %d, want %d", got, solvesBefore)
	}
	if got := f.countRows(`SELECT count(*) FROM solves WHERE team_id = $1`, beta); got != 0 {
		t.Errorf("beta inherited %d solve rows, want 0", got)
	}
	// The mover's own solve still names both the player and the team that fielded them.
	if got := f.countRows(`SELECT count(*) FROM solves WHERE team_id = $1 AND user_id = $2`, alpha, members[1]); got != 1 {
		t.Errorf("the mover's solve is no longer stamped to alpha (%d rows)", got)
	}
	if got := f.countRows(`SELECT count(*) FROM awards WHERE team_id = $1`, alpha); got != 1 {
		t.Errorf("alpha award rows = %d, want 1", got)
	}
}

// The destination cap is decided by the caps trigger under the registration advisory lock, on the
// same statement that writes the membership — there is no size check in Go to race past.
func TestAdminMoveIntoFullTeamRefused(t *testing.T) {
	f := newAdminAPI(t, account.ModeTeams, [2]string{"team_size", "2"})
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	alpha, members := f.seedRosteredTeam("alpha", "a@ctf.test", "b@ctf.test")
	full, _ := f.seedRosteredTeam("full", "c@ctf.test", "d@ctf.test")

	res, body := f.do(http.MethodPost, rosterMovePath(alpha, members[1]),
		map[string]any{"to_team_id": full}, auth...)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("move into a full team: status %d, want 409: %s", res.StatusCode, body)
	}

	// The refusal rolls back the leave leg too: the player is still where they started.
	if got := f.teamIDOf(members[1]); got != alpha {
		t.Errorf("member team_id = %d after a refused move, want %d", got, alpha)
	}
	if got := f.countRows(`SELECT count(*) FROM users WHERE team_id = $1`, full); got != 2 {
		t.Errorf("full team now holds %d members, want 2", got)
	}
}

func TestAdminRosterRefusedInUsersMode(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	// A users-mode instance refuses to boot with teams in the table, so there is nothing to name
	// but an id: the refusal is about the account model, not about which team was asked for.
	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"list", http.MethodGet, rosterPath(1), nil},
		{"remove", http.MethodDelete, rosterMemberPath(1, 1), nil},
		{"move", http.MethodPost, rosterMovePath(1, 1), map[string]any{"to_team_id": 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, body := f.do(tc.method, tc.path, tc.body, auth...)
			if res.StatusCode != http.StatusConflict {
				t.Errorf("%s in users mode: status %d, want 409: %s", tc.name, res.StatusCode, body)
			}
		})
	}
}

// Every impossible edit gets its own refusal. None of them may be a silent success.
func TestAdminRosterRefusals(t *testing.T) {
	f := newAdminAPI(t, account.ModeTeams)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	alpha, members := f.seedRosteredTeam("alpha", "a@ctf.test")
	beta, betaMembers := f.seedRosteredTeam("beta", "c@ctf.test")

	cases := []struct {
		name   string
		method string
		path   string
		body   any
		want   int
	}{
		{"remove from a team that does not exist", http.MethodDelete, rosterMemberPath(424242, members[0]), nil, http.StatusNotFound},
		{"remove a user that does not exist", http.MethodDelete, rosterMemberPath(alpha, 424242), nil, http.StatusNotFound},
		{"remove someone else's member", http.MethodDelete, rosterMemberPath(alpha, betaMembers[0]), nil, http.StatusConflict},
		{"move from a team that does not exist", http.MethodPost, rosterMovePath(424242, members[0]), map[string]any{"to_team_id": beta}, http.StatusNotFound},
		{"move a user that does not exist", http.MethodPost, rosterMovePath(alpha, 424242), map[string]any{"to_team_id": beta}, http.StatusNotFound},
		{"move a non-member", http.MethodPost, rosterMovePath(alpha, betaMembers[0]), map[string]any{"to_team_id": beta}, http.StatusConflict},
		{"move to a team that does not exist", http.MethodPost, rosterMovePath(alpha, members[0]), map[string]any{"to_team_id": 424242}, http.StatusNotFound},
		{"move to the team they are already on", http.MethodPost, rosterMovePath(alpha, members[0]), map[string]any{"to_team_id": alpha}, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, body := f.do(tc.method, tc.path, tc.body, auth...)
			if res.StatusCode != tc.want {
				t.Errorf("status %d, want %d: %s", res.StatusCode, tc.want, body)
			}
		})
	}

	// Nothing above moved anyone.
	if got := f.teamIDOf(members[0]); got != alpha {
		t.Errorf("alpha member team_id = %d, want %d", got, alpha)
	}
	if got := f.teamIDOf(betaMembers[0]); got != beta {
		t.Errorf("beta member team_id = %d, want %d", got, beta)
	}
}

// Two organizers acting on the same player at once: one move lands, the other is refused because
// the player is no longer on the team it named. The row is never on two teams and never lost.
func TestAdminConcurrentMovesOfSameMember(t *testing.T) {
	f := newAdminAPI(t, account.ModeTeams, [2]string{"team_size", "2"})
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	alpha, members := f.seedRosteredTeam("alpha", "a@ctf.test", "b@ctf.test")
	beta, _ := f.seedRosteredTeam("beta", "c@ctf.test")
	gamma, _ := f.seedRosteredTeam("gamma", "d@ctf.test")

	const racers = 8
	targets := [2]int64{beta, gamma}
	mover := members[1]
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		codes = map[int]int{}
	)
	wg.Add(racers)
	for i := range racers {
		go func(i int) {
			defer wg.Done()
			res, _ := f.do(http.MethodPost, rosterMovePath(alpha, mover),
				map[string]any{"to_team_id": targets[i%len(targets)]}, auth...)
			mu.Lock()
			codes[res.StatusCode]++
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	if codes[http.StatusNoContent] != 1 {
		t.Errorf("%d moves succeeded, want exactly 1 (codes=%v)", codes[http.StatusNoContent], codes)
	}
	if codes[http.StatusNoContent]+codes[http.StatusConflict] != racers {
		t.Errorf("unexpected statuses: %v", codes)
	}

	// One membership, on one of the two destinations, and neither destination is over the cap.
	landed := f.teamIDOf(mover)
	if landed != beta && landed != gamma {
		t.Errorf("mover landed on team %d, want %d or %d", landed, beta, gamma)
	}
	for _, id := range []int64{alpha, beta, gamma} {
		if n := f.countRows(`SELECT count(*) FROM users WHERE team_id = $1`, id); n > 2 {
			t.Errorf("team %d holds %d members, over the team_size cap of 2", id, n)
		}
	}
	if n := f.countRows(`SELECT count(*) FROM users WHERE id = $1 AND team_id IS NOT NULL`, mover); n != 1 {
		t.Errorf("mover has %d memberships, want 1", n)
	}
}
