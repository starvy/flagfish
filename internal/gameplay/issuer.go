package gameplay

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/flags"
)

// IssuedInstance is an account's bundle for a unique-flag challenge.
//
// There is deliberately no flag field: only the sha256 of the plaintext is ever stored,
// so leaking it through this type is unrepresentable rather than merely prevented.
type IssuedInstance struct {
	InstanceID int64
	ArtifactID *int64
	Vars       []byte
	Generation int32
}

// IssueInstance assigns a pool instance to an account, idempotently. It runs on first
// access to the challenge — the one place where reading mutates state.
//
// Safety is the constraints', not this function's: PK(challenge_id, account_id) makes it
// idempotent, UNIQUE(instance_id) makes double-issue impossible, and SKIP LOCKED in the
// query makes concurrent viewers pick different rows.
func (s *Service) IssueInstance(ctx context.Context, challengeID, accountID int64) (IssuedInstance, error) {
	tx, err := s.pool.BeginTx(ctx, txOpts)
	if err != nil {
		return IssuedInstance{}, fmt.Errorf("gameplay: issue instance: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	inst, err := issueTx(ctx, s.q.WithTx(tx), challengeID, accountID)
	if err != nil {
		return IssuedInstance{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return IssuedInstance{}, fmt.Errorf("gameplay: issue instance: commit: %w", err)
	}
	return inst, nil
}

func issueTx(ctx context.Context, q *db.Queries, challengeID, accountID int64) (IssuedInstance, error) {
	got, err := q.GetIssuedInstance(ctx, db.GetIssuedInstanceParams{
		ChallengeID: challengeID,
		AccountID:   accountID,
	})
	if err == nil {
		return toIssued(got), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return IssuedInstance{}, fmt.Errorf("gameplay: issue instance: read existing: %w", err)
	}

	// Must be its own statement, before AssignInstance. Under READ COMMITTED a statement's
	// snapshot is taken at statement start, so a lock taken inside the pick would still
	// read a stale snapshot and miss instances other transactions just claimed.
	if lockErr := q.LockChallengePool(ctx, challengeID); lockErr != nil {
		return IssuedInstance{}, fmt.Errorf("gameplay: issue instance: lock pool: %w", lockErr)
	}

	if _, assignErr := q.AssignInstance(ctx, db.AssignInstanceParams{
		ChallengeID: challengeID,
		AccountID:   accountID,
	}); assignErr != nil && !errors.Is(assignErr, pgx.ErrNoRows) {
		return IssuedInstance{}, fmt.Errorf("gameplay: issue instance: assign: %w", assignErr)
	}

	// Zero rows from AssignInstance is ambiguous: either a concurrent first-view won the
	// PK race (re-read finds their row — idempotent and correct), or the pool is dry.
	// Re-reading is what tells them apart.
	got, err = q.GetIssuedInstance(ctx, db.GetIssuedInstanceParams{
		ChallengeID: challengeID,
		AccountID:   accountID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Never fall back to a shared flag: that would silently destroy uniqueness for
		// exactly the late registrants you are most suspicious of.
		return IssuedInstance{}, fmt.Errorf("gameplay: challenge %d: %w", challengeID, flags.ErrPoolExhausted)
	} else if err != nil {
		return IssuedInstance{}, fmt.Errorf("gameplay: issue instance: read after assign: %w", err)
	}

	return toIssued(got), nil
}

func toIssued(r db.GetIssuedInstanceRow) IssuedInstance {
	return IssuedInstance{
		InstanceID: r.ID,
		ArtifactID: r.ArtifactID,
		Vars:       r.Vars,
		Generation: r.Generation,
	}
}
