//go:build integration

package concurrency

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/domain/flags"
	"github.com/starvy/flagfish/internal/gameplay"
)

// A pooled flag instance must never be issued to two accounts.
//
// The whole anti-cheat property rests on one sentence: an instance is issued to at most
// one account. Hand the same flag to two accounts and a shared flag proves nothing, the
// review queue fills with false positives, and the feature is worse than absent — people
// get banned over it.
//
// Issuing is lazy, so it is a check-then-insert by nature. What makes it safe:
//
//	advisory lock per challenge            — serializes the pick, taken as its own statement
//	UNIQUE (instance_id)                   — an instance goes to at most one account
//	PRIMARY KEY (challenge_id, account_id) — an account gets at most one instance
//
// FOR UPDATE … SKIP LOCKED was tried and is NOT sufficient, which this test is what
// proved: it locks the challenge_instances row, but "is it issued?" reads flag_issues — a
// different table — so a transaction whose snapshot predates a concurrent commit still
// sees the instance as free, and by then the other transaction has released the row lock,
// so nothing gets skipped. The loser raises UNIQUE(instance_id), which ON CONFLICT does
// not catch because its target is the primary key. Players get a 500 at CTF start.
func TestFlagPool_NoDoubleIssue(t *testing.T) {
	for _, mode := range []account.Mode{account.ModeUsers, account.ModeTeams} {
		t.Run(mode.String(), func(t *testing.T) {
			f := setup(t, mode)
			ctx := testCtx(t)

			ch := f.seedChallenge(challengeSpec{Value: 500, FlagMode: "unique"})
			f.seedInstances(ch, N) // exactly enough for everyone

			accounts := make([]int64, N)
			for i := range N {
				_, accounts[i] = f.seedUser(fmt.Sprintf("v%03d", i))
			}

			var mu sync.Mutex
			got := make(map[int64]int64) // instanceID -> accountID

			errs := race(N, func(i int) error {
				inst, err := f.svc.IssueInstance(ctx, ch, accounts[i])
				if err != nil {
					return err
				}
				mu.Lock()
				defer mu.Unlock()
				if prev, dup := got[inst.InstanceID]; dup {
					return fmt.Errorf("instance %d issued to BOTH account %d and account %d",
						inst.InstanceID, prev, accounts[i])
				}
				got[inst.InstanceID] = accounts[i]
				return nil
			})
			for i, err := range errs {
				if err != nil {
					t.Fatalf("goroutine %d: %v", i, err)
				}
			}

			// N accounts, N distinct instances.
			if len(got) != N {
				t.Errorf("distinct instances issued = %d, want %d — an instance went to two accounts", len(got), N)
			}
			if n := f.count(`SELECT count(*) FROM flag_issues WHERE challenge_id = $1`, ch); n != N {
				t.Errorf("flag_issues rows = %d, want %d", n, N)
			}
			if n := f.count(
				`SELECT count(*) FROM (SELECT instance_id FROM flag_issues GROUP BY instance_id HAVING count(*) > 1) x`,
			); n != 0 {
				t.Errorf("instances issued more than once = %d, want 0", n)
			}
		})
	}
}

