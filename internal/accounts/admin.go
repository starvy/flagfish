package accounts

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/starvy/flagfish/internal/db"
)

// ErrNoSuchUser is returned when a promotion targets an address that no account holds.
var ErrNoSuchUser = errors.New("accounts: no user with that email")

// CreateAdmin inserts a verified admin and returns its id. It is the console-only escape from
// the bootstrap deadlock: role='admin' is grantable only through the admin API, which itself
// requires an admin, so the first one cannot be made that way.
//
// The insert runs with the account-caps trigger suppressed for the transaction, the same
// exemption the importer runs under, so a full instance — one already at its num_users cap —
// still cannot lock its own first admin out. Suppressing the trigger also skips the audit
// capture for this one row, which is acceptable: the row's existence is the bootstrap record.
func (s *Service) CreateAdmin(ctx context.Context, name, email, password string) (int64, error) {
	hash, err := Hash(password)
	if err != nil {
		return 0, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("accounts: create admin: %w", err)
	}
	defer tx.Rollback(ctx)

	// SET LOCAL scopes the change to this transaction; replica role disables the BEFORE INSERT
	// caps trigger so the cap cannot reject the bootstrap admin. Requires a superuser role,
	// which the importer already assumes.
	if _, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica"); err != nil {
		return 0, fmt.Errorf("accounts: create admin: suppress caps trigger: %w", err)
	}

	id, err := s.q.WithTx(tx).CreateAdmin(ctx, db.CreateAdminParams{
		Name:         name,
		Email:        email,
		PasswordHash: &hash,
	})
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == pgerrcode.UniqueViolation && pg.ConstraintName == "users_email_uniq" {
			return 0, ErrEmailTaken
		}
		return 0, fmt.Errorf("accounts: create admin: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("accounts: create admin: %w", err)
	}
	return id, nil
}

// PromoteToAdmin elevates an existing account to a verified admin, by email. It reports whether
// the account was already an admin so the caller can treat that as an idempotent no-op rather
// than an error, and returns ErrNoSuchUser when the address is unknown.
func (s *Service) PromoteToAdmin(ctx context.Context, email string) (userID int64, alreadyAdmin bool, err error) {
	u, err := s.q.GetUserByEmail(ctx, email)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return 0, false, ErrNoSuchUser
	case err != nil:
		return 0, false, fmt.Errorf("accounts: promote to admin: %w", err)
	}
	if u.Role == "admin" && u.Verified {
		return u.ID, true, nil
	}

	rows, err := s.q.PromoteToAdmin(ctx, email)
	if err != nil {
		return 0, false, fmt.Errorf("accounts: promote to admin: %w", err)
	}
	if rows == 0 {
		// The account existed a moment ago and is gone now — deleted between the read and the
		// write. Loud, not a silent success.
		return 0, false, ErrNoSuchUser
	}
	return u.ID, false, nil
}
