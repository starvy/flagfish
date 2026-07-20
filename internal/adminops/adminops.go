// Package adminops is the write side of the admin API: challenge lifecycle, flags, hints, and user
// administration. Every mutation runs in one transaction with the acting admin stamped on it, so
// the audit triggers attribute each row change to a person — the audit row and the change commit
// or roll back together.
package adminops

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/db"
)

var (
	ErrChallengeNotFound = errors.New("adminops: challenge not found")
	ErrFlagNotFound      = errors.New("adminops: flag not found")
	ErrHintNotFound      = errors.New("adminops: hint not found")
	ErrUserNotFound      = errors.New("adminops: user not found")
	ErrTeamNotFound      = errors.New("adminops: team not found")
	ErrBracketNotFound   = errors.New("adminops: bracket not found")
	ErrAccountNotFound   = errors.New("adminops: account not found")

	// ErrChallengeHasSolves refuses a delete that would destroy scoreboard history.
	ErrChallengeHasSolves = errors.New("adminops: challenge has solves")
	// ErrChallengeHasHistory refuses a delete that would erase recorded attempts, awards or hint
	// unlocks. Attempts are anticheat evidence even when every one of them was wrong.
	ErrChallengeHasHistory = errors.New("adminops: challenge has recorded history")
	// ErrChallengeInUse refuses a delete blocked by other gameplay evidence (issued unique flags).
	ErrChallengeInUse = errors.New("adminops: challenge is in use")
	// ErrHintUnlocked refuses deleting a hint somebody has paid for.
	ErrHintUnlocked = errors.New("adminops: hint has been unlocked")

	ErrLastAdmin = errors.New("adminops: cannot demote the last admin")
	ErrSelfBan   = errors.New("adminops: cannot ban yourself")
	// ErrSelfTeamBan refuses banning a team the acting admin is on: like ErrSelfBan, it is what
	// guarantees a ban can never leave the instance without a usable admin.
	ErrSelfTeamBan = errors.New("adminops: cannot ban your own team")

	ErrTeamNameTaken  = errors.New("adminops: team name is already taken")
	ErrTeamEmailTaken = errors.New("adminops: team email is already in use")
	ErrTeamCapReached = errors.New("adminops: the num_teams cap is reached")

	ErrAwardNotFound = errors.New("adminops: award not found")
	// ErrAwardNotManual refuses revoking a hint_unlock or first_blood award: those are gameplay
	// facts stamped under the challenge lock, not admin adjustments, and never leave the ledger.
	ErrAwardNotManual = errors.New("adminops: award is not a manual adjustment")

	ErrTagNotFound = errors.New("adminops: tag not found")
	// ErrTagInUse refuses an unforced delete of a tag still attached to challenges, so a mistyped
	// delete cannot silently strip a tag off the whole board.
	ErrTagInUse = errors.New("adminops: tag is in use")
	// ErrTagAlreadyAttached is the UNIQUE(challenge_id, value) refusal of a duplicate attach.
	ErrTagAlreadyAttached = errors.New("adminops: tag already attached")
)

// A ValidationError is operator input this service refused. Its Reason is written for the
// operator and safe to put on the wire; it never carries database error text.
type ValidationError struct {
	Reason string
}

func (e *ValidationError) Error() string { return "adminops: invalid: " + e.Reason }

func invalidf(format string, args ...any) error {
	return &ValidationError{Reason: fmt.Sprintf(format, args...)}
}

var Module = fx.Module("adminops", fx.Provide(New))

type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool, q: db.New(pool)} }

// tx runs fn in one transaction with the actor stamped for the audit triggers.
func (s *Service) tx(ctx context.Context, actor audit.Actor, fn func(tx pgx.Tx, q *db.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("adminops: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := audit.Stamp(ctx, tx, actor); err != nil {
		return fmt.Errorf("adminops: %w", err)
	}
	if err := fn(tx, s.q.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("adminops: commit: %w", err)
	}
	return nil
}
