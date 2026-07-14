//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/starvy/flagfish/e2e/client"
)

// TestScoreboardFreeze proves the freeze is real: once a freeze instant is set, the public
// board is served as it stood at that instant, so a score earned after it is hidden — while
// a freeze-exempt admin (?preview) still sees the live board. ?as_of cannot travel past the
// freeze horizon for a non-exempt viewer.
//
// The freeze is a global config knob, so the test neutralises it at the end by pushing the
// horizon far into the future (which makes the frozen board identical to the live one).
func TestScoreboardFreeze(t *testing.T) {
	ctx := context.Background()
	const flag = "flag{freeze}"
	chal := adminCreateChallenge(t, "freeze-"+suffix(), "pwn", 100)
	adminAddStaticFlag(t, chal, flag)

	u := mustRegister(t)
	if got := u.solve(t, chal, flag); got.Status != "correct" {
		t.Fatalf("solve status = %q", got.Status)
	}

	// Live board: the solver is on it.
	live, err := u.api.ScoreboardWithResponse(ctx, &client.ScoreboardParams{})
	if err != nil || live.JSON200 == nil {
		t.Fatalf("scoreboard: %v (status %d)", err, live.StatusCode())
	}
	if findStanding(live.JSON200, u.name) == nil {
		t.Fatal("solver should be on the live board before any freeze")
	}

	// Freeze at an instant BEFORE the solve. The public board rewinds to then.
	freezeAt := time.Now().Add(-time.Hour)
	setConfig(t, map[string]any{"freeze": freezeAt.UTC().Format(time.RFC3339)})
	defer setConfig(t, map[string]any{"freeze": time.Now().Add(100 * 365 * 24 * time.Hour).UTC().Format(time.RFC3339)})

	// A non-exempt viewer no longer sees the post-freeze score.
	frozen, err := u.api.ScoreboardWithResponse(ctx, &client.ScoreboardParams{})
	if err != nil || frozen.JSON200 == nil {
		t.Fatalf("frozen scoreboard: %v (status %d)", err, frozen.StatusCode())
	}
	if findStanding(frozen.JSON200, u.name) != nil {
		t.Error("a score earned after the freeze must be hidden on the public board")
	}

	// ?as_of a future instant is clamped to the freeze horizon — still hidden.
	future := time.Now().Add(time.Hour)
	travel, err := u.api.ScoreboardWithResponse(ctx, &client.ScoreboardParams{AsOf: &future})
	if err != nil || travel.JSON200 == nil {
		t.Fatalf("as_of scoreboard: %v (status %d)", err, travel.StatusCode())
	}
	if findStanding(travel.JSON200, u.name) != nil {
		t.Error("?as_of must not let a non-exempt viewer travel past the freeze")
	}

	// The admin, freeze-exempt with ?preview, still sees the live standings.
	preview, err := admin.api.ScoreboardWithResponse(ctx, &client.ScoreboardParams{Preview: ptr(true)})
	if err != nil || preview.JSON200 == nil {
		t.Fatalf("preview scoreboard: %v (status %d)", err, preview.StatusCode())
	}
	if findStanding(preview.JSON200, u.name) == nil {
		t.Error("an admin preview should still see the post-freeze score")
	}
}
