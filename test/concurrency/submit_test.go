//go:build integration

package concurrency

import (
	"fmt"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/domain/scoring"
	"github.com/starvy/flagfish/internal/gameplay"
)

const correctFlag = "flagfish{the_one_true_flag}"

// One account, N simultaneous submissions of the same correct flag, exactly one solve row.
//
// The duplicate check is the unique constraint, reached through ON CONFLICT DO NOTHING …
// RETURNING, whose zero rows are the already-solved signal. There must never be a SELECT
// on this path: a SELECT-then-INSERT is a race with better manners.
func TestSubmit_DuplicateSolve(t *testing.T) {
	for _, mode := range []account.Mode{account.ModeUsers, account.ModeTeams} {
		t.Run(mode.String(), func(t *testing.T) {
			f := setup(t, mode)
			ctx := testCtx(t)

			ch := f.seedChallenge(challengeSpec{Value: 500, Flag: correctFlag})
			actor, _ := f.seedUser("solver")

			errs := race(N, func(int) error {
				_, err := f.svc.Submit(ctx, gameplay.SubmitInput{
					ChallengeID: ch, Actor: actor, Provided: correctFlag,
				})
				return err
			})
			for i, err := range errs {
				if err != nil {
					t.Fatalf("goroutine %d: %v", i, err)
				}
			}

			if n := f.count(`SELECT count(*) FROM solves WHERE challenge_id = $1`, ch); n != 1 {
				t.Errorf("solves = %d, want exactly 1", n)
			}

			if got := f.score(actor); got != 500 {
				t.Errorf("score = %d, want 500 — a double solve pays twice", got)
			}

			// Every attempt is still logged as correct, deliberately: the log records what
			// was attempted, not what it earned, and a correct flag from an account that
			// already solved is the shape of a shared flag. Dropping those rows would
			// destroy the anti-cheat evidence.
			if n := f.count(`SELECT count(*) FROM submissions WHERE challenge_id = $1 AND type = 'correct'`, ch); n != N {
				t.Errorf("correct submissions logged = %d, want %d (the audit trail is append-only)", n, N)
			}
		})
	}
}

// N distinct accounts submit the correct flag at once: exactly one first-blood award row,
// one caller told, one announcement enqueued.
//
// First blood is decided inside the submitting transaction under the challenge lock, so
// the response can claim it without a second read. The partial unique index backstops the
// paths the lock does not cover: an admin grant, an import, a psql session.
func TestSubmit_FirstBlood_ExactlyOnce(t *testing.T) {
	for _, mode := range []account.Mode{account.ModeUsers, account.ModeTeams} {
		t.Run(mode.String(), func(t *testing.T) {
			f := setup(t, mode)
			ctx := testCtx(t)

			ch := f.seedChallenge(challengeSpec{
				Value: 500, Flag: correctFlag,
				FirstBlood: "bonus", Bonus: i32(50),
			})

			actors := make([]gameplay.Actor, N)
			for i := range N {
				actors[i], _ = f.seedUser(fmt.Sprintf("p%03d", i))
			}

			claimed := make([]bool, N)
			errs := race(N, func(i int) error {
				res, err := f.svc.Submit(ctx, gameplay.SubmitInput{
					ChallengeID: ch, Actor: actors[i], Provided: correctFlag,
				})
				claimed[i] = res.FirstBlood
				return err
			})
			for i, err := range errs {
				if err != nil {
					t.Fatalf("goroutine %d: %v", i, err)
				}
			}

			// Everybody solved it; only one bled.
			if n := f.count(`SELECT count(*) FROM solves WHERE challenge_id = $1`, ch); n != N {
				t.Errorf("solves = %d, want %d", n, N)
			}

			if n := f.count(
				`SELECT count(*) FROM awards WHERE type = 'first_blood' AND challenge_id = $1`, ch,
			); n != 1 {
				t.Errorf("first_blood awards = %d, want exactly 1", n)
			}

			// The service must agree with the database. Awarding once but telling five
			// players they were first is still broken — they all screenshot it.
			told := 0
			for _, c := range claimed {
				if c {
					told++
				}
			}
			if told != 1 {
				t.Errorf("callers told 'first blood' = %d, want exactly 1", told)
			}

			// Enqueued in the solve's own transaction; this counts River's real table.
			if n := f.count(
				`SELECT count(*) FROM river_job WHERE kind = 'announce_first_blood'`,
			); n != 1 {
				t.Errorf("announcements enqueued = %d, want exactly 1", n)
			}
		})
	}
}

// C3b — a hidden account can neither claim a first blood nor burn one.
//
// Two rules, and a prior-solve count alone gets the second wrong: a hidden solver excludes
// itself from that count, so it reads 0, claims the bonus, and the first honest solver
// then trips the partial unique index and gets an error page instead of their first blood.
// Hence the eligibility check is separate from the count.
func TestSubmit_HiddenAccount_CannotClaimOrBurnFirstBlood(t *testing.T) {
	f := setup(t, account.ModeUsers)
	ctx := testCtx(t)

	ch := f.seedChallenge(challengeSpec{
		Value: 500, Flag: correctFlag,
		FirstBlood: "bonus", Bonus: i32(50),
	})

	admin, _ := f.seedUser("hidden-admin")
	f.setVisibility(admin, true, false)

	// The hidden admin test-solves it first.
	res, err := f.svc.Submit(ctx, gameplay.SubmitInput{
		ChallengeID: ch, Actor: admin, Provided: correctFlag,
	})
	if err != nil {
		t.Fatalf("hidden solve: %v", err)
	}
	if res.FirstBlood {
		t.Error("the hidden admin CLAIMED first blood — actor_eligible was not checked")
	}
	if n := f.count(`SELECT count(*) FROM awards WHERE type = 'first_blood'`); n != 0 {
		t.Fatalf("first_blood awards after hidden solve = %d, want 0", n)
	}

	// The first real player must draw blood, and must not 500 on the partial unique index.
	player, _ := f.seedUser("player")
	res, err = f.svc.Submit(ctx, gameplay.SubmitInput{
		ChallengeID: ch, Actor: player, Provided: correctFlag,
	})
	if err != nil {
		t.Fatalf("the first honest solver's submit FAILED (this is the bug): %v", err)
	}
	if !res.FirstBlood {
		t.Error("the first visible solver did NOT draw blood — a hidden solve burned it")
	}
	if n := f.count(`SELECT count(*) FROM awards WHERE type = 'first_blood'`); n != 1 {
		t.Errorf("first_blood awards = %d, want 1", n)
	}
}

