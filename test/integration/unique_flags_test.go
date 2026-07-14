//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/starvy/flagfish/internal/domain/account"
)

// seedUniqueChallenge inserts a visible flag_mode='unique' challenge and returns its id.
func (f *apiFix) seedUniqueChallenge(name string) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO challenges (name, category, value, flag_mode)
		 VALUES ($1,'pwn',500,'unique') RETURNING id`, name).Scan(&id); err != nil {
		f.t.Fatalf("seed unique challenge: %v", err)
	}
	return id
}

// seedInstances fills a challenge's pool with n bundles and returns their plaintext flags. Only the
// hash is stored, exactly as an author-uploaded pool would be.
func (f *apiFix) seedInstances(challengeID int64, n int) []string {
	f.t.Helper()
	out := make([]string, n)
	for i := range n {
		flag := fmt.Sprintf("flag{unique-%d-%d}", challengeID, i)
		vars := fmt.Sprintf(`{"host":"h%d.ctf.test","port":%d}`, i, 31000+i)
		if _, err := f.pool.Exec(context.Background(),
			`INSERT INTO challenge_instances (challenge_id, value_hash, vars)
			 VALUES ($1, sha256($2::bytea), $3::jsonb)`, challengeID, []byte(flag), vars); err != nil {
			f.t.Fatalf("seed instance %d: %v", i, err)
		}
		out[i] = flag
	}
	return out
}

func (f *apiFix) issueCount(challengeID int64) int64 {
	f.t.Helper()
	var n int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM flag_issues WHERE challenge_id = $1`, challengeID).Scan(&n); err != nil {
		f.t.Fatalf("count flag_issues: %v", err)
	}
	return n
}

// challengeDetailBody decodes only what these tests assert on.
type challengeDetailBody struct {
	ID       int64  `json:"id"`
	FlagMode string `json:"flag_mode"`
	Instance *struct {
		InstanceID int64          `json:"instance_id"`
		ArtifactID *int64         `json:"artifact_id"`
		Vars       map[string]any `json:"vars"`
	} `json:"instance"`
}

func (f *apiFix) detail(challengeID int64, mut ...func(*http.Request)) (apiResp, challengeDetailBody) {
	f.t.Helper()
	res, body := f.do(http.MethodGet, fmt.Sprintf("/api/v1/challenges/%d", challengeID), nil, mut...)
	var d challengeDetailBody
	if res.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &d); err != nil {
			f.t.Fatalf("decode detail: %v (%s)", err, body)
		}
	}
	return res, d
}

