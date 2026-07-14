package gameplay

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/domain/prereq"
)

// Unlock is a purchased hint.
type Unlock struct {
	HintID  int64
	Content string
	// Charged is the positive cost; the awards row carries it negated.
	Charged int32
	// Score is the balance after the charge.
	Score int64
}

// UnlockHint spends points on a hint.
//
// There are two races here and the unique index only closes one. UNIQUE(hint_id,
// account) stops the same hint being bought twice; it says nothing about buying three
// different hints concurrently with points for one. That needs the account's spends
// serialized and the balance read fresh under that lock.
func (s *Service) UnlockHint(ctx context.Context, challengeID, hintID int64, actor Actor) (Unlock, error) {
	accountID, err := actor.AccountID(s.mode)
	if err != nil {
		return Unlock{}, fmt.Errorf("gameplay: unlock hint: %w", err)
	}

	tx, err := s.pool.BeginTx(ctx, txOpts)
	if err != nil {
		return Unlock{}, fmt.Errorf("gameplay: unlock hint: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := s.q.WithTx(tx)

	hint, err := q.GetHint(ctx, db.GetHintParams{HintID: hintID, ChallengeID: challengeID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Unlock{}, fmt.Errorf("gameplay: unlock hint: %w: id=%d challenge=%d", ErrHintNotFound, hintID, challengeID)
	} else if err != nil {
		return Unlock{}, fmt.Errorf("gameplay: unlock hint: get hint %d: %w", hintID, err)
	}

	// Prerequisite gate: a hint whose prerequisite hints this account has not all unlocked cannot
	// be purchased. Checked before the account lock and the charge — rejecting a locked hint must
	// not spend a point.
	reqs, err := prereq.Parse(hint.Requirements)
	if err != nil {
		return Unlock{}, fmt.Errorf("gameplay: unlock hint: parse requirements (hint %d): %w", hintID, err)
	}
	if met, metErr := s.hintPrereqsMet(ctx, q, actor, reqs); metErr != nil {
		return Unlock{}, fmt.Errorf("gameplay: unlock hint %d: %w", hintID, metErr)
	} else if !met {
		return Unlock{}, fmt.Errorf("gameplay: unlock hint %d: %w", hintID, ErrHintLocked)
	}

	// Lock the account, not the hint: the invariant is about the balance, and the
	// balance belongs to the account.
	if lockErr := s.lockAccount(ctx, q, actor, accountID); lockErr != nil {
		return Unlock{}, lockErr
	}

	// Ownership is checked before affordability, and under the lock. An account that
	// owns this hint but has since spent down to zero must be told it owns the hint,
	// not that it is broke. The ON CONFLICT below still arbitrates.
	if _, ownedErr := q.GetHintUnlock(ctx, db.GetHintUnlockParams{
		HintID: hint.ID,
		UserID: actor.UserID,
		TeamID: actor.TeamID,
	}); ownedErr == nil {
		return Unlock{}, fmt.Errorf("gameplay: unlock hint %d: %w", hintID, ErrAlreadyUnlocked)
	} else if !errors.Is(ownedErr, pgx.ErrNoRows) {
		return Unlock{}, fmt.Errorf("gameplay: unlock hint: read existing unlock: %w", ownedErr)
	}

	score, err := q.GetAccountScore(ctx, db.GetAccountScoreParams{
		UserID: actor.UserID,
		TeamID: actor.TeamID,
	})
	if err != nil {
		return Unlock{}, fmt.Errorf("gameplay: unlock hint: read score: %w", err)
	}

	if score < int64(hint.Cost) {
		return Unlock{}, fmt.Errorf("gameplay: unlock hint %d: %w: score %d < cost %d",
			hintID, ErrInsufficientScore, score, hint.Cost)
	}

	// A spend is a negative award, so the score stays one SUM over one ledger.
	award, err := q.InsertAward(ctx, db.InsertAwardParams{
		UserID:      actor.UserID,
		TeamID:      actor.TeamID,
		Type:        "hint_unlock",
		ChallengeID: &hint.ChallengeID,
		Name:        hintName(hint),
		Value:       -hint.Cost,
	})
	if err != nil {
		return Unlock{}, fmt.Errorf("gameplay: unlock hint: insert charge: %w", err)
	}

	if _, unlockErr := q.InsertHintUnlock(ctx, db.InsertHintUnlockParams{
		HintID:  hint.ID,
		UserID:  actor.UserID,
		TeamID:  actor.TeamID,
		AwardID: award.ID,
	}); errors.Is(unlockErr, pgx.ErrNoRows) {
		// Zero rows means already unlocked. Returning here rolls back, discarding the
		// award above — otherwise we would charge for a hint the account already owns.
		return Unlock{}, fmt.Errorf("gameplay: unlock hint %d: %w", hintID, ErrAlreadyUnlocked)
	} else if unlockErr != nil {
		return Unlock{}, fmt.Errorf("gameplay: unlock hint: insert unlock: %w", unlockErr)
	}

	if err := tx.Commit(ctx); err != nil {
		return Unlock{}, fmt.Errorf("gameplay: unlock hint: commit: %w", err)
	}

	return Unlock{
		HintID:  hint.ID,
		Content: hint.Content,
		Charged: hint.Cost,
		Score:   score - int64(hint.Cost),
	}, nil
}

// lockAccount takes the spend lock on whichever table the mode points at. The mode
// picks the table, and a table cannot be a bind parameter — hence two queries.
func (s *Service) lockAccount(ctx context.Context, q *db.Queries, actor Actor, id account.ID) error {
	var err error
	switch s.mode {
	case account.ModeTeams:
		_, err = q.LockTeamForSpend(ctx, int64(id))
	case account.ModeUsers:
		_, err = q.LockUserForSpend(ctx, actor.UserID)
	default:
		return fmt.Errorf("gameplay: unlock hint: %w: %s", account.ErrUnknownMode, s.mode)
	}

	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("gameplay: unlock hint: account %d not found", id)
	} else if err != nil {
		return fmt.Errorf("gameplay: unlock hint: lock account %d: %w", id, err)
	}
	return nil
}

// awards.name is NOT NULL and hints.title is not, so fall back to something stable.
func hintName(h db.Hint) string {
	if h.Title != nil && *h.Title != "" {
		return "Hint: " + *h.Title
	}
	return fmt.Sprintf("Hint #%d", h.ID)
}
