package accounts

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/starvy/flagfish/internal/db"
)

var (
	ErrEmailTaken = errors.New("accounts: email is already registered")
	ErrCapReached = errors.New("accounts: registration is full")
)

// Register creates a user and mints a session. The unique index and the caps trigger reject a
// duplicate or over-cap INSERT itself — a prior SELECT would race. An unverified registration
// enqueues its verification mail on the same transaction: either both happen or neither.
func (s *Service) Register(ctx context.Context, name, email, password string, verified bool, ctfName string) (Session, error) {
	hash, err := Hash(password)
	if err != nil {
		return Session{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("accounts: register: %w", err)
	}
	defer tx.Rollback(ctx)

	row, err := s.q.WithTx(tx).CreateUser(ctx, db.CreateUserParams{
		Name:         name,
		Email:        email,
		PasswordHash: &hash,
		Verified:     verified,
	})
	if err != nil {
		// Match the constraint by name, not just the class: any other check constraint on
		// users must surface as an error, not masquerade as "registration is full".
		var pg *pgconn.PgError
		if errors.As(err, &pg) {
			switch {
			case pg.Code == pgerrcode.UniqueViolation && pg.ConstraintName == "users_email_uniq":
				return Session{}, ErrEmailTaken
			case pg.Code == pgerrcode.CheckViolation && pg.ConstraintName == "users_caps":
				return Session{}, ErrCapReached
			}
		}
		return Session{}, fmt.Errorf("accounts: register: %w", err)
	}

	if !verified {
		if err := s.enqueueVerification(ctx, tx, row.ID, email, ctfName); err != nil {
			return Session{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("accounts: register: %w", err)
	}

	return s.mintSession(ctx, row.ID, hash)
}
