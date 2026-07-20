//go:build integration

package concurrency

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/gameplay"
)

func isSubmissionFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == "submissions_challenge_id_fkey"
}

// TestDeleteChallengeVsWrongSubmits races one admin DeleteChallenge against N wrong-answer
// submissions. There is no pre-check on the delete — the FK is the check — so the outcome must be
// one of exactly two: the delete wins and no submission references the vanished challenge, or a
// committed attempt makes it refuse with the has-history error. Nothing may land in between.
func TestDeleteChallengeVsWrongSubmits(t *testing.T) {
	f := setup(t, account.ModeUsers)
	ctx := testCtx(t)

	chID := f.seedChallenge(challengeSpec{Value: 100, Flag: "flagfish{right}"})
	admin, _ := f.seedUser("admin")
	actors := make([]gameplay.Actor, N)
	for i := range N {
		actors[i], _ = f.seedUser(fmt.Sprintf("player%d", i))
	}
	ops := adminops.New(f.pool)

	// Slot 0 deletes; the rest submit wrong answers.
	errs := race(N+1, func(i int) error {
		if i == 0 {
			return ops.DeleteChallenge(ctx, audit.Actor{ID: admin.UserID}, chID)
		}
		_, err := f.svc.Submit(ctx, gameplay.SubmitInput{
			ChallengeID: chID, Actor: actors[i-1], Provided: "flag{wrong}",
		})
		return err
	})

	// Every submit either committed, or lost to the delete loudly: the challenge was gone before
	// the read (not found), the delete's cascade took the flags between the challenge read and the
	// flag check (no flags), or the FK refused the insert mid-flight. Each shape is only possible
	// after the delete committed. Silence is the only failure.
	committed, lost := 0, 0
	for _, err := range errs[1:] {
		switch {
		case err == nil:
			committed++
		case errors.Is(err, gameplay.ErrChallengeNotFound),
			strings.Contains(err.Error(), "has no flags"),
			isSubmissionFKViolation(err):
			lost++
		default:
			t.Fatalf("submit failed for a reason other than losing the race: %v", err)
		}
	}

	challenges := f.count(`SELECT count(*) FROM challenges WHERE id = $1`, chID)
	attempts := f.count(`SELECT count(*) FROM submissions WHERE challenge_id = $1`, chID)

	switch deleteErr := errs[0]; {
	case deleteErr == nil:
		if challenges != 0 {
			t.Errorf("delete reported success but the challenge row survived")
		}
		if attempts != 0 || committed != 0 {
			t.Errorf("delete won yet %d submission(s) committed (%d rows) — history erased or dangling", committed, attempts)
		}
	case errors.Is(deleteErr, adminops.ErrChallengeHasHistory):
		if challenges != 1 {
			t.Errorf("delete was refused but the challenge row is gone")
		}
		if lost != 0 {
			t.Errorf("delete was refused yet %d submit(s) saw its effects", lost)
		}
		if attempts == 0 {
			t.Errorf("delete refused with has-history but no submission exists")
		}
		if attempts != int64(committed) {
			t.Errorf("submission rows (%d) disagree with successful submits (%d)", attempts, committed)
		}
	default:
		t.Fatalf("delete failed for a reason other than recorded history: %v", deleteErr)
	}
}
