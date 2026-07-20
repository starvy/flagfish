//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

// awardView is the created/echoed manual award.
type awardView struct {
	ID        int64  `json:"id"`
	AccountID int64  `json:"account_id"`
	Value     int32  `json:"value"`
	Reason    string `json:"reason"`
}

func (f *apiFix) grantAward(cookie, csrf string, accountID int64, value int, reason string) (apiResp, []byte) {
	f.t.Helper()
	return f.do(http.MethodPost, "/api/v1/admin/awards",
		map[string]any{"account_id": accountID, "value": value, "reason": reason},
		withCookie(cookie), withCSRF(csrf))
}

func (f *apiFix) revokeAward(cookie, csrf string, id int64) (apiResp, []byte) {
	f.t.Helper()
	return f.do(http.MethodDelete, fmt.Sprintf("/api/v1/admin/awards/%d", id), nil,
		withCookie(cookie), withCSRF(csrf))
}

func (f *apiFix) listAwards(cookie, csrf string, accountID int64) []awardView {
	f.t.Helper()
	res, body := f.do(http.MethodGet, fmt.Sprintf("/api/v1/admin/awards?account_id=%d", accountID), nil,
		withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		f.t.Fatalf("list awards: status %d: %s", res.StatusCode, body)
	}
	var out struct {
		Awards []awardView `json:"awards"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		f.t.Fatalf("decode awards: %v (%s)", err, body)
	}
	return out.Awards
}

// scoreOf reads the public scoreboard and returns the account's score, or 0 if it is absent (no
// non-zero scoring event puts it on the board at all).
func (f *apiFix) scoreOf(accountID int64) int64 {
	f.t.Helper()
	res, body := f.do(http.MethodGet, "/api/v1/scoreboard", nil)
	if res.StatusCode != http.StatusOK {
		f.t.Fatalf("scoreboard: status %d: %s", res.StatusCode, body)
	}
	var got scoreboardBody
	if err := json.Unmarshal(body, &got); err != nil {
		f.t.Fatalf("decode scoreboard: %v (%s)", err, body)
	}
	for _, s := range got.Standings {
		if s.AccountID == accountID {
			return s.Score
		}
	}
	return 0
}

// seedCaptainedTeam builds a team with one member and stamps that member as captain — the state the
// enrolment flow leaves behind, and the representative a manual team award is recorded against.
func (f *apiFix) seedCaptainedTeam(teamName, memberName, memberEmail string) (teamID, memberID int64) {
	f.t.Helper()
	teamID = f.seedTeam(teamName)
	memberID = f.seedUser(memberName, memberEmail)
	f.joinTeam(memberEmail, teamID)
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE teams SET captain_id = $1 WHERE id = $2`, memberID, teamID); err != nil {
		f.t.Fatalf("set captain: %v", err)
	}
	return teamID, memberID
}

func (f *apiFix) awardExists(id int64) bool {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM awards WHERE id = $1`, id).Scan(&n); err != nil {
		f.t.Fatalf("award exists: %v", err)
	}
	return n == 1
}

// TestAdminAwardUsersMode drives the whole manual-adjustment lifecycle in users mode: a positive and
// a negative grant move the board, the audit trail is stamped with the acting admin, and a revoke is
// a real DELETE that moves the board back and leaves an untouched stamped solve alone.
func TestAdminAwardUsersMode(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, adminID := f.admin("root", "root@example.com")

	player := f.seedUser("Player", "player@ctf.test")
	// A stamped solve worth 100: the fact the revoke must never disturb.
	chal := f.seedChallenge("Warmup", "misc", 100)
	f.seedSolve(chal, player, 100)

	// A positive adjustment.
	res, body := f.grantAward(cookie, csrf, player, 500, "make-good for downtime")
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("grant: status %d: %s", res.StatusCode, body)
	}
	var plus awardView
	if err := json.Unmarshal(body, &plus); err != nil {
		t.Fatalf("decode grant: %v (%s)", err, body)
	}
	if plus.Value != 500 || plus.Reason != "make-good for downtime" || plus.ID == 0 {
		t.Fatalf("grant echoed %+v, want value=500 reason set id>0", plus)
	}
	if got := f.scoreOf(player); got != 600 {
		t.Fatalf("after +500 award on a 100 solve: score=%d, want 600", got)
	}
	if n := f.auditCount("awards", "INSERT", adminID); n != 1 {
		t.Fatalf("audit INSERT rows for the acting admin = %d, want 1", n)
	}

	// A negative adjustment (a penalty). Negative values are the whole point.
	res, body = f.grantAward(cookie, csrf, player, -250, "flag-sharing penalty")
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("grant penalty: status %d: %s", res.StatusCode, body)
	}
	var minus awardView
	if err := json.Unmarshal(body, &minus); err != nil {
		t.Fatalf("decode penalty: %v (%s)", err, body)
	}
	if got := f.scoreOf(player); got != 350 {
		t.Fatalf("after -250 penalty: score=%d, want 350", got)
	}

	if awards := f.listAwards(cookie, csrf, player); len(awards) != 2 {
		t.Fatalf("manual awards listed = %d, want 2", len(awards))
	}

	// Revoke the penalty: a real DELETE, moving the board back and leaving the audit trail.
	if res, body = f.revokeAward(cookie, csrf, minus.ID); res.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: status %d: %s", res.StatusCode, body)
	}
	if f.awardExists(minus.ID) {
		t.Fatal("a revoked award must be gone from the ledger, not flipped")
	}
	if got := f.scoreOf(player); got != 600 {
		t.Fatalf("after revoking the -250 penalty: score=%d, want 600", got)
	}
	if n := f.auditCount("awards", "DELETE", adminID); n != 1 {
		t.Fatalf("audit DELETE rows for the acting admin = %d, want 1", n)
	}

	// The stamped solve is untouched by any of this — its value is a fact, not a join.
	var solveValue int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT value FROM solves WHERE challenge_id = $1 AND user_id = $2`, chal, player).Scan(&solveValue); err != nil {
		t.Fatalf("read solve: %v", err)
	}
	if solveValue != 100 {
		t.Fatalf("stamped solve value = %d, want 100 (never rewritten by an award revoke)", solveValue)
	}

	// Revoke the remaining bonus: the account has no scoring event left and drops off the board.
	if res, _ = f.revokeAward(cookie, csrf, plus.ID); res.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke bonus: status %d", res.StatusCode)
	}
	if got := f.scoreOf(player); got != 100 {
		t.Fatalf("after revoking both awards: score=%d, want the bare 100 solve", got)
	}
}

