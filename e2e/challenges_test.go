//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"

	"github.com/starvy/flagfish/e2e/client"
)

// TestChallengeSolveLifecycle is the competition happy path: an admin publishes a challenge
// with a static flag, a player sees it, a wrong flag is rejected, the right flag scores and
// takes first blood, a resubmit is already-solved, a second player scores but does not take
// first blood, and the scoreboard and solve list reflect all of it.
func TestChallengeSolveLifecycle(t *testing.T) {
	ctx := context.Background()
	const flag = "flag{e2e-correct-flag}"
	chal := adminCreateChallenge(t, "solve-me-"+suffix(), "web", 100)
	adminAddStaticFlag(t, chal, flag)
	// origin/main exposes no admin API to enable first blood (challenges.first_blood defaults
	// to 'none'), so turn it on directly to exercise the announcement path.
	if err := execSQL(ctx, `UPDATE challenges SET first_blood = 'announce' WHERE id = $1`, chal); err != nil {
		t.Fatalf("enable first blood: %v", err)
	}

	a := mustRegister(t)

	// The board lists it, unsolved, at full value.
	list, err := a.api.ListChallengesWithResponse(ctx)
	if err != nil || list.JSON200 == nil {
		t.Fatalf("list: %v (status %d)", err, list.StatusCode())
	}
	item := findListItem(t, list.JSON200, chal)
	if item.Solved {
		t.Error("challenge should start unsolved")
	}
	if item.Value != 100 {
		t.Errorf("list value = %d, want 100", item.Value)
	}

	// A wrong flag is incorrect and scores nothing.
	wrong := a.solve(t, chal, "flag{wrong}")
	if wrong.Status != "incorrect" {
		t.Errorf("wrong flag status = %q, want incorrect", wrong.Status)
	}
	if wrong.Value != 0 {
		t.Errorf("incorrect value = %d, want 0", wrong.Value)
	}

	// The right flag scores and takes first blood.
	correct := a.solve(t, chal, flag)
	if correct.Status != "correct" {
		t.Errorf("correct flag status = %q, want correct", correct.Status)
	}
	if !correct.FirstBlood {
		t.Error("the first solver should take first blood")
	}
	if correct.Value != 100 {
		t.Errorf("solve value = %d, want 100", correct.Value)
	}

	// Resubmitting is already-solved, not a second score.
	again := a.solve(t, chal, flag)
	if again.Status != "already_solved" {
		t.Errorf("resubmit status = %q, want already_solved", again.Status)
	}

	// Detail now reports solved.
	detail, err := a.api.ChallengeDetailWithResponse(ctx, chal)
	if err != nil || detail.JSON200 == nil {
		t.Fatalf("detail: %v (status %d)", err, detail.StatusCode())
	}
	if !detail.JSON200.Solved {
		t.Error("detail should report solved after a correct submission")
	}
	if detail.JSON200.Locked {
		t.Error("a challenge with no prerequisites must not be locked")
	}

	// A second solver scores but does not take first blood.
	b := mustRegister(t)
	bs := b.solve(t, chal, flag)
	if bs.Status != "correct" {
		t.Errorf("second solver status = %q, want correct", bs.Status)
	}
	if bs.FirstBlood {
		t.Error("the second solver must not take first blood")
	}

	// The solve list names both solvers.
	solves, err := a.api.ChallengeSolvesWithResponse(ctx, chal)
	if err != nil || solves.JSON200 == nil {
		t.Fatalf("solves: %v (status %d)", err, solves.StatusCode())
	}
	if solves.JSON200.Solves == nil || len(*solves.JSON200.Solves) < 2 {
		t.Errorf("expected at least two solves, got %v", solves.JSON200.Solves)
	}

	// Both solvers appear on the scoreboard with the challenge's value.
	sb, err := a.api.ScoreboardWithResponse(ctx, &client.ScoreboardParams{})
	if err != nil || sb.JSON200 == nil {
		t.Fatalf("scoreboard: %v (status %d)", err, sb.StatusCode())
	}
	if s := findStanding(sb.JSON200, a.name); s == nil || s.Score < 100 {
		t.Errorf("first solver missing or under-scored on the board: %v", s)
	}
	if s := findStanding(sb.JSON200, b.name); s == nil || s.Score < 100 {
		t.Errorf("second solver missing or under-scored on the board: %v", s)
	}
}

// TestSubmitRequiresAuth confirms an anonymous attempt is walled off (an API 403, not a
// redirect the caller cannot act on).
func TestSubmitRequiresAuth(t *testing.T) {
	chal := adminCreateChallenge(t, "auth-wall-"+suffix(), "misc", 50)
	adminAddStaticFlag(t, chal, "flag{whatever}")

	resp := anonUser().publicReq(t, "POST", fmt.Sprintf("/challenges/%d/attempt", chal), map[string]any{"flag": "x"})
	if resp.code != 403 {
		t.Fatalf("anonymous attempt status = %d, want 403; body=%s", resp.code, resp.body)
	}
}

func findListItem(t *testing.T, body *client.ChallengesOutputBody, id int64) client.ChallengeListItem {
	t.Helper()
	if body.Challenges != nil {
		for _, it := range *body.Challenges {
			if it.Id == id {
				return it
			}
		}
	}
	t.Fatalf("challenge %d not found on the board", id)
	return client.ChallengeListItem{}
}

func findStanding(body *client.ScoreboardOutputBody, name string) *client.Standing {
	if body.Standings == nil {
		return nil
	}
	for i := range *body.Standings {
		if (*body.Standings)[i].Name == name {
			return &(*body.Standings)[i]
		}
	}
	return nil
}

// suffix returns a short unique string for names that must not collide across tests.
func suffix() string { return fmt.Sprintf("%d", uniq.Add(1)) }

func attempt(flag string) client.AttemptInputBody { return client.AttemptInputBody{Flag: flag} }

func ptr[T any](v T) *T { return &v }
