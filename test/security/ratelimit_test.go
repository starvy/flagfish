//go:build integration

package security

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// Every teamless player must have their own rate-limit budget.
//
// In teams mode a player with no team has no account, so keying the limiter on the account id puts
// all of them in one bucket. That bucket is at its emptiest exactly when the event opens: the whole
// field is teamless at once and hammering create-team and join-team, so they lock each other out of
// the one action that would give them an account — no attacker involved, though one player can also
// spend the shared budget on purpose and wall off everyone else.
func TestS30_TeamlessPlayersEachGetTheirOwnRateLimitBudget(t *testing.T) {
	const (
		limit   = 5
		players = 4
	)
	wholeWindow(t, 30*time.Second)
	f := setup(t, withTeamsMode(), withLimit(limit))
	ctx := context.Background()

	sids := make([]string, players)
	for i := range players {
		name := fmt.Sprintf("teamless%d", i)
		f.user(name, pw)
		sess, err := f.acct.Login(ctx, name+"@ctf.test", pw)
		if err != nil {
			t.Fatalf("login %s: %v", name, err)
		}
		sids[i] = sess.ID
	}

	// One player spends far past their budget — the kickoff scramble, or a single griefer.
	for range limit * 3 {
		f.do(http.MethodGet, "/api/v1/probe", withCookie(sids[0]))
	}
	if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(sids[0])).StatusCode; got != http.StatusTooManyRequests {
		t.Fatalf("the flooding player got %d after %d requests, want 429 — a teamless player is "+
			"not being limited at all, which is not the fix", got, limit*3)
	}

	// Everyone else still has the whole of their own, and is still limited at the end of it.
	for p := 1; p < players; p++ {
		for i := range limit {
			if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(sids[p])).StatusCode; got == http.StatusTooManyRequests {
				t.Fatalf("teamless player %d was rate-limited on request %d by traffic that was not "+
					"theirs. Every teamless player is keyed on the same absent account, so the whole "+
					"field shares ONE budget for create-team and join-team at kickoff", p, i)
			}
		}
		if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(sids[p])).StatusCode; got != http.StatusTooManyRequests {
			t.Errorf("teamless player %d: %d after %d requests, want 429 — giving them their own "+
				"bucket must not give them an unmetered one", p, got, limit)
		}
	}
}

// A user id and an account id that are the same integer are not the same bucket.
//
// They come from separate sequences and in teams mode an account id is a team id, so user 1 and
// team 1 both exist on any instance where anybody has made a team. Keying a teamless player on a
// bare id would hand them somebody else's budget — and hand somebody else theirs.
func TestS31_RateLimitNamespacesDoNotCollide(t *testing.T) {
	const limit = 5
	wholeWindow(t, 30*time.Second)
	f := setup(t, withTeamsMode(), withLimit(limit))
	ctx := context.Background()

	// Ids are reset per fixture, so these two collide on the number: the teamless player is user 1
	// and the team member's account is team 1.
	loner := f.user("loner", pw)
	teamID := f.team("squad")
	member := f.user("member", pw)
	f.assign(member, teamID)
	if loner != teamID {
		t.Fatalf("fixture ids drifted: user %d vs team %d — the collision this test exists for "+
			"is not set up", loner, teamID)
	}

	lonerSess, err := f.acct.Login(ctx, "loner@ctf.test", pw)
	if err != nil {
		t.Fatalf("login loner: %v", err)
	}
	memberSess, err := f.acct.Login(ctx, "member@ctf.test", pw)
	if err != nil {
		t.Fatalf("login member: %v", err)
	}

	// The teamless player empties their bucket.
	for range limit * 2 {
		f.do(http.MethodGet, "/api/v1/probe", withCookie(lonerSess.ID))
	}
	if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(lonerSess.ID)).StatusCode; got != http.StatusTooManyRequests {
		t.Fatalf("the teamless player got %d, want 429 — they are not being limited at all", got)
	}

	// The team whose id is the same integer is untouched.
	for i := range limit {
		if got := f.do(http.MethodGet, "/api/v1/probe", withCookie(memberSess.ID)).StatusCode; got == http.StatusTooManyRequests {
			t.Fatalf("the member of team %d was limited on request %d by a teamless user whose user "+
				"id happens to be %d. The two ids share a bucket, so one player can spend a whole "+
				"team's budget", teamID, i, loner)
		}
	}
}
