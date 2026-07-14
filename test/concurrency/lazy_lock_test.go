//go:build integration

package concurrency

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/gameplay"
)

// A wrong answer must never take the challenge lock.
//
// The lazy ordering is a performance property with no correctness symptom: move the lock
// to the top of the submit transaction, where it looks like it belongs, and every other
// test in this package still passes while the product quietly serializes every wrong guess
// on the hottest challenge through one row. Only this test defends it.
//
// Sampling and timing would both be flaky, so instead we hold the challenge lock in an
// outer transaction and require that a wrong answer commits anyway (it never wanted the
// lock) while a correct one blocks. The second half is the control: without it, a submit
// path that took no lock at all would also pass.
func TestLazyLock_WrongAnswerNeverTakesTheChallengeLock(t *testing.T) {
	f := setup(t, account.ModeUsers)
	ctx := testCtx(t)

	ch := f.seedChallenge(challengeSpec{Value: 500, Flag: correctFlag})

	// Hold the challenge lock, exactly as the correct path would.
	holder, err := f.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		t.Fatalf("begin holder: %v", err)
	}
	defer func() { _ = holder.Rollback(ctx) }()

	var locked int64
	if lockErr := holder.QueryRow(ctx,
		`SELECT id FROM challenges WHERE id = $1 FOR NO KEY UPDATE`, ch).Scan(&locked); lockErr != nil {
		t.Fatalf("take the lock: %v", lockErr)
	}

	// 1. Wrong answers must sail through, while another transaction holds the lock. The
	// deadline is what catches the regression: if the lazy ordering is lost they wait on
	// the holder and blow it.
	wrongCtx, cancelWrong := context.WithTimeout(ctx, 15*time.Second)
	defer cancelWrong()

	actors := make([]gameplay.Actor, N)
	for i := range N {
		actors[i], _ = f.seedUser(itoa(i))
	}

	errs := race(N, func(i int) error {
		res, subErr := f.svc.Submit(wrongCtx, gameplay.SubmitInput{
			ChallengeID: ch, Actor: actors[i], Provided: "flagfish{definitely_not_it}",
		})
		if subErr == nil && res.Status != gameplay.StatusIncorrect {
			return errors.New("expected StatusIncorrect")
		}
		return subErr
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("the lazy lock is broken: a wrong answer blocked on the challenge lock "+
				"(goroutine %d: %v).\n"+
				"    The lock has been moved to the top of the submit transaction, or "+
				"strengthened to FOR UPDATE.\n"+
				"    ~99%% of submissions are wrong answers: this serializes the entire event.", i, err)
		}
	}

	// Committed and durable, with the lock still held.
	if n := f.count(`SELECT count(*) FROM submissions WHERE challenge_id = $1 AND type = 'incorrect'`, ch); n != N {
		t.Errorf("incorrect submissions committed under the held lock = %d, want %d", n, N)
	}

	// 2. The control: a correct answer must block, proving the assertion above means "the
	// wrong answer did not want the lock" and not "there is no lock to take".
	blockCtx, cancelBlock := context.WithTimeout(ctx, 2*time.Second)
	defer cancelBlock()

	winner, _ := f.seedUser("the-solver")
	_, err = f.svc.Submit(blockCtx, gameplay.SubmitInput{
		ChallengeID: ch, Actor: winner, Provided: correctFlag,
	})
	if err == nil {
		t.Fatal("a correct submission completed while the challenge lock was held by another " +
			"transaction — the lock is not being taken at all, so the exact-decay and " +
			"exact-first-blood guarantees rest on nothing")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("correct submission failed for the wrong reason: %v (want a lock wait / deadline)", err)
	}

	// 3. Release, and the correct path proceeds normally.
	if rbErr := holder.Rollback(ctx); rbErr != nil {
		t.Fatalf("release the lock: %v", rbErr)
	}
	res, err := f.svc.Submit(ctx, gameplay.SubmitInput{
		ChallengeID: ch, Actor: winner, Provided: correctFlag,
	})
	if err != nil {
		t.Fatalf("submit after the lock was released: %v", err)
	}
	if res.Status != gameplay.StatusCorrect {
		t.Errorf("status = %s, want correct", res.Status)
	}
}

// C11b — the same claim, live: wrong answers interleaved with real solvers on one hot
// challenge must not be dragged into the solvers' lock queue. The test above holds the
// lock artificially; this one lets the solvers hold it.
func TestLazyLock_MixedWorkload_WrongAnswersDoNotQueueBehindSolvers(t *testing.T) {
	f := setup(t, account.ModeUsers)
	ctx := testCtx(t)

	ch := f.seedChallenge(challengeSpec{
		Value: 500, Flag: correctFlag,
		Function: "logarithmic",
		Initial:  i32(500), Minimum: i32(100), Decay: i32(50),
		FirstBlood: "bonus", Bonus: i32(50),
	})

	actors := make([]gameplay.Actor, N)
	for i := range N {
		actors[i], _ = f.seedUser(itoa(i))
	}

	// 75% wrong, which is generous to us: a real CTF is closer to 99%.
	errs := race(N, func(i int) error {
		provided := "flagfish{nope}"
		if i%4 == 0 {
			provided = correctFlag
		}
		_, err := f.svc.Submit(ctx, gameplay.SubmitInput{
			ChallengeID: ch, Actor: actors[i], Provided: provided,
		})
		return err
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	solvers := int64((N + 3) / 4)
	if n := f.count(`SELECT count(*) FROM solves WHERE challenge_id = $1`, ch); n != solvers {
		t.Errorf("solves = %d, want %d", n, solvers)
	}
	if n := f.count(`SELECT count(*) FROM submissions WHERE challenge_id = $1 AND type = 'incorrect'`, ch); n != int64(N)-solvers {
		t.Errorf("incorrect = %d, want %d", n, int64(N)-solvers)
	}
	if n := f.count(`SELECT count(*) FROM awards WHERE type = 'first_blood'`); n != 1 {
		t.Errorf("first_blood awards = %d, want 1", n)
	}
}

func itoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "u0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{digits[i%10]}, b...)
		i /= 10
	}
	return "u" + string(b)
}
