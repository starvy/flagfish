//go:build integration

package security

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// S20b — the user profile PATCH cannot touch identity or moderation state.
//
// email, role, banned, hidden, team_id and must_change_password are absent from both the body
// schema (strict: unknown keys are refused) and the UPDATE's SET list. Each has its own route
// with its own semantics — a session kill, a last-admin check — that a profile PATCH would skip.
func TestS20b_UserPatchMassAssignmentIsRefused(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	f.user("moderator", pw, asAdmin)
	sess, err := f.acct.Login(ctx, "moderator@ctf.test", pw)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	uid := f.user("subject", pw)

	before := f.userRow(uid)

	for _, payload := range []string{
		`{"banned":true}`,
		`{"hidden":true}`,
		`{"role":"admin"}`,
		`{"email":"stolen@ctf.test"}`,
		`{"team_id":1}`,
		`{"must_change_password":true}`,
		`{"password_hash":"x"}`,
		`{"name":"renamed","banned":true}`,
	} {
		r := f.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/users/%d", uid),
			withCookie(sess.ID), withCSRF(sess.CSRFToken),
			withBody("application/json", []byte(payload)))
		if r.StatusCode == http.StatusOK {
			t.Errorf("PATCH %s was accepted (%s)", payload, r.Body)
		}
		if after := f.userRow(uid); after != before {
			t.Errorf("PATCH %s changed the row:\n before %+v\n after  %+v", payload, before, after)
		}
	}
}

// S21 — the forced-password-change wall is real, and so is its exit.
//
// A forced user is refused everywhere except the password-change endpoint itself; changing the
// password clears the flag and the new session plays normally. Without the exemption the wall
// redirects the user to change a password on an endpoint the wall itself blocks — an unrecoverable
// loop; without the clear, the change "succeeds" and the wall stays up anyway.
func TestS21_ForcedPasswordChangeWallHasAnExit(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	uid := f.user("forced", pw)
	f.forcePasswordChange(uid)

	sess, err := f.acct.Login(ctx, "forced@ctf.test", pw)
	if err != nil {
		t.Fatalf("login while forced must work (the login route is exempt): %v", err)
	}

	// The wall: an ordinary authenticated route is refused.
	if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(sess.ID)).StatusCode; got != http.StatusForbidden {
		t.Fatalf("forced user reached an ordinary route: %d, want 403", got)
	}

	// The exit: the password-change endpoint answers.
	r := f.do(http.MethodPost, "/api/v1/me/password",
		withCookie(sess.ID), withCSRF(sess.CSRFToken),
		withBody("application/json",
			[]byte(`{"current_password":"`+pw+`","new_password":"a brand new passphrase"}`)))
	if r.StatusCode != http.StatusOK {
		t.Fatalf("the forced-change exit is walled: %d (%s) — the user is trapped in a loop", r.StatusCode, r.Body)
	}

	// The change discharged the order…
	if n := f.count(`SELECT count(*) FROM users WHERE id = $1 AND must_change_password`, uid); n != 0 {
		t.Error("must_change_password survived a real password change — the gate is a one-way door again")
	}
	// …and the fresh session plays.
	fresh := r.cookie("flagfish_session")
	if fresh == nil {
		t.Fatal("the password change minted no session cookie")
	}
	if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(fresh.Value)).StatusCode; got != http.StatusOK {
		t.Errorf("the post-change session is still walled: %d, want 200", got)
	}
}

// S22 — the login rehash does NOT clear the forced-change flag.
//
// The rehash re-mints the same password: an imported bcrypt hash upgrades to Argon2id the first
// time its owner logs in. If that write went through the clearing query, every imported user with
// a pending forced change would discharge it by logging in — with the very password the order
// exists to retire.
func TestS22_LoginRehashDoesNotClearForcedChange(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	bh, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	uid := f.user("imported-forced", pw, withHash(string(bh)))
	f.forcePasswordChange(uid)

	sess, err := f.acct.Login(ctx, "imported-forced@ctf.test", pw)
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	// The rehash happened…
	var hash string
	if err := f.pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, uid).Scan(&hash); err != nil {
		t.Fatalf("read hash: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("the rehash did not run (hash %q) — this test would pass vacuously", hash)
	}

	// …and the flag survived it, so the wall still stands for the session it minted.
	if n := f.count(`SELECT count(*) FROM users WHERE id = $1 AND must_change_password`, uid); n != 1 {
		t.Error("the login rehash cleared must_change_password — logging in with the OLD password discharged the forced change")
	}
	if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(sess.ID)).StatusCode; got != http.StatusForbidden {
		t.Errorf("forced user plays on after a rehashing login: %d, want 403", got)
	}
}

func (f *fixture) forcePasswordChange(uid int64) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE users SET must_change_password = true WHERE id = $1`, uid); err != nil {
		f.t.Fatalf("force password change: %v", err)
	}
}

// userRow is comparable on purpose (team_id coalesced to -1), so "nothing changed" is one `!=`.
type userRow struct {
	Name, Email, Role                string
	Banned, Hidden, MustChangePasswd bool
	TeamID                           int64
}

func (f *fixture) userRow(id int64) userRow {
	f.t.Helper()
	var r userRow
	if err := f.pool.QueryRow(context.Background(),
		`SELECT name, email, role, banned, hidden, must_change_password, COALESCE(team_id, -1)
		   FROM users WHERE id = $1`, id).
		Scan(&r.Name, &r.Email, &r.Role, &r.Banned, &r.Hidden, &r.MustChangePasswd, &r.TeamID); err != nil {
		f.t.Fatalf("read user: %v", err)
	}
	return r
}
