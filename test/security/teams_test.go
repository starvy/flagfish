//go:build integration

package security

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Team fixtures. Raw SQL on purpose: rows normally arrive through registration or the importer,
// and what is under test here is the admin write surface and the walls on top of them.

func (f *fixture) team(name string) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO teams (name) VALUES ($1) RETURNING id`, name).Scan(&id); err != nil {
		f.t.Fatalf("seed team: %v", err)
	}
	return id
}

func (f *fixture) assign(userID, teamID int64) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE users SET team_id = $1 WHERE id = $2`, teamID, userID); err != nil {
		f.t.Fatalf("assign user %d to team %d: %v", userID, teamID, err)
	}
}

// score gives a team a scoreboard presence: one challenge, one stamped solve.
func (f *fixture) score(teamID, userID int64, value int) {
	f.t.Helper()
	ctx := context.Background()
	var chID int64
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO challenges (name, category, value) VALUES ($1,'misc',$2) RETURNING id`,
		fmt.Sprintf("ch-for-%d", teamID), value).Scan(&chID); err != nil {
		f.t.Fatalf("seed challenge: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO solves (challenge_id, user_id, team_id, value) VALUES ($1,$2,$3,$4)`,
		chID, userID, teamID, value); err != nil {
		f.t.Fatalf("seed solve: %v", err)
	}
}

// putJSON is a cookie-authenticated JSON PUT with the CSRF token attached.
func (f *fixture) putJSON(path, csrf, sid, body string) resp {
	f.t.Helper()
	return f.do(http.MethodPut, path,
		withCookie(sid), withCSRF(csrf), withBody("application/json", []byte(body)))
}

// S17 — a team ban walls every member, on both credentials, and takes their sessions with it.
//
// The wall reads Principal.TeamBanned, which LoadPrincipal computes for cookie and token alike;
// the session kill runs in the ban's own transaction. Either half missing is a real hole: without
// the wall a banned team plays on, without the kill a live cookie rides until its next check.
func TestS17_TeamBanWallsEveryMemberCredential(t *testing.T) {
	f := setup(t, withTeamsMode())
	ctx := context.Background()

	f.user("overlord", pw, asAdmin)
	adminSess, err := f.acct.Login(ctx, "overlord@ctf.test", pw)
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}

	teamID := f.team("doomed")
	type member struct {
		id         int64
		sid, token string
	}
	names := []string{"m1", "m2"}
	members := make([]member, 0, len(names))
	for _, name := range names {
		uid := f.user(name, pw)
		f.assign(uid, teamID)
		sess, err := f.acct.Login(ctx, name+"@ctf.test", pw)
		if err != nil {
			t.Fatalf("login %s: %v", name, err)
		}
		tok, err := f.acct.CreateToken(ctx, uid, nil, time.Hour)
		if err != nil {
			t.Fatalf("token %s: %v", name, err)
		}
		members = append(members, member{id: uid, sid: sess.ID, token: tok.Plaintext})
	}

	// Both credentials work before the ban, or the test proves nothing.
	for i, m := range members {
		if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(m.sid)).StatusCode; got != http.StatusOK {
			t.Fatalf("member %d cookie before ban: %d, want 200", i, got)
		}
		if got := f.do(http.MethodGet, "/api/v1/probe", withToken(m.token)).StatusCode; got != http.StatusOK {
			t.Fatalf("member %d token before ban: %d, want 200", i, got)
		}
	}

	r := f.putJSON(fmt.Sprintf("/api/v1/admin/teams/%d/ban", teamID), adminSess.CSRFToken, adminSess.ID,
		`{"banned":true}`)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("ban: %d (%s)", r.StatusCode, r.Body)
	}

	for i, m := range members {
		cookie := f.do(http.MethodGet, "/api/v1/probe", withCookie(m.sid)).StatusCode
		token := f.do(http.MethodGet, "/api/v1/probe", withToken(m.token)).StatusCode
		// The sessions are dead, so the cookie is a 401; the token still authenticates and must
		// hit the wall as a 403. Neither may be a 200, and the token case is the invariant: a
		// banned team keeping API access through member tokens is the bypass this wall closes.
		if cookie == http.StatusOK {
			t.Errorf("member %d cookie survived the team ban", i)
		}
		if token != http.StatusForbidden {
			t.Errorf("member %d token after team ban: %d, want 403", i, token)
		}
		if n := f.count(`SELECT count(*) FROM sessions WHERE user_id = $1`, m.id); n != 0 {
			t.Errorf("member %d still holds %d live sessions after the team ban", i, n)
		}
	}
}