// The headline anti-cheat feature, reachable from the API at last: the first view of a unique-flag
// challenge issues the account an instance, and every view after it returns that same instance
// without touching the pool. Five browser tabs are one flag, not five.
func TestUniqueFlag_IssuedOnFirstView_AndOnlyOnce(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	cookie, _ := f.register("Ada", "ada@ctf.test", "correct-horse-battery")
	ch := f.seedUniqueChallenge("heap")
	f.seedInstances(ch, 3)

	res, first := f.detail(ch, withCookie(cookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("first view: status %d", res.StatusCode)
	}
	if first.FlagMode != "unique" {
		t.Errorf("flag_mode = %q, want unique", first.FlagMode)
	}
	if first.Instance == nil {
		t.Fatal("first view returned no instance — the challenge is unplayable")
	}
	if first.Instance.Vars["host"] == nil {
		t.Errorf("instance vars are empty: %+v — the description has nothing to render against", first.Instance.Vars)
	}
	if n := f.issueCount(ch); n != 1 {
		t.Fatalf("flag_issues rows = %d, want 1", n)
	}

	res, second := f.detail(ch, withCookie(cookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("second view: status %d", res.StatusCode)
	}
	if second.Instance == nil || second.Instance.InstanceID != first.Instance.InstanceID {
		t.Errorf("second view gave instance %+v, want the first one (%d)", second.Instance, first.Instance.InstanceID)
	}
	if n := f.issueCount(ch); n != 1 {
		t.Errorf("flag_issues rows = %d after a second view, want 1 — the read wrote twice", n)
	}
	// The pool must be intact: two of three instances still free.
	var free int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM challenge_instances ci
		  WHERE ci.challenge_id = $1
		    AND NOT EXISTS (SELECT 1 FROM flag_issues fi WHERE fi.instance_id = ci.id)`, ch).Scan(&free); err != nil {
		t.Fatalf("count free: %v", err)
	}
	if free != 2 {
		t.Errorf("free instances = %d, want 2", free)
	}
}

// A static challenge is a pure read. No instance on the wire, and — the part that matters for the
// hot-ish read path — not one row written.
func TestUniqueFlag_StaticChallengeIsUnaffected(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	cookie, _ := f.register("Ada", "ada@ctf.test", "correct-horse-battery")
	ch := f.seedChallenge("sanity", "misc", 100)
	f.seedFlag(ch, "flag{static}")

	res, d := f.detail(ch, withCookie(cookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("detail: status %d", res.StatusCode)
	}
	if d.FlagMode != "static" {
		t.Errorf("flag_mode = %q, want static", d.FlagMode)
	}
	if d.Instance != nil {
		t.Errorf("static challenge returned an instance: %+v", d.Instance)
	}
	if n := f.issueCount(ch); n != 0 {
		t.Errorf("flag_issues rows = %d, want 0 — a static view must write nothing", n)
	}
}

// An admin previewing a challenge must not burn an instance a player needs: the pool is sized for
// the field, not for the organisers.
func TestUniqueFlag_AdminPreviewDoesNotConsumeAnInstance(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	ch := f.seedUniqueChallenge("rev")
	f.seedInstances(ch, 1) // the last instance in the world

	adminCookie, _, _ := f.admin("root", "root@ctf.test")
	res, d := f.detail(ch, withCookie(adminCookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("admin preview: status %d", res.StatusCode)
	}
	if d.Instance != nil {
		t.Errorf("admin preview was issued instance %+v", d.Instance)
	}
	if n := f.issueCount(ch); n != 0 {
		t.Fatalf("flag_issues rows = %d after an admin preview, want 0 — the admin burned the pool", n)
	}

	// The player still gets the one instance the admin did not take.
	playerCookie, _ := f.register("Ada", "ada@ctf.test", "correct-horse-battery")
	res, player := f.detail(ch, withCookie(playerCookie))
	if res.StatusCode != http.StatusOK || player.Instance == nil {
		t.Fatalf("player view after admin preview: status %d, instance %+v", res.StatusCode, player.Instance)
	}
}

// An anonymous viewer has no account to issue to, so there is nothing to take from the pool. With
// public challenge visibility the detail is readable; it just carries no instance.
func TestUniqueFlag_AnonymousViewerDoesNotConsumeAnInstance(t *testing.T) {
	f := newAPI(t, account.ModeUsers, [2]string{"challenge_visibility", "public"})
	ch := f.seedUniqueChallenge("web")
	f.seedInstances(ch, 1)

	res, d := f.detail(ch)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("anonymous detail: status %d", res.StatusCode)
	}
	if d.Instance != nil {
		t.Errorf("anonymous viewer was issued instance %+v", d.Instance)
	}
	if n := f.issueCount(ch); n != 0 {
		t.Errorf("flag_issues rows = %d, want 0 — an anonymous view consumed the pool", n)
	}
}

// An exhausted pool is a hard failure. The one thing it must never be is a 200 with no flag in it:
// a silent fallback to a shared flag destroys uniqueness for exactly the late registrants the
// detector exists to catch.
func TestUniqueFlag_PoolExhaustionIsLoud(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	ch := f.seedUniqueChallenge("crypto")
	f.seedInstances(ch, 1)

	early, _ := f.register("Ada", "ada@ctf.test", "correct-horse-battery")
	res, d := f.detail(ch, withCookie(early))
	if res.StatusCode != http.StatusOK || d.Instance == nil {
		t.Fatalf("early registrant: status %d, instance %+v", res.StatusCode, d.Instance)
	}

	late, _ := f.register("Grace", "grace@ctf.test", "correct-horse-battery")
	res, body := f.do(http.MethodGet, fmt.Sprintf("/api/v1/challenges/%d", ch), nil, withCookie(late))
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("exhausted pool: status %d, want 503 — a challenge with no flag must not be served as OK (%s)",
			res.StatusCode, body)
	}
	var served challengeDetailBody
	if json.Unmarshal(body, &served) == nil && served.ID != 0 {
		t.Errorf("exhausted pool returned a challenge body: %s", body)
	}
	if n := f.issueCount(ch); n != 1 {
		t.Errorf("flag_issues rows = %d, want 1 — the failed view still wrote", n)
	}
}

// The provable detector, now reachable: a solve on a unique-flag challenge by an account that was
// never issued an instance. Issuance is lazy, so anyone who so much as opened the challenge has a
// flag_issues row — no row plus a solve means the flag came from somewhere else.
func TestAnticheatUnissuedSolves(t *testing.T) {
	f := newAnticheatAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@ctf.test")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	unique := f.seedUniqueChallenge("heap")
	f.seedInstances(unique, 2)
	static := f.seedChallenge("sanity", "misc", 100)

	// Ada plays honestly: she opened the challenge, so she holds an instance of her own.
	adaCookie, _ := f.register("Ada", "ada@ctf.test", "correct-horse-battery")
	ada := f.userID("ada@ctf.test")
	if res, d := f.detail(unique, withCookie(adaCookie)); res.StatusCode != http.StatusOK || d.Instance == nil {
		t.Fatalf("ada's first view: status %d, instance %+v", res.StatusCode, d.Instance)
	}
	f.seedDatedSolve(unique, ada, 500, time.Now())

	// Mallory solved a challenge she never opened. There is no honest way to hold that flag.
	f.register("Mallory", "mallory@ctf.test", "correct-horse-battery")
	mallory := f.userID("mallory@ctf.test")
	f.seedDatedSolve(unique, mallory, 500, time.Now())
	// A static challenge has no pool, so an unissued solve on it means nothing and must not appear.
	f.seedDatedSolve(static, mallory, 100, time.Now())

	res, body := f.do(http.MethodGet, "/api/v1/admin/anticheat/unissued-solves", nil, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unissued-solves: status %d (%s)", res.StatusCode, body)
	}
	var got struct {
		Solves []struct {
			SolveID       int64  `json:"solve_id"`
			ChallengeID   int64  `json:"challenge_id"`
			ChallengeName string `json:"challenge_name"`
			AccountID     int64  `json:"account_id"`
			UserID        int64  `json:"user_id"`
			Value         int32  `json:"value"`
		} `json:"solves"`
		Total int64 `json:"total"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if len(got.Solves) != 1 || got.Total != 1 {
		t.Fatalf("want exactly one unissued solve, got %d (total %d): %s", len(got.Solves), got.Total, body)
	}
	s := got.Solves[0]
	if s.AccountID != mallory || s.UserID != mallory {
		t.Errorf("flagged account %d/%d, want mallory (%d)", s.AccountID, s.UserID, mallory)
	}
	if s.AccountID == ada {
		t.Error("ada was flagged — she was issued an instance before she solved")
	}
	if s.ChallengeID != unique || s.ChallengeName != "heap" {
		t.Errorf("challenge = %d/%q, want %d/heap", s.ChallengeID, s.ChallengeName, unique)
	}
	if s.Value != 500 {
		t.Errorf("value = %d, want 500", s.Value)
	}
}
