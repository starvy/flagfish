//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

func TestSubmitAndUnlock(t *testing.T) {
	f := newAPI(t, account.ModeUsers)

	chID := f.seedChallenge("Sanity", "misc", 100)
	f.seedFlag(chID, "flag{correct}")
	cookie, csrf := f.register("Ada", "ada@ctf.test", "correct horse battery")

	attempt := func(flag string, mut ...func(*http.Request)) (apiResp, []byte) {
		return f.do(http.MethodPost, fmt.Sprintf("/api/v1/challenges/%d/attempt", chID),
			map[string]any{"flag": flag}, mut...)
	}

	// Wrong flag: 200, incorrect. Never takes the challenge lock.
	res, body := attempt("flag{nope}", withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("wrong attempt: status %d: %s", res.StatusCode, body)
	}
	if got := decodeAttempt(t, body); got.Status != "incorrect" {
		t.Fatalf("wrong attempt: want incorrect, got %q (%s)", got.Status, body)
	}

	// Correct flag: 200, correct, snapshotted value.
	res, body = attempt("flag{correct}", withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("correct attempt: status %d: %s", res.StatusCode, body)
	}
	if got := decodeAttempt(t, body); got.Status != "correct" || got.Value != 100 {
		t.Fatalf("correct attempt: want correct/100, got %q/%d (%s)", got.Status, got.Value, body)
	}

	// Same account, same flag again: the UNIQUE solve constraint, not a SELECT.
	res, body = attempt("flag{correct}", withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("re-attempt: status %d: %s", res.StatusCode, body)
	}
	if got := decodeAttempt(t, body); got.Status != "already_solved" {
		t.Fatalf("re-attempt: want already_solved, got %q (%s)", got.Status, body)
	}

	// Cookie write without the CSRF header is rejected before it reaches gameplay.
	res, _ = attempt("flag{correct}", withCookie(cookie))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("attempt without CSRF: want 403, got %d", res.StatusCode)
	}

	// The solve above is worth 100; a 50-cost hint is affordable.
	hintID := f.seedHint(chID, "look under the rug", 50)
	unlockPath := fmt.Sprintf("/api/v1/challenges/%d/hints/%d/unlock", chID, hintID)

	res, body = f.do(http.MethodPost, unlockPath, nil, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unlock: status %d: %s", res.StatusCode, body)
	}
	if got := decodeUnlock(t, body); got.Content != "look under the rug" || got.Score != 50 {
		t.Fatalf("unlock: want content/score 50, got %q/%d (%s)", got.Content, got.Score, body)
	}

	// Buying the same hint twice is the UNIQUE(hint,account) conflict.
	res, _ = f.do(http.MethodPost, unlockPath, nil, withCookie(cookie), withCSRF(csrf))
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("re-unlock: want 409, got %d", res.StatusCode)
	}
}

type attemptResult struct {
	Status     string `json:"status"`
	FirstBlood bool   `json:"first_blood"`
	Value      int32  `json:"value"`
}

func decodeAttempt(t *testing.T, body []byte) attemptResult {
	t.Helper()
	var v attemptResult
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode attempt: %v (%s)", err, body)
	}
	return v
}

type unlockResult struct {
	HintID  int64  `json:"hint_id"`
	Content string `json:"content"`
	Charged int32  `json:"charged"`
	Score   int64  `json:"score"`
}

func decodeUnlock(t *testing.T, body []byte) unlockResult {
	t.Helper()
	var v unlockResult
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode unlock: %v (%s)", err, body)
	}
	return v
}