// Concurrent first-views by the same account are idempotent.
//
// Five browser tabs must yield one instance, not five, and must not burn four out of the
// pool. This is the PRIMARY KEY (challenge_id, account_id) half of the invariant.
func TestFlagPool_SameAccount_ConcurrentFirstViews_AreIdempotent(t *testing.T) {
	f := setup(t, account.ModeUsers)
	ctx := testCtx(t)

	ch := f.seedChallenge(challengeSpec{Value: 500, FlagMode: "unique"})
	f.seedInstances(ch, 10)

	_, acct := f.seedUser("tabhoarder")

	ids := make([]int64, N)
	errs := race(N, func(i int) error {
		inst, err := f.svc.IssueInstance(ctx, ch, acct)
		ids[i] = inst.InstanceID
		return err
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	for i, id := range ids {
		if id != ids[0] {
			t.Fatalf("goroutine %d got instance %d, goroutine 0 got %d — the account was issued two",
				i, id, ids[0])
		}
	}
	if n := f.count(`SELECT count(*) FROM flag_issues WHERE challenge_id = $1`, ch); n != 1 {
		t.Errorf("flag_issues rows = %d, want 1", n)
	}
	// The other nine must be untouched: a burned pool is a dead challenge.
	if n := f.count(`SELECT count(*) FROM challenge_instances ci
                     WHERE ci.challenge_id = $1
                       AND NOT EXISTS (SELECT 1 FROM flag_issues fi WHERE fi.instance_id = ci.id)`, ch); n != 9 {
		t.Errorf("unissued instances remaining = %d, want 9 — concurrent tabs BURNED the pool", n)
	}
}

// C5c — pool exhaustion is a hard failure, never a fallback.
//
// A dry pool makes the challenge unavailable to that account. Quietly falling back to a
// shared static flag would silently destroy the uniqueness property for exactly the late
// registrants you are most suspicious of.
func TestFlagPool_Exhaustion_FailsLoudly(t *testing.T) {
	f := setup(t, account.ModeUsers)
	ctx := testCtx(t)

	ch := f.seedChallenge(challengeSpec{Value: 500, FlagMode: "unique"})
	const supply = N / 2
	f.seedInstances(ch, supply) // only half the accounts can be served

	accounts := make([]int64, N)
	for i := range N {
		_, accounts[i] = f.seedUser(fmt.Sprintf("late%03d", i))
	}

	errs := race(N, func(i int) error {
		_, err := f.svc.IssueInstance(ctx, ch, accounts[i])
		return err
	})

	served, exhausted := 0, 0
	for i, err := range errs {
		switch {
		case err == nil:
			served++
		case errors.Is(err, flags.ErrPoolExhausted):
			exhausted++
		default:
			t.Fatalf("goroutine %d: unexpected error: %v", i, err)
		}
	}

	// No more than the supply (a double-issue) and no fewer (an instance lost by a picker
	// that skipped a free row).
	if served != supply {
		t.Errorf("accounts served = %d, want %d (the pool size)", served, supply)
	}
	if exhausted != N-supply {
		t.Errorf("ErrPoolExhausted = %d, want %d", exhausted, N-supply)
	}
	if n := f.count(`SELECT count(*) FROM flag_issues WHERE challenge_id = $1`, ch); n != int64(supply) {
		t.Errorf("flag_issues rows = %d, want %d", n, supply)
	}
}

// C5d — a unique flag is attributed to the account it was issued to, even when someone
// else submits it.
//
// The shared flag is still accepted, in the ordinary words: a detector that announces
// itself is not a detector, and rejecting the flag would teach the cheater that the
// platform tracks provenance. The row surfaces in the review queue and a human decides.
func TestFlagPool_SharedFlag_IsAcceptedAndAttributedSilently(t *testing.T) {
	f := setup(t, account.ModeUsers)
	ctx := testCtx(t)

	ch := f.seedChallenge(challengeSpec{Value: 500, FlagMode: "unique"})
	pool := f.seedInstances(ch, 4)

	_, ownerAcct := f.seedUser("owner")
	cheater, _ := f.seedUser("cheater")

	// Issue the owner an instance, then work out which plaintext flag is theirs.
	inst, err := f.svc.IssueInstance(ctx, ch, ownerAcct)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	var ownerFlag string
	for _, flag := range pool {
		var id int64
		scanErr := f.pool.QueryRow(ctx,
			`SELECT id FROM challenge_instances WHERE challenge_id = $1 AND value_hash = sha256($2::bytea)`,
			ch, []byte(flag)).Scan(&id)
		if scanErr == nil && id == inst.InstanceID {
			ownerFlag = flag
			break
		}
	}
	if ownerFlag == "" {
		t.Fatal("could not identify the owner's flag")
	}

	res, err := f.svc.Submit(ctx, gameplay.SubmitInput{
		ChallengeID: ch, Actor: cheater, Provided: ownerFlag,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	if res.Status != gameplay.StatusCorrect {
		t.Errorf("status = %s, want correct — we do NOT reject a shared flag", res.Status)
	}

	// The evidence is stamped on the submission, not joined at query time.
	var attributed *int64
	err = f.pool.QueryRow(ctx,
		`SELECT attributed_account_id FROM submissions WHERE challenge_id = $1 AND user_id = $2`,
		ch, cheater.UserID).Scan(&attributed)
	if err != nil {
		t.Fatalf("read attribution: %v", err)
	}
	if attributed == nil {
		t.Fatal("attributed_account_id is NULL — the sharing evidence was not stamped")
	}
	if *attributed != ownerAcct {
		t.Errorf("attributed_account_id = %d, want %d (the owner)", *attributed, ownerAcct)
	}
	if *attributed == cheater.UserID {
		t.Error("the flag was attributed to the submitter — attribution is meaningless")
	}

	// The sharing detector reads the stamp alone — no join to flag_issues.
	if n := f.count(`SELECT count(*) FROM submissions sub CROSS JOIN instance i
                     WHERE sub.type = 'correct'
                       AND sub.attributed_account_id IS NOT NULL
                       AND sub.attributed_account_id IS DISTINCT FROM
                           (CASE WHEN i.user_mode = 'teams' THEN sub.team_id ELSE sub.user_id END)`); n != 1 {
		t.Errorf("flag-sharing detections = %d, want 1", n)
	}
}
