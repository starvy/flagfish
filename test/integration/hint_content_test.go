//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
)

// hintContentDetail decodes the hint shape that matters for the persisted-body property: whether a
// hint is unlocked/locked for the caller, and the body itself when the caller has paid for it.
type hintContentDetail struct {
	Hints []struct {
		ID       int64   `json:"id"`
		Unlocked bool    `json:"unlocked"`
		Locked   bool    `json:"locked"`
		Content  *string `json:"content"`
	} `json:"hints"`
}

func (f *apiFix) decodeHintContent(body []byte) hintContentDetail {
	f.t.Helper()
	var d hintContentDetail
	if err := json.Unmarshal(body, &d); err != nil {
		f.t.Fatalf("decode detail: %v (%s)", err, body)
	}
	return d
}

func (d hintContentDetail) find(t *testing.T, id int64) (unlocked, locked bool, content *string) {
	t.Helper()
	for _, h := range d.Hints {
		if h.ID == id {
			return h.Unlocked, h.Locked, h.Content
		}
	}
	t.Fatalf("hint %d not present on detail", id)
	return false, false, nil
}

// TestUnlockedHintBodySurvivesReload: challenge detail carries the body of a hint THIS account has
// already unlocked, so a reload still shows what the player paid for — and it carries that body to
// no one else. A hint the account has not unlocked, and a hint gated behind an unmet prerequisite,
// return their cost and lock state but never a byte of content.
//
// Without the fix the detail omits content entirely, so the first "reload shows the body" assertion
// fails; the redaction assertions are the security floor and must hold no matter what.
func TestUnlockedHintBodySurvivesReload(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	ch := f.seedChallenge("Hinted", "misc", 100)
	f.seedFlag(ch, "flag{h}")

	const paidBody = "the vault code is 4815-1623-42"
	const gatedBody = "and the second door opens outward"
	paid := f.seedHint(ch, paidBody, 0)
	// A hint gated on `paid`: locked until `paid` is unlocked. Its body must never appear before then.
	gated := f.seedHintWithReqs(ch, gatedBody, 0, fmt.Sprintf(`{"prerequisites":[%d]}`, paid))

	unlockerCookie, unlockerCSRF := f.register("Ada", "ada@ctf.test", "correct horse battery")
	bystanderCookie := func() string { c, _ := f.register("Bob", "bob@ctf.test", "correct horse battery"); return c }()

	// Before unlocking, even the buyer sees no body.
	res, body := f.do(http.MethodGet, fmt.Sprintf("/api/v1/challenges/%d", ch), nil, withCookie(unlockerCookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("detail before unlock: got %d (%s)", res.StatusCode, body)
	}
	if unlocked, _, content := f.decodeHintContent(body).find(t, paid); unlocked || content != nil {
		t.Fatalf("unpurchased hint leaked: unlocked=%v content=%v", unlocked, content)
	}

	// Buy it.
	res, body = f.do(http.MethodPost, f.unlockPath(ch, paid), nil, withCookie(unlockerCookie), withCSRF(unlockerCSRF))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unlock: got %d (%s)", res.StatusCode, body)
	}

	// Reload: the body is there now, straight off the detail — no unlock response held in memory.
	res, body = f.do(http.MethodGet, fmt.Sprintf("/api/v1/challenges/%d", ch), nil, withCookie(unlockerCookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("detail after unlock: got %d (%s)", res.StatusCode, body)
	}
	d := f.decodeHintContent(body)

	unlocked, _, content := d.find(t, paid)
	if !unlocked {
		t.Error("paid hint is not marked unlocked after purchase")
	}
	if content == nil || *content != paidBody {
		t.Errorf("reloaded detail does not carry the paid hint body: got %v, want %q", content, paidBody)
	}

	// The gated hint stays locked for the buyer (its prerequisite chain is unlocked, but it is a
	// separate purchase) — and locked or not, an unpurchased hint carries no content.
	if _, _, gatedContent := d.find(t, gated); gatedContent != nil {
		t.Errorf("gated, unpurchased hint leaked its body to the buyer: %q", *gatedContent)
	}

	// A different account that never unlocked anything sees neither body.
	res, body = f.do(http.MethodGet, fmt.Sprintf("/api/v1/challenges/%d", ch), nil, withCookie(bystanderCookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("bystander detail: got %d (%s)", res.StatusCode, body)
	}
	bd := f.decodeHintContent(body)
	if _, _, c := bd.find(t, paid); c != nil {
		t.Errorf("bystander was handed the paid hint body: %q", *c)
	}
	if _, locked, c := bd.find(t, gated); c != nil {
		t.Errorf("bystander was handed the gated hint body: %q", *c)
	} else if !locked {
		t.Error("gated hint should be locked for an account with no unlocks")
	}
}
