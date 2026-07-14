// Package gameplay is the submit hot path and the hint-unlock spend.
//
// The invariants it upholds — one solve per account, one first blood, exact decay,
// no negative scores — live in the database constraints, not in this code. This
// package composes them.
package gameplay

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/account"
)

// Both hot-path transactions are pinned to READ COMMITTED deliberately: under stronger
// isolation a row lock on a concurrently updated row raises 40001 instead of waiting,
// which would turn every contended submit into a rejected flag.
var txOpts = pgx.TxOptions{IsoLevel: pgx.ReadCommitted}

// JobInserter enqueues a job on the caller's transaction, so the enqueue commits or
// rolls back with the work that justified it. *river.Client[pgx.Tx] satisfies it.
type JobInserter interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// Service is the gameplay service.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	jobs JobInserter
	mode account.Mode
}

// New builds the service. The account mode is fixed at setup, not read per request.
func New(pool *pgxpool.Pool, jobs JobInserter, mode account.Mode) *Service {
	return &Service{pool: pool, q: db.New(pool), jobs: jobs, mode: mode}
}

// Mode reports the account mode the service plays under.
func (s *Service) Mode() account.Mode { return s.mode }

// Actor is the authenticated submitter. Both ids are always written to gameplay rows;
// the mode decides which one is read.
type Actor struct {
	UserID int64
	TeamID *int64
	IP     *netip.Addr
}

// AccountID collapses the actor to the account that plays the game.
func (a Actor) AccountID(mode account.Mode) (account.ID, error) {
	return mode.Resolve(account.Membership{UserID: a.UserID, TeamID: a.TeamID})
}

// Status is the outcome of a submission.
type Status uint8

const (
	StatusIncorrect Status = iota
	StatusCorrect
	// StatusAlreadySolved comes from the UNIQUE constraint — zero rows out of
	// ON CONFLICT DO NOTHING — never from a SELECT.
	StatusAlreadySolved
)

var statusNames = map[Status]string{
	StatusIncorrect:     "incorrect",
	StatusCorrect:       "correct",
	StatusAlreadySolved: "already_solved",
}

// String returns the wire name of the status.
func (s Status) String() string {
	if n, ok := statusNames[s]; ok {
		return n
	}
	return fmt.Sprintf("Status(%d)", uint8(s))
}

// Errors returned by the gameplay service.
var (
	ErrChallengeNotFound = errors.New("gameplay: challenge not found")
	// ErrChallengeLocked rejects a submission against a visible-but-locked challenge whose
	// prerequisites this account has not solved. A locked challenge whose prerequisites hide it
	// reports ErrChallengeNotFound instead, so its existence is never disclosed.
	ErrChallengeLocked = errors.New("gameplay: challenge is locked by unmet prerequisites")
	// ErrNoAttemptsRemaining rejects a submission before the flag is even compared.
	ErrNoAttemptsRemaining = errors.New("gameplay: no attempts remaining for this challenge")
	ErrHintNotFound        = errors.New("gameplay: hint not found")
	// ErrHintLocked rejects a hint unlock whose prerequisite hints are not all unlocked.
	ErrHintLocked        = errors.New("gameplay: hint is locked by unmet prerequisites")
	ErrInsufficientScore = errors.New("gameplay: insufficient score to unlock this hint")
	ErrAlreadyUnlocked   = errors.New("gameplay: hint is already unlocked by this account")
)
