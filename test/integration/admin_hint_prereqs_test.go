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

type hintBody struct {
	ID            int64   `json:"id"`
	Prerequisites []int64 `json:"prerequisites"`
}

func decodeHint(t *testing.T, body []byte) hintBody {
	t.Helper()
	var v hintBody
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode hint: %v (%s)", err, body)
	}
	return v
}

// TestAdminHintPrereqsGateUnlock: a hint chain authored over the API gates the purchase exactly
// like an imported one — the gated hint is locked on the detail and unpurchasable until its
// prerequisite is unlocked.
func TestAdminHintPrereqsGateUnlock(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	ch := f.adminChallenge("Hinted", 100, auth...)
	f.adminFlag(ch, "flag{h}", auth...)

	res, body := f.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/challenges/%d/hints", ch),
		map[string]any{"content": "first", "cost": 0}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("add first hint: got %d (%s)", res.StatusCode, body)
	}
	h1 := decodeHint(t, body).ID

	res, body = f.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/challenges/%d/hints", ch),
		map[string]any{"content": "gated", "cost": 0, "prerequisites": []int64{h1}}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("add gated hint: got %d (%s)", res.StatusCode, body)
	}
	gated := decodeHint(t, body)
	if len(gated.Prerequisites) != 1 || gated.Prerequisites[0] != h1 {
		t.Fatalf("gated hint echo = %+v, want prerequisites [%d]", gated, h1)
	}

	playerCookie, playerCSRF := f.register("Ada", "ada@ctf.test", "correct horse battery")

	// The player detail marks the gated hint locked.
	res, body = f.do(http.MethodGet, fmt.Sprintf("/api/v1/challenges/%d", ch), nil, withCookie(playerCookie))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("detail: got %d (%s)", res.StatusCode, body)
	}
	for _, h := range f.decodeDetail(body).Hints {
		if h.ID == gated.ID && !h.Locked {
			t.Error("gated hint is not locked before its prerequisite is unlocked")
		}
		if h.ID == h1 && h.Locked {
			t.Error("prerequisite hint should not be locked")
		}
	}

	// Unlock refused until the prerequisite is unlocked; then it goes through.
	res, body = f.do(http.MethodPost, f.unlockPath(ch, gated.ID), nil, withCookie(playerCookie), withCSRF(playerCSRF))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("gated unlock: got %d, want 403 (%s)", res.StatusCode, body)
	}
	res, body = f.do(http.MethodPost, f.unlockPath(ch, h1), nil, withCookie(playerCookie), withCSRF(playerCSRF))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("prerequisite unlock: got %d (%s)", res.StatusCode, body)
	}
	res, body = f.do(http.MethodPost, f.unlockPath(ch, gated.ID), nil, withCookie(playerCookie), withCSRF(playerCSRF))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("gated unlock after prerequisite: got %d (%s)", res.StatusCode, body)
	}
}

// TestAdminHintPrereqsValidationAndClear: native writes are same-challenge only — the reader would
// match a cross-challenge id, but the editor refuses to author one — and the PATCH set is whole:
// omit keeps, [] and null both clear.
func TestAdminHintPrereqsValidationAndClear(t *testing.T) {
	f := newAdminAPI(t, account.ModeUsers)
	cookie, csrf, _ := f.admin("root", "root@example.com")
	auth := []func(*http.Request){withCookie(cookie), withCSRF(csrf)}

	chA := f.adminChallenge("Alpha", 100, auth...)
	chB := f.adminChallenge("Bravo", 100, auth...)

	res, body := f.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/challenges/%d/hints", chA),
		map[string]any{"content": "on alpha", "cost": 0}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("hint on alpha: got %d (%s)", res.StatusCode, body)
	}
	alphaHint := decodeHint(t, body).ID

	// A hint on another challenge is refused as a prerequisite.
	res, body = f.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/challenges/%d/hints", chB),
		map[string]any{"content": "cross", "cost": 0, "prerequisites": []int64{alphaHint}}, auth...)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("cross-challenge prerequisite: got %d, want 422 (%s)", res.StatusCode, body)
	}

	// So is one that does not exist at all — and the refused create stored nothing.
	res, body = f.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/challenges/%d/hints", chB),
		map[string]any{"content": "dangling", "cost": 0, "prerequisites": []int64{999999}}, auth...)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("dangling prerequisite: got %d, want 422 (%s)", res.StatusCode, body)
	}
	var hintsOnB int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM hints WHERE challenge_id = $1`, chB).Scan(&hintsOnB); err != nil {
		t.Fatalf("count hints: %v", err)
	}
	if hintsOnB != 0 {
		t.Errorf("%d hint(s) on Bravo after refused creates — the validation did not roll the insert back", hintsOnB)
	}

	// A valid chain on one challenge.
	res, body = f.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/challenges/%d/hints", chA),
		map[string]any{"content": "gated", "cost": 0, "prerequisites": []int64{alphaHint}}, auth...)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("gated hint: got %d (%s)", res.StatusCode, body)
	}
	gated := decodeHint(t, body).ID
	hintPath := fmt.Sprintf("/api/v1/admin/challenges/%d/hints/%d", chA, gated)

	// Self-reference is refused on update.
	res, body = f.do(http.MethodPatch, hintPath, map[string]any{"prerequisites": []int64{gated}}, auth...)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("self-reference: got %d, want 422 (%s)", res.StatusCode, body)
	}

	// An unrelated PATCH keeps the set.
	res, body = f.do(http.MethodPatch, hintPath, map[string]any{"cost": 5}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unrelated patch: got %d (%s)", res.StatusCode, body)
	}
	if got := decodeHint(t, body).Prerequisites; len(got) != 1 || got[0] != alphaHint {
		t.Fatalf("prerequisites after unrelated patch = %v, want [%d]", got, alphaHint)
	}

	// An explicit null clears, same as [].
	res, body = f.do(http.MethodPatch, hintPath, map[string]any{"prerequisites": nil}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("null clear: got %d (%s)", res.StatusCode, body)
	}
	if got := decodeHint(t, body).Prerequisites; len(got) != 0 {
		t.Fatalf("prerequisites after null = %v, want none", got)
	}

	// Re-set, then clear with [].
	res, body = f.do(http.MethodPatch, hintPath, map[string]any{"prerequisites": []int64{alphaHint}}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("re-set: got %d (%s)", res.StatusCode, body)
	}
	res, body = f.do(http.MethodPatch, hintPath, map[string]any{"prerequisites": []int64{}}, auth...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("[] clear: got %d (%s)", res.StatusCode, body)
	}
	if got := decodeHint(t, body).Prerequisites; len(got) != 0 {
		t.Fatalf("prerequisites after [] = %v, want none", got)
	}
}