// TestAdminAwardTeamsMode proves a manual award moves the team board in teams mode and reverses on
// revoke — the scoring account is the team, recorded against its captain.
func TestAdminAwardTeamsMode(t *testing.T) {
	f := newAdminAPI(t, account.ModeTeams)
	cookie, csrf, _ := f.admin("root", "root@example.com")

	teamID, _ := f.seedCaptainedTeam("Bit Flippers", "Ada", "ada@ctf.test")

	res, body := f.grantAward(cookie, csrf, teamID, 1000, "organizer make-good")
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("grant team: status %d: %s", res.StatusCode, body)
	}
	granted := decodeID(t, body)
	if got := f.scoreOf(teamID); got != 1000 {
		t.Fatalf("team score after +1000: %d, want 1000", got)
	}

	if res, _ = f.revokeAward(cookie, csrf, granted); res.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke team award: status %d", res.StatusCode)
	}
	if got := f.scoreOf(teamID); got != 0 {
		t.Fatalf("team score after revoke: %d, want 0 (off the board)", got)
	}
}

// TestAdminRevokeRefusesGameplayAward guards the ledger: hint charges and first-blood bonuses are
// gameplay facts and can never be revoked through the manual-award path, whatever id is passed.
func TestAdminRevokeRefusesGameplayAward(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")

	player := f.seedUser("Player", "player@ctf.test")
	chal := f.seedChallenge("Crypto", "crypto", 300)

	seedTypedAward := func(kind string, value int) int64 {
		f.t.Helper()
		var id int64
		if err := f.pool.QueryRow(context.Background(),
			`INSERT INTO awards (user_id, type, challenge_id, name, value)
			 VALUES ($1,$2,$3,$4,$5) RETURNING id`,
			player, kind, chal, kind, value).Scan(&id); err != nil {
			f.t.Fatalf("seed %s award: %v", kind, err)
		}
		return id
	}

	firstBlood := seedTypedAward("first_blood", 50)
	hintCharge := seedTypedAward("hint_unlock", -10)

	for _, id := range []int64{firstBlood, hintCharge} {
		res, body := f.revokeAward(cookie, csrf, id)
		if res.StatusCode != http.StatusConflict {
			t.Fatalf("revoke gameplay award %d: status %d, want 409: %s", id, res.StatusCode, body)
		}
		if !f.awardExists(id) {
			t.Fatalf("gameplay award %d must survive a refused revoke", id)
		}
	}

	// The manual-award list never surfaces these gameplay rows either.
	if awards := f.listAwards(cookie, csrf, player); len(awards) != 0 {
		t.Fatalf("gameplay awards leaked into the manual list: %d rows", len(awards))
	}
}

// TestAdminRevokeUnknownAward is a plain 404 — no such row.
func TestAdminRevokeUnknownAward(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")

	res, _ := f.revokeAward(cookie, csrf, 999999)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("revoke missing award: status %d, want 404", res.StatusCode)
	}
}

// TestAdminGrantAwardValidation rejects the meaningless adjustments before they reach the ledger.
func TestAdminGrantAwardValidation(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	player := f.seedUser("Player", "player@ctf.test")

	cases := []struct {
		name   string
		value  int
		reason string
		status int
	}{
		{"zero value is noise", 0, "does nothing", http.StatusUnprocessableEntity},
		{"empty reason is unexplained", 100, "", http.StatusUnprocessableEntity},
		{"whitespace reason is unexplained", 100, "   ", http.StatusUnprocessableEntity},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, body := f.grantAward(cookie, csrf, player, c.value, c.reason)
			if res.StatusCode != c.status {
				t.Fatalf("grant(%d,%q): status %d, want %d: %s", c.value, c.reason, res.StatusCode, c.status, body)
			}
		})
	}

	// An unknown account is a 404, not a silent no-op.
	res, _ := f.grantAward(cookie, csrf, 999999, 100, "nobody home")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("grant to missing account: status %d, want 404", res.StatusCode)
	}

	// None of the refusals wrote anything.
	if awards := f.listAwards(cookie, csrf, player); len(awards) != 0 {
		t.Fatalf("a refused grant still wrote an award: %d rows", len(awards))
	}
}