// A decaying challenge value must be exact after N concurrent solves.
//
// Recomputing it read-modify-write would let every solver read the same stale solve count
// and the last writer would win — the board would settle on f(1) after N solves and
// nothing would report it. The recalc is one statement under the challenge lock instead,
// so the count cannot be stale, and exactness falls out of a lock taken for first blood
// anyway.
func TestSubmit_DecayIsExactUnderConcurrency(t *testing.T) {
	for _, fn := range []string{"linear", "logarithmic"} {
		t.Run(fn, func(t *testing.T) {
			f := setup(t, account.ModeUsers)
			ctx := testCtx(t)

			const (
				initial = 500
				minimum = 100
				decay   = 30
			)
			ch := f.seedChallenge(challengeSpec{
				Value: initial, Flag: correctFlag,
				Function: fn,
				Initial:  i32(initial), Minimum: i32(minimum), Decay: i32(decay),
			})

			actors := make([]gameplay.Actor, N)
			for i := range N {
				actors[i], _ = f.seedUser(fmt.Sprintf("d%03d", i))
			}

			errs := race(N, func(i int) error {
				_, err := f.svc.Submit(ctx, gameplay.SubmitInput{
					ChallengeID: ch, Actor: actors[i], Provided: correctFlag,
				})
				return err
			})
			for i, err := range errs {
				if err != nil {
					t.Fatalf("goroutine %d: %v", i, err)
				}
			}

			if n := f.count(`SELECT count(*) FROM solves WHERE challenge_id = $1`, ch); n != N {
				t.Fatalf("solves = %d, want %d", n, N)
			}

			// The value must be f(solve count), not f(whatever the last writer saw). The
			// expectation comes from the pure domain curve; test/integration pins that the
			// SQL agrees with it.
			curve := scoring.Curve{
				Function: mustParseFn(t, fn),
				Initial:  initial, Minimum: minimum, Decay: decay,
			}
			want := int32(curve.ValueAt(N))
			if got := f.challengeValue(ch); got != want {
				t.Errorf("challenges.value = %d, want %d = f(%d solves) — last writer won", got, want, N)
			}
		})
	}
}

// C4b — solves.value is stamped from the locked read, not the optimistic one before it.
//
// The pre-lock read can be stale by the time the lock is granted; stamping it is a
// one-word bug that pays some solvers the wrong points and never reports itself.
//
// Whatever order the goroutines commit in, the k-th solver pays ValueAt(k), so the
// stamped values are exactly the multiset {ValueAt(0) … ValueAt(N-1)}. A stale read would
// collapse values onto each other and break it.
func TestSubmit_SolveValueIsSnapshotUnderTheLock(t *testing.T) {
	f := setup(t, account.ModeUsers)
	ctx := testCtx(t)

	const (
		initial = 1000
		minimum = 100
		decay   = 1 // steep, so every step of the curve is a distinct value
	)
	ch := f.seedChallenge(challengeSpec{
		Value: initial, Flag: correctFlag,
		Function: "linear",
		Initial:  i32(initial), Minimum: i32(minimum), Decay: i32(decay),
	})

	actors := make([]gameplay.Actor, N)
	for i := range N {
		actors[i], _ = f.seedUser(fmt.Sprintf("s%03d", i))
	}

	errs := race(N, func(i int) error {
		_, err := f.svc.Submit(ctx, gameplay.SubmitInput{
			ChallengeID: ch, Actor: actors[i], Provided: correctFlag,
		})
		return err
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	curve := scoring.Curve{
		Function: scoring.FunctionLinear,
		Initial:  initial, Minimum: minimum, Decay: decay,
	}
	want := make([]int, 0, N)
	for k := range N {
		want = append(want, curve.ValueAt(k)) // the k-th solver paid the price after k prior solves
	}
	slices.Sort(want)

	rows, err := f.pool.Query(ctx, `SELECT value FROM solves WHERE challenge_id = $1`, ch)
	if err != nil {
		t.Fatalf("read solves: %v", err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[int32])
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	gotInts := make([]int, len(got))
	for i, v := range got {
		gotInts[i] = int(v)
	}
	slices.Sort(gotInts)

	if !slices.Equal(gotInts, want) {
		t.Errorf("stamped values do not match the curve.\n got: %v\nwant: %v\n"+
			"the price was read BEFORE the lock, so concurrent solvers stamped a stale value",
			gotInts, want)
	}
}

func mustParseFn(t *testing.T, s string) scoring.Function {
	t.Helper()
	fn, err := scoring.ParseFunction(s)
	if err != nil {
		t.Fatalf("parse function %q: %v", s, err)
	}
	return fn
}
