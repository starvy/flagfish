//go:build integration

package concurrency

import (
	"errors"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/gameplay"
)

// max_attempts is exact under one account's concurrent submissions.
//
// The gate counts an account's wrong answers, then inserts one more — a check-then-act.
// Fire many wrong submissions from ONE account at once and, without a lock, they all read
// the same stale count before any commits, so every one slips past the cap: a scripted
// client brute-forces the flag past max_attempts. The per-(challenge, account) advisory
// lock serializes this account's own submissions so the count is exact.
//
// Revert the lock in submit.go and this fails: far more than the cap of wrong answers land.
func TestMaxAttemptsExactUnderConcurrency(t *testing.T) {
	const attempts = 20 // more than the cap, so most must be turned away
	const maxTries = 3

	run := func(t *testing.T, mode account.Mode) {
		t.Helper()
		f := setup(t, mode)
		ctx := testCtx(t)

		ch := f.seedChallenge(challengeSpec{Value: 500, Flag: correctFlag})
		if _, err := f.pool.Exec(ctx,
			`UPDATE challenges SET max_attempts = $1 WHERE id = $2`, maxTries, ch); err != nil {
			t.Fatalf("set max_attempts: %v", err)
		}

		// One account: the lock keys on it, and the whole point is that this account's own
		// concurrent guesses serialize.
		actor, _ := f.seedUser("brute-forcer")

		errs := race(attempts, func(int) error {
			_, err := f.svc.Submit(ctx, gameplay.SubmitInput{
				ChallengeID: ch, Actor: actor, Provided: "flagfish{wrong}",
			})
			return err
		})

		var admitted, refused int
		for i, err := range errs {
			switch {
			case err == nil:
				admitted++
			case errors.Is(err, gameplay.ErrNoAttemptsRemaining):
				refused++
			default:
				t.Fatalf("goroutine %d: unexpected error: %v", i, err)
			}
		}

		if admitted != maxTries {
			t.Errorf("%s: submissions admitted past the gate = %d, want %d "+
				"(the max_attempts count is racy — the lock is missing or not per-account)",
				mode, admitted, maxTries)
		}
		if refused != attempts-maxTries {
			t.Errorf("%s: submissions refused = %d, want %d", mode, refused, attempts-maxTries)
		}
		if n := f.count(
			`SELECT count(*) FROM submissions WHERE challenge_id = $1 AND type = 'incorrect'`, ch,
		); n != maxTries {
			t.Errorf("%s: incorrect submissions recorded = %d, want %d "+
				"(a scripted client got extra flag comparisons past the cap)", mode, n, maxTries)
		}
	}

	// The lock key differs by mode (user_id vs team_id); assert it lands on the right column
	// in both.
	t.Run("users", func(t *testing.T) { run(t, account.ModeUsers) })
	t.Run("teams", func(t *testing.T) { run(t, account.ModeTeams) })
}
