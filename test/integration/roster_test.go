//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

// memberID finds a roster member by display name, failing the test if absent.
func memberID(t *testing.T, team teamView, name string) int64 {
	t.Helper()
	for _, m := range team.Members {
		if m.Name == name {
			return m.UserID
		}
	}
	t.Fatalf("member %q not in roster %+v", name, team.Members)
	return 0
}

// seedTeamSolve stamps one solve for a team, giving it a scoreboard presence that freezes the roster.
func (f *apiFix) seedTeamSolve(teamID, userID int64) {
	f.t.Helper()
	chID := f.seedChallenge(fmt.Sprintf("ch-%d", teamID), "misc", 100)
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO solves (challenge_id, user_id, team_id, value) VALUES ($1,$2,$3,100)`,
		chID, userID, teamID); err != nil {
		f.t.Fatalf("seed solve: %v", err)
	}
}

func (f *apiFix) countRows(query string, args ...any) int64 {
	f.t.Helper()
	var n int64
	if err := f.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		f.t.Fatalf("count (%s): %v", query, err)
	}
	return n
}

// enrolledTeam builds a captain + the named members through the real create/join routes, returning
// the captain's auth and the roster as the captain sees it.
func (f *apiFix) enrolledTeam(name string, members ...string) (capCookie, capCSRF string, team teamView) {
	f.t.Helper()
	capCookie, capCSRF = f.register("cap", "cap@ctf.test", "correct horse battery")
	f.createTeam(capCookie, capCSRF, name, "hunter22")
	for _, m := range members {
		c, s := f.register(m, m+"@ctf.test", "correct horse battery")
		f.joinTeamByName(c, s, name, "hunter22")
	}
	// Re-read so the returned roster carries every member the caller just enrolled.
	_, body := f.do(http.MethodGet, "/api/v1/me/team", nil, withCookie(capCookie))
	team = decodeTeam(f.t, body)
	return capCookie, capCSRF, team
}

func TestCaptainKicksMember(t *testing.T) {
	f := newTeamAPI(t)
	capCookie, capCSRF, team := f.enrolledTeam("Bit Flippers", "mem")
	mem := memberID(t, team, "mem")

	res, body := f.do(http.MethodDelete, fmt.Sprintf("/api/v1/me/team/members/%d", mem),
		nil, withCookie(capCookie), withCSRF(capCSRF))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("kick: status %d, want 200: %s", res.StatusCode, body)
	}
	after := decodeTeam(t, body)
	if len(after.Members) != 1 {
		t.Fatalf("roster after kick = %d, want 1: %+v", len(after.Members), after.Members)
	}

	if n := f.countRows(`SELECT count(*) FROM users WHERE id = $1 AND team_id IS NULL`, mem); n != 1 {
		t.Errorf("kicked member still carries a team_id")
	}
	// The removal is audited against the captain.
	if n := f.countRows(
		`SELECT count(*) FROM audit_log WHERE target_table='users' AND target_id=$1 AND action='UPDATE' AND actor_id=$2`,
		mem, memberID(t, team, "cap"),
	); n == 0 {
		t.Errorf("kick left no audit row stamped with the captain")
	}
}

func TestKickScoredMemberRefused(t *testing.T) {
	f := newTeamAPI(t)
	capCookie, capCSRF, team := f.enrolledTeam("Scorers", "mem")
	mem := memberID(t, team, "mem")
	f.seedTeamSolve(team.ID, memberID(t, team, "cap"))

	res, body := f.do(http.MethodDelete, fmt.Sprintf("/api/v1/me/team/members/%d", mem),
		nil, withCookie(capCookie), withCSRF(capCSRF))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("kick after scoring: status %d, want 403: %s", res.StatusCode, body)
	}
	if n := f.countRows(`SELECT count(*) FROM users WHERE id = $1 AND team_id = $2`, mem, team.ID); n != 1 {
		t.Errorf("a scored team's member was removed anyway")
	}
}

func TestNonCaptainKickForbidden(t *testing.T) {
	f := newTeamAPI(t)
	_, _, team := f.enrolledTeam("Mutiny", "m1", "m2")
	// m1 logs in and tries to kick m2 — the captain guard lives in the statement, not the handler.
	m1Cookie, m1CSRF := f.login("m1@ctf.test", "correct horse battery")
	m2 := memberID(t, team, "m2")

	res, body := f.do(http.MethodDelete, fmt.Sprintf("/api/v1/me/team/members/%d", m2),
		nil, withCookie(m1Cookie), withCSRF(m1CSRF))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("non-captain kick: status %d, want 403: %s", res.StatusCode, body)
	}
	if n := f.countRows(`SELECT count(*) FROM users WHERE id = $1 AND team_id = $2`, m2, team.ID); n != 1 {
		t.Errorf("a non-captain removed a member")
	}
}

func TestKickSelfAndNonMemberRefused(t *testing.T) {
	f := newTeamAPI(t)
	capCookie, capCSRF, team := f.enrolledTeam("Solo")
	capUID := memberID(t, team, "cap")

	res, body := f.do(http.MethodDelete, fmt.Sprintf("/api/v1/me/team/members/%d", capUID),
		nil, withCookie(capCookie), withCSRF(capCSRF))
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("self-kick: status %d, want 409: %s", res.StatusCode, body)
	}

	// A user who exists but is on no team is not a member.
	f.register("out", "out@ctf.test", "correct horse battery")
	var outID int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT id FROM users WHERE email='out@ctf.test'`).Scan(&outID); err != nil {
		t.Fatalf("outsider id: %v", err)
	}
	res, body = f.do(http.MethodDelete, fmt.Sprintf("/api/v1/me/team/members/%d", outID),
		nil, withCookie(capCookie), withCSRF(capCSRF))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("kick non-member: status %d, want 404: %s", res.StatusCode, body)
	}
}

