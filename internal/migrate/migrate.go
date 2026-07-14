// Package migrate runs the goose migrations, behind a Postgres advisory lock.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for goose
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/starvy/flagfish/internal/db/migrations"
)

// lockID is an arbitrary but fixed 64-bit key. Every replica that wants to migrate
// asks for this same lock, which is the whole mechanism. Changing it re-opens the
// race, so it is a constant and it never moves.
const lockID int64 = 0x766C616A6B61 // "flagfish"

// Run applies all outstanding migrations.
//
// Migrate-on-boot with N replicas is a schema-corrupting race: N processes
// start, all read "current version = 4", and all try to apply migration 5. Goose's
// own version table does not save you — two transactions can both pass the version
// check before either commits, and then you are applying DDL twice against a schema
// that only tolerated it once.
//
// The fix is one line, and it is trivially forgotten: take an advisory lock around
// the whole migration step. The losers block, wake up to find the work already done,
// and no-op. Postgres releases the lock if the winner dies mid-migration, so a
// crashed deploy does not wedge the fleet.
//
// The lock must be taken on a single pinned connection — an advisory lock is held by
// a session, and a pool hands out a different session each call. That is what
// db.Conn() is for here, and it is not an optimisation.
func Run(ctx context.Context, dsn string, log *slog.Logger) error {
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("migrate: open: %w", err)
	}
	defer sqlDB.Close()

	conn, err := sqlDB.Conn(ctx) // pin one session: the lock lives on it
	if err != nil {
		return fmt.Errorf("migrate: pin connection: %w", err)
	}
	defer conn.Close()

	log.Info("acquiring migration lock", "lock_id", lockID)
	if _, lockErr := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lockID); lockErr != nil {
		return fmt.Errorf("migrate: acquire advisory lock: %w", lockErr)
	}
	defer func() {
		// Best effort: if this fails the session is already gone, and Postgres has
		// released the lock for us. Say so rather than dropping it on the floor.
		if _, unlockErr := conn.ExecContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", lockID); unlockErr != nil {
			log.Warn("releasing migration lock failed; the session is gone and Postgres has released it",
				"lock_id", lockID, "error", unlockErr)
		}
	}()

	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(gooseLogger{log})
	if dialectErr := goose.SetDialect("postgres"); dialectErr != nil {
		return fmt.Errorf("migrate: dialect: %w", dialectErr)
	}

	if upErr := goose.UpContext(ctx, sqlDB, "."); upErr != nil {
		return fmt.Errorf("migrate: up: %w", upErr)
	}

	version, err := goose.GetDBVersionContext(ctx, sqlDB)
	if err != nil {
		return fmt.Errorf("migrate: version: %w", err)
	}
	log.Info("goose migrations applied", "version", version)

	// River owns its own tables and its own migrator, so they are not goose migrations —
	// forking River's schema into our migration series would break every River upgrade
	// from here on. They are applied here, under the same advisory lock we are still
	// holding, because they are exactly as racy with N replicas as ours are.
	//
	// This is not optional plumbing. The submit hot path enqueues AnnounceFirstBlood with
	// river.InsertTx inside the solve transaction. With no `river_job` table that insert
	// fails, and because it is in the transaction it takes the solve down with it — so the
	// first first-blood of the event would 500, on the most visible action in the product.
	// Migrating River is part of migrating flagfish.
	if err := RunRiver(ctx, dsn, log); err != nil {
		return err
	}
	return nil
}

// RunRiver applies River's own migrations (river_job, river_leader, …).
//
// Idempotent: River records its version in `river_migration` and no-ops when current.
// Callers already inside Run hold the advisory lock; the test harness calls this
// directly.
func RunRiver(ctx context.Context, dsn string, log *slog.Logger) error {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("migrate: river: pool: %w", err)
	}
	defer pool.Close()

	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("migrate: river: migrator: %w", err)
	}

	res, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return fmt.Errorf("migrate: river: up: %w", err)
	}
	for _, v := range res.Versions {
		log.Info("river migration applied", "version", v.Version, "name", v.Name)
	}
	return nil
}

// Version reports the current schema version without changing anything.
func Version(ctx context.Context, dsn string) (int64, error) {
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return 0, fmt.Errorf("migrate: open: %w", err)
	}
	defer sqlDB.Close()

	goose.SetBaseFS(migrations.FS)
	if dialectErr := goose.SetDialect("postgres"); dialectErr != nil {
		return 0, fmt.Errorf("migrate: dialect: %w", dialectErr)
	}
	version, err := goose.GetDBVersionContext(ctx, sqlDB)
	if err != nil {
		return 0, fmt.Errorf("migrate: version: %w", err)
	}
	return version, nil
}

// Embedded reports the migrations compiled into this binary. Useful for a
// pre-deploy sanity check that the image contains what you think it does.
func Embedded() ([]string, error) {
	entries, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("migrate: glob embedded migrations: %w", err)
	}
	return entries, nil
}

type gooseLogger struct{ log *slog.Logger }

func (g gooseLogger) Printf(format string, v ...any) {
	g.log.Info(fmt.Sprintf(format, v...))
}

func (g gooseLogger) Fatalf(format string, v ...any) {
	// goose calls Fatalf on failure. We refuse to os.Exit from a library: the
	// error is already being returned to the caller, who decides what to do.
	g.log.Error(fmt.Sprintf(format, v...))
}
