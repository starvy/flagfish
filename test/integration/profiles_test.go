//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

// setModeration flips a user's hidden/banned flags directly — the state an admin action would leave.
func (f *apiFix) setModeration(userID int64, hidden, banned bool) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE users SET hidden = $2, banned = $3 WHERE id = $1`, userID, hidden, banned); err != nil {
		f.t.Fatalf("set moderation on %d: %v", userID, err)
	}
}

// seedHiddenChallengeSolve gives a user a solve on a NON-visible challenge, so its points land on the
// score but its name must never appear in the public solve history.
func (f *apiFix) seedHiddenChallengeSolve(userID int64, value int) {
	f.t.Helper()
	ctx := context.Background()
	var chID int64
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO challenges (name, category, value, state) VALUES ('SECRET-CHAL','hidden-cat',$1,'hidden') RETURNING id`,
		value).Scan(&chID); err != nil {
		f.t.Fatalf("seed hidden challenge: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO solves (challenge_id, user_id, value) VALUES ($1,$2,$3)`, chID, userID, value); err != nil {
		f.t.Fatalf("seed hidden solve: %v", err)
	}
}

func TestPublicUserProfile(t *testing.T) {
	f := newAPI(t, account.ModeUsers)

	cookie, csrf := f.register("Alice", "alice@ctf.test", "correct-horse-battery")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}
	aliceID := f.userID("alice@ctf.test")

	// Alice fills in her public profile and solves a visible challenge.
	if res, body := f.do(http.MethodPatch, "/api/v1/me", map[string]any{
		"website": "https://alice.example", "affiliation": "Acme U", "country": "CZ",
	}, auth...); res.StatusCode != http.StatusOK {
		t.Fatalf("profile patch: %d (%s)", res.StatusCode, body)
	}
	chID := f.seedChallenge("Sanity", "misc", 100)
	f.seedFlag(chID, "flag{ok}")
	if res, body := f.do(http.MethodPost, "/api/v1/challenges/"+itoa(chID)+"/attempt",
		map[string]any{"flag": "flag{ok}"}, auth...); res.StatusCode != http.StatusOK {
		t.Fatalf("solve: %d (%s)", res.StatusCode, body)
	}
	// A hidden challenge she also solved: its points count, its name must not leak.
	f.seedHiddenChallengeSolve(aliceID, 250)

	// Anonymous public read: the visibility-gated fields are present, contact and moderation are not.
	res, body := f.do(http.MethodGet, "/api/v1/users/"+itoa(aliceID), nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("public profile: %d (%s)", res.StatusCode, body)
	}
	var pub map[string]any
	if err := json.Unmarshal(body, &pub); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pub["name"] != "Alice" {
		t.Errorf("name = %v, want Alice", pub["name"])
	}
	if pub["website"] != "https://alice.example" || pub["affiliation"] != "Acme U" || pub["country"] != "CZ" {
		t.Errorf("public fields missing: %+v", pub)
	}
	if score, ok := pub["score"].(float64); !ok || int(score) != 350 {
		t.Errorf("score = %v, want 350 (100 visible + 250 hidden)", pub["score"])
	}
	for _, leaked := range []string{"email", "pending_email", "hidden", "banned", "role", "password_hash"} {
		if _, ok := pub[leaked]; ok {
			t.Errorf("public profile leaks %q", leaked)
		}
	}

	// The solve history names only the visible challenge — never the hidden one.
	solves, ok := pub["solves"].([]any)
	if !ok {
		t.Fatalf("solves is %T, want []any", pub["solves"])
	}
	if len(solves) != 1 {
		t.Fatalf("solves = %d, want exactly 1 (the visible challenge)", len(solves))
	}
	first, ok := solves[0].(map[string]any)
	if !ok {
		t.Fatalf("solves[0] is %T, want map[string]any", solves[0])
	}
	if first["challenge_name"] != "Sanity" {
		t.Errorf("solve name = %v, want Sanity", first["challenge_name"])
	}
	for _, s := range solves {
		m, ok := s.(map[string]any)
		if !ok {
			t.Fatalf("solve is %T, want map[string]any", s)
		}
		if m["challenge_name"] == "SECRET-CHAL" {
			t.Fatal("public solve history leaked a hidden challenge")
		}
	}
}

// TestPublicUserProfileHiddenBanned proves the gate: a hidden or banned account is 404 to the public
// and 200 to an admin. Removing the `u.hidden = false AND u.banned = false` predicate — or dropping
// the admin widening — flips one of these assertions, so the security property is under test.
func TestPublicUserProfileHiddenBanned(t *testing.T) {
	f := newAPI(t, account.ModeUsers)

	adminCookie, _, _ := f.admin("root", "root@ctf.test")

	f.register("Ghost", "ghost@ctf.test", "correct-horse-battery")
	ghostID := f.userID("ghost@ctf.test")
	f.register("Cheat", "cheat@ctf.test", "correct-horse-battery")
	cheatID := f.userID("cheat@ctf.test")

	f.setModeration(ghostID, true, false) // hidden
	f.setModeration(cheatID, false, true) // banned

	for _, id := range []int64{ghostID, cheatID} {
		// Public: gone.
		if res, _ := f.do(http.MethodGet, "/api/v1/users/"+itoa(id), nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("public read of masked user %d: %d, want 404", id, res.StatusCode)
		}
		// Admin: visible.
		if res, body := f.do(http.MethodGet, "/api/v1/users/"+itoa(id), nil, withCookie(adminCookie)); res.StatusCode != http.StatusOK {
			t.Errorf("admin read of masked user %d: %d, want 200 (%s)", id, res.StatusCode, body)
		}
	}

	// A user who does not exist is 404 to everyone, admin included — the same answer as hidden, so a
	// hidden account's existence is never an oracle.
	if res, _ := f.do(http.MethodGet, "/api/v1/users/999999", nil, withCookie(adminCookie)); res.StatusCode != http.StatusNotFound {
		t.Errorf("admin read of missing user: %d, want 404", res.StatusCode)
	}
}
