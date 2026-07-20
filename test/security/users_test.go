//go:build integration

package security

import (
	"context"
	"fmt"
	"net/http"
	"testing"
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
