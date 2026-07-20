//go:build e2e

package e2e

import (
	"context"
	"testing"
)

// TestPrerequisiteUnlock proves the prerequisite gate end to end: a challenge whose
// prerequisite is unmet is locked and unsubmittable; solving the prerequisite unlocks it.
func TestPrerequisiteUnlock(t *testing.T) {
	ctx := context.Background()
	const prereqFlag = "flag{prereq}"
	const lockedFlag = "flag{unlocked}"

	prereq := adminCreateChallenge(t, "gate-"+suffix(), "intro", 100)
	adminAddStaticFlag(t, prereq, prereqFlag)

	locked := adminCreateChallenge(t, "boss-"+suffix(), "intro", 200)
	adminAddStaticFlag(t, locked, lockedFlag)

	// "preview" keeps the real name visible but the challenge locked until the prerequisite
	// is solved.
	admin.adminReq(t, "PUT", path("/challenges/%d/requirements", locked),
		map[string]any{"prerequisites": []int64{prereq}, "visibility": "preview"}).require(t, 200)

	u := mustRegister(t)

	// Before solving the prerequisite: locked on the board, and unsubmittable.
	list, err := u.api.ListChallengesWithResponse(ctx)
	if err != nil || list.JSON200 == nil {
		t.Fatalf("list: %v", err)
	}
	if item := findListItem(t, list.JSON200, locked); !item.Locked {
		t.Error("challenge with an unmet prerequisite should be locked")
	}
	blocked, err := u.api.AttemptWithResponse(ctx, locked, attempt(lockedFlag))
	if err != nil {
		t.Fatalf("attempt locked: %v", err)
	}
	if blocked.StatusCode() != 403 {
		t.Fatalf("attempt on a locked challenge = %d, want 403; body=%s", blocked.StatusCode(), blocked.Body)
	}

	// Solve the prerequisite.
	if got := u.solve(t, prereq, prereqFlag); got.Status != "correct" {
		t.Fatalf("prereq solve status = %q", got.Status)
	}

	// Now it is unlocked and submittable.
	list2, err := u.api.ListChallengesWithResponse(ctx)
	if err != nil || list2.JSON200 == nil {
		t.Fatalf("list after prereq: %v", err)
	}
	if item := findListItem(t, list2.JSON200, locked); item.Locked {
		t.Error("challenge should unlock once its prerequisite is solved")
	}
	if got := u.solve(t, locked, lockedFlag); got.Status != "correct" {
		t.Errorf("unlocked solve status = %q, want correct", got.Status)
	}
}
