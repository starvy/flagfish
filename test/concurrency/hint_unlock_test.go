//go:build integration

package concurrency

import (
	"errors"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/gameplay"
)

// Concurrent POSTs of the same unlock must charge the player once. Anything less than a
// unique constraint — a SELECT-then-INSERT, an affordability check against a memoized
// score — lets every goroutine insert, and the player is billed twice for one hint.
func TestHintUnlock_NoDoubleCharge(t *testing.T) {
	for _, mode := range []account.Mode{account.ModeUsers, account.ModeTeams} {
		t.Run(mode.String(), func(t *testing.T) {
			f := setup(t, mode)
			ctx := testCtx(t)

			ch := f.seedChallenge(challengeSpec{Value: 500, Flag: "flagfish{x}"})
			hint := f.seedHint(ch, 100)

			actor, _ := f.seedUser("solver")
			f.grantPoints(actor, 100) // exactly the cost: one unlock is affordable, two are not

			errs := race(N, func(int) error {
				_, err := f.svc.UnlockHint(ctx, ch, hint, actor)
				return err
			})

			winners := 0
			for i, err := range errs {
				switch {
				case err == nil:
					winners++
				case errors.Is(err, gameplay.ErrAlreadyUnlocked):
					// the constraint spoke
				default:
					t.Fatalf("goroutine %d: unexpected error: %v", i, err)
				}
			}
			if winners != 1 {
				t.Errorf("unlocks that succeeded = %d, want exactly 1", winners)
			}

			if n := f.count(`SELECT count(*) FROM hint_unlocks WHERE hint_id = $1`, hint); n != 1 {
				t.Errorf("hint_unlocks rows = %d, want 1 — concurrent unlocks must collapse to one row", n)
			}
			if n := f.count(`SELECT count(*) FROM awards WHERE type = 'hint_unlock'`); n != 1 {
				t.Errorf("hint_unlock awards = %d, want 1 — the player was charged %d times", n, n)
			}

			// The invariant: N goroutines, 100 points, one 100-point hint.
			if got := f.score(actor); got != 0 {
				t.Errorf("score = %d, want 0 — a score below zero is the double-charge bug", got)
			}
			if got := f.score(actor); got < 0 {
				t.Fatalf("SCORE WENT NEGATIVE (%d)", got)
			}
		})
	}
}

// C1b — the half a unique constraint cannot fix.
//
// UNIQUE(hint_id, account) stops the same hint being bought twice, but buying twenty
// distinct hints at once with points for three satisfies every constraint in the schema
// and still overdraws. Only the account-row lock in UnlockHint defends that.
func TestHintUnlock_ConcurrentDistinctHints_ScoreNeverNegative(t *testing.T) {
	for _, mode := range []account.Mode{account.ModeUsers, account.ModeTeams} {
		t.Run(mode.String(), func(t *testing.T) {
			f := setup(t, mode)
			ctx := testCtx(t)

			ch := f.seedChallenge(challengeSpec{Value: 500, Flag: "flagfish{x}"})

			const hints = 20
			const cost = 100
			ids := make([]int64, hints)
			for i := range hints {
				ids[i] = f.seedHint(ch, cost)
			}

			actor, _ := f.seedUser("spender")
			f.grantPoints(actor, 3*cost) // funded for three of the twenty

			errs := race(hints, func(i int) error {
				_, err := f.svc.UnlockHint(ctx, ch, ids[i], actor)
				return err
			})

			bought := 0
			for i, err := range errs {
				switch {
				case err == nil:
					bought++
				case errors.Is(err, gameplay.ErrInsufficientScore):
					// the balance was read exactly, under the lock, and it said no
				default:
					t.Fatalf("goroutine %d: unexpected error: %v", i, err)
				}
			}

			if bought != 3 {
				t.Errorf("hints bought = %d, want exactly 3 (funded for 3)", bought)
			}
			if n := f.count(`SELECT count(*) FROM awards WHERE type = 'hint_unlock'`); n != 3 {
				t.Errorf("charges = %d, want 3", n)
			}

			if got := f.score(actor); got != 0 {
				t.Errorf("score = %d, want 0", got)
			}
			if got := f.score(actor); got < 0 {
				t.Fatalf("SCORE WENT NEGATIVE (%d) — the affordability check read a stale balance", got)
			}
		})
	}
}

// A rolled-back unlock must leave no charge behind.
//
// The award must be inserted before the hint_unlocks row (award_id is NOT NULL), so on
// the already-unlocked path the player is briefly charged for nothing. UnlockHint has to
// roll back there rather than return early — and returning early is the natural refactor.
// The double-charge case above still passes if that rollback is dropped, which is why this
// one is separate.
func TestHintUnlock_AlreadyUnlocked_LeavesNoOrphanCharge(t *testing.T) {
	f := setup(t, account.ModeUsers)
	ctx := testCtx(t)

	ch := f.seedChallenge(challengeSpec{Value: 500, Flag: "flagfish{x}"})
	hint := f.seedHint(ch, 10)

	actor, _ := f.seedUser("buyer")
	f.grantPoints(actor, 1000)

	if _, err := f.svc.UnlockHint(ctx, ch, hint, actor); err != nil {
		t.Fatalf("first unlock: %v", err)
	}
	scoreAfterFirst := f.score(actor)

	// Serially, so this exercises the ErrAlreadyUnlocked path with no race at all.
	if _, err := f.svc.UnlockHint(ctx, ch, hint, actor); !errors.Is(err, gameplay.ErrAlreadyUnlocked) {
		t.Fatalf("second unlock: err = %v, want ErrAlreadyUnlocked", err)
	}

	if n := f.count(`SELECT count(*) FROM awards WHERE type = 'hint_unlock'`); n != 1 {
		t.Errorf("charges = %d, want 1 — the second attempt left an ORPHAN CHARGE", n)
	}
	if got := f.score(actor); got != scoreAfterFirst {
		t.Errorf("score = %d, want %d — the player was billed for a hint they already owned",
			got, scoreAfterFirst)
	}
}