// S18 — an admin cannot ban their own team. The refusal is what guarantees a team ban can never
// leave the instance without a usable admin, exactly like the self-ban refusal on users.
func TestS18_SelfTeamBanIsRefused(t *testing.T) {
	f := setup(t, withTeamsMode())
	ctx := context.Background()

	uid := f.user("captain-admin", pw, asAdmin)
	teamID := f.team("home")
	f.assign(uid, teamID)

	sess, err := f.acct.Login(ctx, "captain-admin@ctf.test", pw)
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	r := f.putJSON(fmt.Sprintf("/api/v1/admin/teams/%d/ban", teamID), sess.CSRFToken, sess.ID,
		`{"banned":true}`)
	if r.StatusCode != http.StatusConflict {
		t.Errorf("self-team-ban: %d, want 409 (%s)", r.StatusCode, r.Body)
	}
	if n := f.count(`SELECT count(*) FROM teams WHERE id = $1 AND banned`, teamID); n != 0 {
		t.Error("the admin's own team was banned — the instance may now have no usable admin")
	}
	// And the admin still works.
	if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(sess.ID)).StatusCode; got != http.StatusOK {
		t.Errorf("admin walled after a refused self-team-ban: %d", got)
	}
}

// S19 — hidden and banned teams do not exist publicly, and stay visible to an admin.
//
// The public team page 404s (not 403: existence is the leak) and the scoreboard drops them;
// the admin viewer sees them on the same scoreboard route, because moderating a team must not
// require unbanning it first.
func TestS19_HiddenAndBannedTeamsAreInvisiblePublicly(t *testing.T) {
	f := setup(t, withTeamsMode())
	ctx := context.Background()

	f.user("watcher", pw, asAdmin)
	adminSess, err := f.acct.Login(ctx, "watcher@ctf.test", pw)
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}

	teams := map[string]int64{"honest": f.team("honest"), "ghost": f.team("ghost"), "outlaw": f.team("outlaw")}
	for i, name := range []string{"honest", "ghost", "outlaw"} {
		uid := f.user(fmt.Sprintf("p%d", i), pw)
		f.assign(uid, teams[name])
		f.score(teams[name], uid, 100)
	}

	// Flip the flags through the real admin routes — the setters under test.
	r := f.putJSON(fmt.Sprintf("/api/v1/admin/teams/%d/hidden", teams["ghost"]), adminSess.CSRFToken, adminSess.ID, `{"hidden":true}`)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("hide: %d (%s)", r.StatusCode, r.Body)
	}
	r = f.putJSON(fmt.Sprintf("/api/v1/admin/teams/%d/ban", teams["outlaw"]), adminSess.CSRFToken, adminSess.ID, `{"banned":true}`)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("ban: %d (%s)", r.StatusCode, r.Body)
	}

	// Public team pages: the hidden and the banned team 404 identically; the honest one is there.
	if got := f.do(http.MethodGet, fmt.Sprintf("/api/v1/teams/%d", teams["honest"])).StatusCode; got != http.StatusOK {
		t.Errorf("honest team page: %d, want 200", got)
	}
	for _, name := range []string{"ghost", "outlaw"} {
		if got := f.do(http.MethodGet, fmt.Sprintf("/api/v1/teams/%d", teams[name])).StatusCode; got != http.StatusNotFound {
			t.Errorf("%s team page: %d, want 404 — a masked team must not exist publicly", name, got)
		}
	}

	names := func(body string) map[string]bool {
		var out struct {
			Standings []struct {
				Name string `json:"name"`
			} `json:"standings"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatalf("decode scoreboard: %v (%s)", err, body)
		}
		set := map[string]bool{}
		for _, s := range out.Standings {
			set[s.Name] = true
		}
		return set
	}

	pub := f.do(http.MethodGet, "/api/v1/scoreboard")
	if pub.StatusCode != http.StatusOK {
		t.Fatalf("public scoreboard: %d", pub.StatusCode)
	}
	got := names(pub.Body)
	if !got["honest"] || got["ghost"] || got["outlaw"] {
		t.Errorf("public scoreboard shows %v, want honest only", got)
	}

	adm := f.do(http.MethodGet, "/api/v1/scoreboard", withCookie(adminSess.ID))
	if adm.StatusCode != http.StatusOK {
		t.Fatalf("admin scoreboard: %d", adm.StatusCode)
	}
	got = names(adm.Body)
	for _, name := range []string{"honest", "ghost", "outlaw"} {
		if !got[name] {
			t.Errorf("admin scoreboard is missing %q — moderation needs to see what the public cannot", name)
		}
	}

	// The 404s must not be distinguishable from a team that never existed.
	missing := f.do(http.MethodGet, "/api/v1/teams/424242")
	masked := f.do(http.MethodGet, fmt.Sprintf("/api/v1/teams/%d", teams["outlaw"]))
	if missing.StatusCode != masked.StatusCode || !sameProblemType(missing.Body, masked.Body) {
		t.Errorf("a masked team answers differently from a missing one:\n masked:  %d %s\n missing: %d %s",
			masked.StatusCode, masked.Body, missing.StatusCode, missing.Body)
	}
}

// S20 — the profile PATCH cannot be turned into a moderation tool.
//
// banned, hidden and captain_id are absent from both the body schema (strict: unknown keys are
// refused) and the UPDATE's SET list. A request that smuggles them in must change nothing: mass
// assignment through a partial-update endpoint is the classic CTFd-shaped hole.
func TestS20_TeamPatchMassAssignmentIsRefused(t *testing.T) {
	f := setup(t, withTeamsMode())
	ctx := context.Background()

	f.user("editor", pw, asAdmin)
	sess, err := f.acct.Login(ctx, "editor@ctf.test", pw)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	teamID := f.team("target")

	// One smuggled key per request: a combined payload would let one refused key mask another
	// that a future refactor quietly started accepting.
	for _, payload := range []string{
		`{"banned":true}`,
		`{"hidden":true}`,
		`{"captain_id":42}`,
		`{"password_hash":"x"}`,
		`{"name":"renamed","banned":true}`,
	} {
		r := f.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/teams/%d", teamID),
			withCookie(sess.ID), withCSRF(sess.CSRFToken),
			withBody("application/json", []byte(payload)))
		if r.StatusCode == http.StatusOK {
			t.Errorf("PATCH %s was accepted (%s)", payload, r.Body)
		}

		var row struct {
			Name      string
			Banned    bool
			Hidden    bool
			CaptainID *int64
			PwHash    *string
		}
		if err := f.pool.QueryRow(ctx,
			`SELECT name, banned, hidden, captain_id, password_hash FROM teams WHERE id = $1`, teamID).
			Scan(&row.Name, &row.Banned, &row.Hidden, &row.CaptainID, &row.PwHash); err != nil {
			t.Fatalf("read team: %v", err)
		}
		if row.Banned || row.Hidden || row.CaptainID != nil || row.PwHash != nil {
			t.Errorf("PATCH %s landed: %+v", payload, row)
		}
		if row.Name != "target" {
			t.Errorf("PATCH %s was refused but still renamed the team to %q — a partial write", payload, row.Name)
		}
	}
}

// S21 — the roster controls are captain-only, enforced in the statement, not the handler.
//
// A non-captain member who reaches DELETE /me/team/members/{id}, PUT /me/team/captain, or
// DELETE /me/team must change nothing: the guard is the UPDATE/DELETE's WHERE clause, so deleting
// the handler-side story still leaves the database refusing. Each attempt is a 4xx and the roster,
// captaincy, and the team's existence are all intact afterwards. Remove the guard and this fails.
func TestS21_RosterControlsAreCaptainOnly(t *testing.T) {
	f := setup(t, withTeamsMode())
	ctx := context.Background()

	captainID := f.user("skipper", pw)
	memberID := f.user("deckhand", pw)
	teamID := f.team("vessel")
	f.assign(captainID, teamID)
	f.assign(memberID, teamID)
	if _, err := f.pool.Exec(ctx, `UPDATE teams SET captain_id = $1 WHERE id = $2`, captainID, teamID); err != nil {
		t.Fatalf("seat captain: %v", err)
	}

	sess, err := f.acct.Login(ctx, "deckhand@ctf.test", pw)
	if err != nil {
		t.Fatalf("login member: %v", err)
	}

	// Kick the captain: the non-captain member has no authority to remove anyone.
	kick := f.do(http.MethodDelete, fmt.Sprintf("/api/v1/me/team/members/%d", captainID),
		withCookie(sess.ID), withCSRF(sess.CSRFToken))
	if kick.StatusCode != http.StatusForbidden {
		t.Errorf("non-captain kick: %d, want 403 (%s)", kick.StatusCode, kick.Body)
	}

	// Seize captaincy for themselves.
	grab := f.do(http.MethodPut, "/api/v1/me/team/captain",
		withCookie(sess.ID), withCSRF(sess.CSRFToken),
		withBody("application/json", []byte(fmt.Sprintf(`{"user_id":%d}`, memberID))))
	if grab.StatusCode != http.StatusForbidden {
		t.Errorf("non-captain transfer: %d, want 403 (%s)", grab.StatusCode, grab.Body)
	}

	// Disband the team out from under the captain.
	disband := f.do(http.MethodDelete, "/api/v1/me/team",
		withCookie(sess.ID), withCSRF(sess.CSRFToken))
	if disband.StatusCode != http.StatusForbidden {
		t.Errorf("non-captain disband: %d, want 403 (%s)", disband.StatusCode, disband.Body)
	}

	// Nothing moved: both still on the team, the captain unchanged, the team alive.
	if n := f.count(`SELECT count(*) FROM users WHERE team_id = $1`, teamID); n != 2 {
		t.Errorf("roster changed under a non-captain: %d members remain, want 2", n)
	}
	if n := f.count(`SELECT count(*) FROM teams WHERE id = $1 AND captain_id = $2`, teamID, captainID); n != 1 {
		t.Errorf("captaincy moved under a non-captain")
	}
}

func sameProblemType(a, b string) bool {
	typeOf := func(s string) string {
		var v struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			return ""
		}
		return v.Type
	}
	return strings.EqualFold(typeOf(a), typeOf(b))
}
