package gameplay

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/flags"
	"github.com/starvy/flagfish/internal/domain/prereq"
	"github.com/starvy/flagfish/internal/jobs"
)

// SubmitInput is one flag submission.
type SubmitInput struct {
	ChallengeID int64
	Actor       Actor
	Provided    string
}

// Result is the outcome of a submission.
type Result struct {
	Status     Status
	FirstBlood bool
	// Value is the price snapshotted under the lock at solve time.
	Value int32
}

// Submit is the hot path: flag compare, solve insert, first-blood detection and the
// announcement enqueue, in one transaction.
//
// The challenge lock is taken lazily, only once the flag has compared correct. Roughly
// 99% of submissions are wrong, and locking at the top would serialize every wrong
// guess on the hottest challenge through a single row.
func (s *Service) Submit(ctx context.Context, in SubmitInput) (Result, error) {
	// A teamless user in teams mode has no account to attribute a solve to. The resolved
	// account keys the max_attempts lock below, matching the column the count scopes on.
	acct, err := in.Actor.AccountID(s.mode)
	if err != nil {
		return Result{}, fmt.Errorf("gameplay: submit: %w", err)
	}

	tx, err := s.pool.BeginTx(ctx, txOpts)
	if err != nil {
		return Result{}, fmt.Errorf("gameplay: submit: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := s.q.WithTx(tx)

	ch, err := q.GetChallenge(ctx, in.ChallengeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, fmt.Errorf("gameplay: submit: %w: id=%d", ErrChallengeNotFound, in.ChallengeID)
	} else if err != nil {
		return Result{}, fmt.Errorf("gameplay: submit: get challenge %d: %w", in.ChallengeID, err)
	}

	// Prerequisite gate, on the no-lock path: a challenge whose prerequisites this account has not
	// all solved is not playable. A visible-but-locked challenge is rejected as locked; one the
	// anonymize flag keeps hidden is reported as not found, so its existence never leaks.
	reqs, err := prereq.Parse(ch.Requirements)
	if err != nil {
		return Result{}, fmt.Errorf("gameplay: submit: parse requirements (challenge %d): %w", ch.ID, err)
	}
	if met, metErr := s.challengePrereqsMet(ctx, q, in.Actor, reqs); metErr != nil {
		return Result{}, fmt.Errorf("gameplay: submit: challenge %d: %w", ch.ID, metErr)
	} else if !met {
		if reqs.Visibility.Visible() {
			return Result{}, fmt.Errorf("gameplay: submit: %w: id=%d", ErrChallengeLocked, ch.ID)
		}
		return Result{}, fmt.Errorf("gameplay: submit: %w: id=%d (prerequisites unmet)", ErrChallengeNotFound, ch.ID)
	}

	if ch.MaxAttempts > 0 {
		// Serialize this account's own concurrent submissions to this challenge so the count
		// below is exact — without it, they all read the same stale count and every one slips
		// past the cap, and a scripted client brute-forces the flag. Per (challenge, account),
		// so different players never contend: this is not the challenge-row lock, and the
		// wrong-answer hot path across players is untouched.
		if lockErr := q.LockAttemptCounter(ctx, db.LockAttemptCounterParams{
			ChallengeID: ch.ID,
			AccountID:   int64(acct),
		}); lockErr != nil {
			return Result{}, fmt.Errorf("gameplay: submit: lock attempt counter: %w", lockErr)
		}
		wrong, cntErr := q.CountIncorrectSubmissions(ctx, db.CountIncorrectSubmissionsParams{
			ChallengeID: ch.ID,
			UserID:      in.Actor.UserID,
			TeamID:      in.Actor.TeamID,
		})
		if cntErr != nil {
			return Result{}, fmt.Errorf("gameplay: submit: count wrong attempts: %w", cntErr)
		}
		if wrong >= int64(ch.MaxAttempts) {
			return Result{}, fmt.Errorf("gameplay: submit: challenge %d: %w", ch.ID, ErrNoAttemptsRemaining)
		}
	}

	match, err := s.check(ctx, q, &ch, in.Provided)
	if err != nil {
		// A corrupt flag row (e.g. a regex that does not compile) must never be
		// laundered into "incorrect" — that tells a player their correct flag is wrong,
		// silently, for the whole event.
		return Result{}, fmt.Errorf("gameplay: submit: check flag (challenge %d): %w", ch.ID, err)
	}

	if !match.Correct {
		if _, insErr := q.InsertSubmission(ctx, db.InsertSubmissionParams{
			ChallengeID: ch.ID,
			UserID:      in.Actor.UserID,
			TeamID:      in.Actor.TeamID,
			Type:        "incorrect",
			Provided:    in.Provided,
			Ip:          in.Actor.IP,
		}); insErr != nil {
			return Result{}, fmt.Errorf("gameplay: submit: insert incorrect submission: %w", insErr)
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return Result{}, fmt.Errorf("gameplay: submit: commit incorrect: %w", commitErr)
		}
		return Result{Status: StatusIncorrect}, nil
	}

	// Correct path only, from here.
	//
	// The lock is FOR NO KEY UPDATE, not FOR UPDATE: the FK inserts below take FOR KEY
	// SHARE on this row, and FOR UPDATE would block them.
	locked, err := q.LockChallengeForSubmit(ctx, ch.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, fmt.Errorf("gameplay: submit: %w: id=%d (deleted or hidden mid-submit)", ErrChallengeNotFound, ch.ID)
	} else if err != nil {
		return Result{}, fmt.Errorf("gameplay: submit: lock challenge %d: %w", ch.ID, err)
	}

	sub, err := q.InsertSubmission(ctx, db.InsertSubmissionParams{
		ChallengeID: locked.ID,
		UserID:      in.Actor.UserID,
		TeamID:      in.Actor.TeamID,
		Type:        "correct",
		Provided:    in.Provided,
		Ip:          in.Actor.IP,
		// Stamped here, never joined at query time, so sharing detection survives the
		// instance being rotated or deleted.
		AttributedAccountID: match.AttributedAccountID,
	})
	if err != nil {
		return Result{}, fmt.Errorf("gameplay: submit: insert correct submission: %w", err)
	}

	// Value comes from the locked row, not the earlier unlocked read: another solver may
	// have decayed the challenge in between.
	solve, err := q.InsertSolve(ctx, db.InsertSolveParams{
		SubmissionID: &sub.ID,
		ChallengeID:  locked.ID,
		UserID:       in.Actor.UserID,
		TeamID:       in.Actor.TeamID,
		Value:        locked.Value,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Zero rows from ON CONFLICT DO NOTHING is the already-solved signal. We still
		// commit: the correct-submission row is real, and on unique-flag challenges it is
		// the evidence that a flag was shared.
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return Result{}, fmt.Errorf("gameplay: submit: commit already-solved: %w", commitErr)
		}
		return Result{Status: StatusAlreadySolved}, nil
	} else if err != nil {
		return Result{}, fmt.Errorf("gameplay: submit: insert solve: %w", err)
	}

	firstBlood := false
	if locked.FirstBlood != "none" {
		counts, countErr := q.CountSolvesExcluding(ctx, db.CountSolvesExcludingParams{
			ChallengeID:    locked.ID,
			ExcludeSolveID: solve.ID,
			UserID:         in.Actor.UserID,
			TeamID:         in.Actor.TeamID,
		})
		if countErr != nil {
			return Result{}, fmt.Errorf("gameplay: submit: count prior solves: %w", countErr)
		}
		// Both halves are needed: no visible solve came before us, AND we are visible
		// ourselves. Without the second, a hidden admin's count is 0 by construction and
		// they take the blood.
		firstBlood = counts.PriorSolves == 0 && counts.ActorEligible
	}

	if firstBlood {
		if locked.FirstBlood == "bonus" {
			if locked.FirstBloodBonus == nil {
				return Result{}, fmt.Errorf(
					"gameplay: submit: challenge %d has first_blood='bonus' but no bonus value", locked.ID,
				)
			}
			if _, err := q.InsertAward(ctx, db.InsertAwardParams{
				UserID:      in.Actor.UserID,
				TeamID:      in.Actor.TeamID,
				Type:        "first_blood",
				ChallengeID: &locked.ID,
				Name:        "First blood: " + locked.Name,
				Value:       *locked.FirstBloodBonus,
			}); err != nil {
				return Result{}, fmt.Errorf("gameplay: submit: insert first-blood award: %w", err)
			}
		}

		// Enqueued on our transaction, so a rolled-back solve un-enqueues its
		// announcement. The worker checks the freeze at send time, not here.
		if _, err := s.jobs.InsertTx(ctx, tx, jobs.AnnounceFirstBlood{
			ChallengeID:   locked.ID,
			ChallengeName: locked.Name,
			UserID:        in.Actor.UserID,
			TeamID:        in.Actor.TeamID,
			SolveID:       solve.ID,
			SolvedAt:      solve.Date.Time,
		}, nil); err != nil {
			return Result{}, fmt.Errorf("gameplay: submit: enqueue first-blood announcement: %w", err)
		}
	}

	// One statement, exact because we hold the lock. Never a read-modify-write.
	if locked.Function != "static" {
		if err := q.RecalcChallengeValue(ctx, locked.ID); err != nil {
			return Result{}, fmt.Errorf("gameplay: submit: recalc challenge value: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("gameplay: submit: commit correct: %w", err)
	}

	return Result{Status: StatusCorrect, FirstBlood: firstBlood, Value: solve.Value}, nil
}

type match struct {
	Correct bool
	// AttributedAccountID is the account the matched flag was issued to — nil unless
	// flag_mode='unique'. Not necessarily the submitter; that difference is the
	// anti-cheat signal.
	AttributedAccountID *int64
}

// check performs no writes and takes no locks, which is what makes the lazy lock
// possible.
func (s *Service) check(ctx context.Context, q *db.Queries, ch *db.Challenge, provided string) (match, error) {
	mode, err := flags.ParseMode(ch.FlagMode)
	if err != nil {
		return match{}, err
	}

	switch mode {
	case flags.ModeStatic:
		return checkStatic(ctx, q, ch, provided)
	case flags.ModeUnique:
		return checkUnique(ctx, q, ch, provided)
	default:
		return match{}, fmt.Errorf("gameplay: unhandled flag mode %s", mode)
	}
}

func checkStatic(ctx context.Context, q *db.Queries, ch *db.Challenge, provided string) (match, error) {
	rows, err := q.GetChallengeFlags(ctx, ch.ID)
	if err != nil {
		return match{}, fmt.Errorf("load flags: %w", err)
	}
	if len(rows) == 0 {
		// Reporting every attempt as incorrect would hide the authoring error for the
		// whole event — players would just think the challenge is hard.
		return match{}, fmt.Errorf("challenge %d has no flags", ch.ID)
	}

	fs := make([]flags.Flag, 0, len(rows))
	for _, r := range rows {
		t, parseErr := flags.ParseType(r.Type)
		if parseErr != nil {
			return match{}, fmt.Errorf("flag %d: %w", r.ID, parseErr)
		}
		fs = append(fs, flags.Flag{Type: t, Content: r.Content, CaseInsensitive: r.CaseInsensitive})
	}

	// Both folds run on this pre-lock, no-write leg and swap one pure in-memory
	// comparison for another: no new query, no branch below the challenge lock, so
	// the lazy-lock property the wrong-answer path rests on is untouched. A corrupt
	// logic value is a hard error, like an unparseable flag_mode above — never a
	// silent fall back to 'any', which would quietly weaken an 'all' challenge.
	logic, err := flags.ParseLogic(ch.Logic)
	if err != nil {
		return match{}, err
	}
	var ok bool
	if logic == flags.LogicAll {
		ok, err = flags.MatchAll(fs, provided)
	} else {
		ok, err = flags.MatchAny(fs, provided)
	}
	if err != nil {
		return match{}, err
	}
	return match{Correct: ok}, nil
}

// checkUnique probes the instance pool by hash: one indexed lookup, O(1) in accounts,
// and no byte-wise comparison to leak timing.
//
// A flag issued to another account is still accepted as correct, silently: rejecting it
// would teach the cheater that we track provenance. It is merely stamped for later
// admin review.
func checkUnique(ctx context.Context, q *db.Queries, ch *db.Challenge, provided string) (match, error) {
	h := flags.Hash(provided)

	row, err := q.LookupInstanceByHash(ctx, db.LookupInstanceByHashParams{
		ChallengeID: ch.ID,
		ValueHash:   h[:],
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return match{Correct: false}, nil
	} else if err != nil {
		return match{}, fmt.Errorf("probe instance pool: %w", err)
	}

	// A hit with a NULL account is a valid pool flag that was never issued: correct, and
	// itself worth reviewing.
	return match{Correct: true, AttributedAccountID: row.AttributedAccountID}, nil
}