func TestCaptainTransferFlipsTheGuard(t *testing.T) {
	f := newTeamAPI(t)
	capCookie, capCSRF, team := f.enrolledTeam("Handover", "mem")
	memCookie, memCSRF := f.login("mem@ctf.test", "correct horse battery")
	mem := memberID(t, team, "mem")

	res, body := f.do(http.MethodPut, "/api/v1/me/team/captain",
		map[string]any{"user_id": mem}, withCookie(capCookie), withCSRF(capCSRF))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("transfer: status %d, want 200: %s", res.StatusCode, body)
	}
	if got := decodeTeam(t, body); got.IsCaptain {
		t.Errorf("the former captain still reports is_captain after handing over")
	}

	// The new captain now passes the captain guard; the old one no longer does.
	res, body = f.do(http.MethodPatch, "/api/v1/me/team",
		map[string]any{"website": "https://newcap.example"}, withCookie(memCookie), withCSRF(memCSRF))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("new captain patch: status %d, want 200: %s", res.StatusCode, body)
	}
	res, body = f.do(http.MethodPatch, "/api/v1/me/team",
		map[string]any{"website": "https://oldcap.example"}, withCookie(capCookie), withCSRF(capCSRF))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("old captain patch: status %d, want 403: %s", res.StatusCode, body)
	}
	if n := f.countRows(`SELECT count(*) FROM teams WHERE id=$1 AND captain_id=$2`, team.ID, mem); n != 1 {
		t.Errorf("captain_id did not move to the new captain")
	}
}

func TestTransferToNonMemberRefused(t *testing.T) {
	f := newTeamAPI(t)
	capCookie, capCSRF, team := f.enrolledTeam("Closed")
	_ = team
	f.register("out", "out@ctf.test", "correct horse battery")
	var outID int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT id FROM users WHERE email='out@ctf.test'`).Scan(&outID); err != nil {
		t.Fatalf("outsider id: %v", err)
	}

	res, body := f.do(http.MethodPut, "/api/v1/me/team/captain",
		map[string]any{"user_id": outID}, withCookie(capCookie), withCSRF(capCSRF))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("transfer to non-member: status %d, want 404: %s", res.StatusCode, body)
	}
	if n := f.countRows(`SELECT count(*) FROM teams WHERE id=$1 AND captain_id=$2`, team.ID, outID); n != 0 {
		t.Errorf("captaincy escaped to a non-member")
	}
}

func TestDisbandZeroHistoryTeam(t *testing.T) {
	f := newTeamAPI(t)
	capCookie, capCSRF, team := f.enrolledTeam("Mistake", "mem")
	capUID := memberID(t, team, "cap")
	mem := memberID(t, team, "mem")

	res, body := f.do(http.MethodDelete, "/api/v1/me/team",
		nil, withCookie(capCookie), withCSRF(capCSRF))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("disband: status %d, want 200: %s", res.StatusCode, body)
	}
	if n := f.countRows(`SELECT count(*) FROM teams WHERE id=$1`, team.ID); n != 0 {
		t.Errorf("the team survived a disband")
	}
	// Both members are freed by the ON DELETE SET NULL.
	if n := f.countRows(`SELECT count(*) FROM users WHERE id IN ($1,$2) AND team_id IS NULL`, capUID, mem); n != 2 {
		t.Errorf("disband left members attached to the deleted team")
	}
	// The delete is audited against the captain.
	if n := f.countRows(
		`SELECT count(*) FROM audit_log WHERE target_table='teams' AND target_id=$1 AND action='DELETE' AND actor_id=$2`,
		team.ID, capUID,
	); n == 0 {
		t.Errorf("disband left no audit row stamped with the captain")
	}
}

func TestDisbandScoredTeamConflicts(t *testing.T) {
	f := newTeamAPI(t)
	capCookie, capCSRF, team := f.enrolledTeam("Established")
	f.seedTeamSolve(team.ID, memberID(t, team, "cap"))

	res, body := f.do(http.MethodDelete, "/api/v1/me/team",
		nil, withCookie(capCookie), withCSRF(capCSRF))
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("disband scored team: status %d, want 409: %s", res.StatusCode, body)
	}
	if n := f.countRows(`SELECT count(*) FROM teams WHERE id=$1`, team.ID); n != 1 {
		t.Errorf("a scored team was deleted — its ledger is now orphaned")
	}
}

// A wrong submission is ledger too: it carries anti-cheat evidence, so it must also block disband.
func TestDisbandBlockedByFailedSubmission(t *testing.T) {
	f := newTeamAPI(t)
	capCookie, capCSRF, team := f.enrolledTeam("Attempted")
	chID := f.seedChallenge("attempted-ch", "misc", 100)
	f.seedTeamSubmission(chID, memberID(t, team, "cap"), team.ID, "incorrect", "10.0.0.1", 0)

	res, body := f.do(http.MethodDelete, "/api/v1/me/team",
		nil, withCookie(capCookie), withCSRF(capCSRF))
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("disband with a failed submission: status %d, want 409: %s", res.StatusCode, body)
	}
}
