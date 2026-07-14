//go:build e2e

package e2e

import (
	"context"
	"testing"
)

// TestHintUnlock covers the hint economy: a hint cannot be unlocked without the points to
// pay for it, and once the player has scored, unlocking reveals the content and charges the
// cost against their balance.
func TestHintUnlock(t *testing.T) {
	ctx := context.Background()
	const flag = "flag{hint-me}"
	chal := adminCreateChallenge(t, "hinted-"+suffix(), "crypto", 100)
	adminAddStaticFlag(t, chal, flag)
	hint := adminAddHint(t, chal, "the secret is in the header", 50)

	u := mustRegister(t)

	// With zero points the unlock is refused for lack of funds (402 Payment Required).
	broke, err := u.api.UnlockHintWithResponse(ctx, chal, hint)
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if broke.StatusCode() != 402 {
		t.Fatalf("unlock with no points = %d, want 402; body=%s", broke.StatusCode(), broke.Body)
	}

	// Score the challenge, then the unlock succeeds and charges the cost.
	if got := u.solve(t, chal, flag); got.Status != "correct" {
		t.Fatalf("solve status = %q", got.Status)
	}
	ok, err := u.api.UnlockHintWithResponse(ctx, chal, hint)
	if err != nil || ok.JSON200 == nil {
		t.Fatalf("unlock after scoring: %v (status %d, body %s)", err, ok.StatusCode(), ok.Body)
	}
	if ok.JSON200.Content == "" {
		t.Error("unlocked hint returned no content")
	}
	if ok.JSON200.Charged != 50 {
		t.Errorf("charged = %d, want 50", ok.JSON200.Charged)
	}
	if ok.JSON200.Score != 50 {
		t.Errorf("balance after unlock = %d, want 50 (100 scored - 50 hint)", ok.JSON200.Score)
	}
}
