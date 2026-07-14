package accounts

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/account"
)

// ErrNoSuchUser is returned when a promotion targets an address that no account holds.
var ErrNoSuchUser = errors.New("accounts: no user with that email")

// ErrModeConflict means the instance already plays a different account model than the one asked
// for. user_mode is fixed at setup — every scoring query keys on it — so this is a refusal, not a
// migration.
var ErrModeConflict = errors.New("accounts: the instance is already set up under a different account model")

// InstanceSpec is the instance the console bootstrap brings up, or finds already up.
type InstanceSpec struct {
	// Mode is the account model. It is fixed forever at setup, so it only ever takes effect on a
	// database that has no instance row yet.
	Mode account.Mode

	// ModeExplicit says the operator named the mode. That turns "the instance already plays a
	// different model" from a fact to accept into an ErrModeConflict: an operator who asked for
	// teams must never be told the instance is live and then find it playing users.
	ModeExplicit bool

	// Version names the binary that set the instance up; it is stamped on the instance row.
	Version string
}

// AdminSpec is a first-admin bootstrap: the account, and the instance it makes live.
type AdminSpec struct {
	Name     string
	Email    string
	Password string
	Instance InstanceSpec
}

// Bootstrapped reports what the bootstrap left behind.
type Bootstrapped struct {
	UserID int64

	// Mode is the account model now in force. It is the requested one on a fresh database and the
	// instance's own on a re-run, which is why it is reported rather than assumed.
	Mode account.Mode

	// AlreadyAdmin is set by PromoteToAdmin when the account needed no promotion.
	AlreadyAdmin bool
}

// CreateAdmin inserts a verified admin AND makes the instance live, in one transaction. It is the
// console-only escape from the bootstrap deadlock: role='admin' is grantable only through the admin
// API, which itself requires an admin, so the first one cannot be made that way — and until setup is
// marked done the route policy denies every request, including the login the admin would need.
//
// Both halves or neither: an instance is never live without an admin, and never has an admin without
// being live.
//
// The insert runs with the account-caps trigger suppressed for the transaction, the same exemption
// the importer runs under, so a full instance — one already at its num_users cap — still cannot lock
// its own first admin out. Suppressing the trigger also skips the audit capture for these rows, which
// is acceptable: their existence is the bootstrap record.
func (s *Service) CreateAdmin(ctx context.Context, spec AdminSpec) (Bootstrapped, error) {
	hash, err := Hash(spec.Password)
	if err != nil {
		return Bootstrapped{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Bootstrapped{}, fmt.Errorf("accounts: create admin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	// SET LOCAL scopes the change to this transaction; replica role disables the BEFORE INSERT
	// caps trigger so the cap cannot reject the bootstrap admin. Requires a superuser role,
	// which the importer already assumes.
	if _, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica"); err != nil {
		return Bootstrapped{}, fmt.Errorf("accounts: create admin: suppress caps trigger: %w", err)
	}

	id, err := s.q.WithTx(tx).CreateAdmin(ctx, db.CreateAdminParams{
		Name:         spec.Name,
		Email:        spec.Email,
		PasswordHash: &hash,
	})
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == pgerrcode.UniqueViolation && pg.ConstraintName == "users_email_uniq" {
			return Bootstrapped{}, ErrEmailTaken
		}
		return Bootstrapped{}, fmt.Errorf("accounts: create admin: %w", err)
	}

	mode, err := s.goLive(ctx, tx, spec.Instance)
	if err != nil {
		return Bootstrapped{}, fmt.Errorf("accounts: create admin: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Bootstrapped{}, fmt.Errorf("accounts: create admin: %w", err)
	}
	return Bootstrapped{UserID: id, Mode: mode}, nil
}

// PromoteToAdmin elevates an existing account to a verified admin, by email, and — in the same
// transaction — makes sure the instance is live, exactly as CreateAdmin does. Promotion is the
// bootstrap path for an instance whose accounts already exist (an import, a restore), and one of
// those can be sitting on a database that was never marked set up.
//
// Idempotent on a live instance: it reports AlreadyAdmin rather than erroring, and re-asserting an
// instance row that already exists changes nothing. The account model of a live instance is a fact —
// it is honoured, not overwritten — unless the operator explicitly demanded a different one, which
// is ErrModeConflict.
//
// Returns ErrNoSuchUser when the address is unknown.
func (s *Service) PromoteToAdmin(ctx context.Context, email string, inst InstanceSpec) (Bootstrapped, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Bootstrapped{}, fmt.Errorf("accounts: promote to admin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	q := s.q.WithTx(tx)

	u, err := q.GetUserByEmail(ctx, email)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Bootstrapped{}, ErrNoSuchUser
	case err != nil:
		return Bootstrapped{}, fmt.Errorf("accounts: promote to admin: %w", err)
	}

	alreadyAdmin := u.Role == "admin" && u.Verified
	if !alreadyAdmin {
		rows, perr := q.PromoteToAdmin(ctx, email)
		if perr != nil {
			return Bootstrapped{}, fmt.Errorf("accounts: promote to admin: %w", perr)
		}
		if rows == 0 {
			// The row was read a statement ago in this very transaction; zero rows updated means the
			// email no longer matches, which cannot happen. Loud, not a silent success.
			return Bootstrapped{}, ErrNoSuchUser
		}
	}

	mode, err := s.goLive(ctx, tx, inst)
	if err != nil {
		return Bootstrapped{}, fmt.Errorf("accounts: promote to admin: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Bootstrapped{}, fmt.Errorf("accounts: promote to admin: %w", err)
	}
	return Bootstrapped{UserID: u.ID, Mode: mode, AlreadyAdmin: alreadyAdmin}, nil
}

// goLive writes the two rows that make an instance playable — the instance singleton the scoring SQL
// keys on, and the config key the route policy gates on — and returns the account model actually in
// force. Both writes are upserts, so a re-run is a no-op rather than a corruption.
func (s *Service) goLive(ctx context.Context, tx pgx.Tx, inst InstanceSpec) (account.Mode, error) {
	q := s.q.WithTx(tx)

	raw, err := q.EnsureInstance(ctx, db.EnsureInstanceParams{
		UserMode: inst.Mode.String(),
		Version:  inst.Version,
	})
	if err != nil {
		return 0, fmt.Errorf("ensure instance: %w", err)
	}
	mode, err := account.ParseMode(raw)
	if err != nil {
		return 0, fmt.Errorf("instance user_mode: %w", err)
	}
	if inst.ModeExplicit && mode != inst.Mode {
		return 0, fmt.Errorf("%w: it plays %q, you asked for %q (user_mode is fixed at setup)",
			ErrModeConflict, mode, inst.Mode)
	}

	if err := q.MarkSetupComplete(ctx); err != nil {
		return 0, fmt.Errorf("mark setup complete: %w", err)
	}
	// A server may already be running against this database with a snapshot in which setup was never
	// done — every route in it 403s. Wake it up on commit instead of making the operator restart it.
	if err := config.NotifyChanged(ctx, tx); err != nil {
		return 0, err
	}
	return mode, nil
}
