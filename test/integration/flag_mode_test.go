//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

// createChallenge posts a challenge (static by default) and returns its id.
func (f *apiFix) createChallenge(auth []func(*http.Request), body map[string]any) int64 {
	f.t.Helper()
	res, rb := f.do(http.MethodPost, "/api/v1/admin/challenges", body, auth...)
	if res.StatusCode != http.StatusCreated {
		f.t.Fatalf("create challenge: %d (%s)", res.StatusCode, rb)
	}
	return decodeID(f.t, rb)
}

// addFlag attaches a flag through the admin API.
func (f *apiFix) addFlag(auth []func(*http.Request), challengeID int64, typ, content string) {
	f.t.Helper()
	res, rb := f.do(http.MethodPost, "/api/v1/admin/challenges/"+itoa(challengeID)+"/flags",
		map[string]any{"type": typ, "content": content}, auth...)
	if res.StatusCode != http.StatusCreated {
		f.t.Fatalf("add flag: %d (%s)", res.StatusCode, rb)
	}
}

func (f *apiFix) setFlagMode(auth []func(*http.Request), challengeID int64, mode string) (int, []byte) {
	f.t.Helper()
	res, rb := f.do(http.MethodPut, "/api/v1/admin/challenges/"+itoa(challengeID)+"/flag-mode",
		map[string]any{"flag_mode": mode}, auth...)
	return res.StatusCode, rb
}

func flagModeOf(t *testing.T, body []byte) string {
	t.Helper()
	var v struct {
		FlagMode string `json:"flag_mode"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode flag_mode: %v (%s)", err, body)
	}
	return v.FlagMode
}

func TestFlagMode_SwitchGuards(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@ctf.test")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	t.Run("to unique refused while a regex flag exists", func(t *testing.T) {
		ch := f.createChallenge(auth, map[string]any{"name": "re", "category": "c", "value": 100})
		f.addFlag(auth, ch, "regex", `flagfish\{.*\}`)
		if code, body := f.setFlagMode(auth, ch, "unique"); code != http.StatusUnprocessableEntity {
			t.Fatalf("switch with regex: %d, want 422 (%s)", code, body)
		}
	})

	t.Run("to static refused when the challenge has no flags", func(t *testing.T) {
		ch := f.seedUniqueChallenge("nf") // unique, no flags
		if code, body := f.setFlagMode(auth, ch, "static"); code != http.StatusUnprocessableEntity {
			t.Fatalf("switch to static with no flags: %d, want 422 (%s)", code, body)
		}
	})

	t.Run("to unique refused while logic is all", func(t *testing.T) {
		ch := f.createChallenge(auth, map[string]any{"name": "la", "category": "c", "value": 100, "logic": "all"})
		f.addFlag(auth, ch, "static", "flagfish{a}")
		if code, body := f.setFlagMode(auth, ch, "unique"); code != http.StatusUnprocessableEntity {
			t.Fatalf("switch to unique with logic=all: %d, want 422 (%s)", code, body)
		}
	})

	t.Run("either direction refused while the challenge has solves", func(t *testing.T) {
		ch := f.createChallenge(auth, map[string]any{"name": "solved", "category": "c", "value": 100})
		f.addFlag(auth, ch, "static", "flagfish{win}")

		pc, pcsrf := f.register("player", "player@ctf.test", "correct-horse-battery")
		res, _ := f.do(http.MethodPost, "/api/v1/challenges/"+itoa(ch)+"/attempt",
			map[string]any{"flag": "flagfish{win}"}, withCookie(pc), withCSRF(pcsrf))
		if res.StatusCode != http.StatusOK {
			t.Fatalf("seed solve: submit status %d", res.StatusCode)
		}
		if code, body := f.setFlagMode(auth, ch, "unique"); code != http.StatusUnprocessableEntity {
			t.Fatalf("switch with solves present: %d, want 422 (%s)", code, body)
		}
	})

	t.Run("happy path switches both directions", func(t *testing.T) {
		ch := f.createChallenge(auth, map[string]any{"name": "ok", "category": "c", "value": 100})
		f.addFlag(auth, ch, "static", "flagfish{ok}")

		// static → unique is allowed even with an empty pool; the failure is loud by design later.
		code, body := f.setFlagMode(auth, ch, "unique")
		if code != http.StatusOK {
			t.Fatalf("switch to unique: %d (%s)", code, body)
		}
		if m := flagModeOf(t, body); m != "unique" {
			t.Fatalf("flag_mode after switch = %q, want unique", m)
		}
		// unique → static is allowed because the challenge still has a flag.
		code, body = f.setFlagMode(auth, ch, "static")
		if code != http.StatusOK {
			t.Fatalf("switch back to static: %d (%s)", code, body)
		}
		if m := flagModeOf(t, body); m != "static" {
			t.Fatalf("flag_mode after switch back = %q, want static", m)
		}
	})
}

// The submit path honours challenges.logic: 'all' needs one submission to satisfy every flag; 'any'
// accepts a submission that satisfies just one.
func TestFlagMode_LogicAllSubmit(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@ctf.test")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}
	pc, pcsrf := f.register("p", "p@ctf.test", "correct-horse-battery")
	pauth := []func(*http.Request){withCookie(pc), withCSRF(pcsrf)}

	submit := func(ch int64, flag string) string {
		res, body := f.do(http.MethodPost, "/api/v1/challenges/"+itoa(ch)+"/attempt",
			map[string]any{"flag": flag}, pauth...)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("submit %q: status %d (%s)", flag, res.StatusCode, body)
		}
		var v struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatalf("decode status: %v (%s)", err, body)
		}
		return v.Status
	}

	// logic='all': the static flag matches only {both}; the regex matches {both} and {nope}. Only a
	// submission satisfying BOTH is correct.
	all := f.createChallenge(auth, map[string]any{"name": "all", "category": "c", "value": 100, "logic": "all"})
	f.addFlag(auth, all, "static", "flagfish{both}")
	f.addFlag(auth, all, "regex", `flagfish\{(both|nope)\}`)

	if s := submit(all, "flagfish{nope}"); s != "incorrect" {
		t.Fatalf("logic=all, partial match {nope}: status %q, want incorrect — AND fold not applied", s)
	}
	if s := submit(all, "flagfish{both}"); s != "correct" {
		t.Fatalf("logic=all, full match {both}: status %q, want correct", s)
	}

	// The same flags under logic='any' accept the partial match, proving the branch is what decided.
	any := f.createChallenge(auth, map[string]any{"name": "any", "category": "c", "value": 100, "logic": "any"})
	f.addFlag(auth, any, "static", "flagfish{both}")
	f.addFlag(auth, any, "regex", `flagfish\{(both|nope)\}`)
	if s := submit(any, "flagfish{nope}"); s != "correct" {
		t.Fatalf("logic=any, partial match {nope}: status %q, want correct", s)
	}
}
