//go:build integration

package integration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/migrate"
)

// adminCLIDBName is a database of this suite's own, separate from the shared flagfish_test the rest of
// the integration harness truncates. The admin bootstrap needs to run against a pristine schema and to
// tighten num_users without disturbing the other tests, so it gets its own database entirely.
const adminCLIDBName = "flagfish_test_admincli"

// provisionAdminCLIDB (re)creates adminCLIDBName on the test Postgres, migrates it, and returns a pool
// to it. The database is dropped and recreated each run so a leftover row cannot make a test pass.
func provisionAdminCLIDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	maintDSN := os.Getenv("TEST_DATABASE_URL")
	if maintDSN == "" {
		t.Skip("TEST_DATABASE_URL is not set — run `task test-integration`")
	}
	ctx := context.Background()

	targetDSN, err := swapDatabase(maintDSN, adminCLIDBName)
	if err != nil {
		t.Fatalf("derive target dsn: %v", err)
	}

	admin, err := pgx.Connect(ctx, maintDSN)
	if err != nil {
		t.Fatalf("maintenance connect: %v", err)
	}
	// CREATE/DROP DATABASE cannot run in a transaction and takes an identifier, not a bind
	// parameter; the name is a compile-time constant, so there is no injection surface here.
	if _, err = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+adminCLIDBName+" WITH (FORCE)"); err != nil {
		admin.Close(ctx)
		t.Fatalf("drop database: %v", err)
	}
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+adminCLIDBName); err != nil {
		admin.Close(ctx)
		t.Fatalf("create database: %v", err)
	}
	admin.Close(ctx)

	if err = migrate.Run(ctx, targetDSN, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate target: %v", err)
	}

	pool, err := pgxpool.New(ctx, targetDSN)
	if err != nil {
		t.Fatalf("target pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func swapDatabase(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	u.Path = "/" + name
	return u.String(), nil
}

func newAdminService(t *testing.T, pool *pgxpool.Pool) *accounts.Service {
	t.Helper()
	return accounts.NewService(pool, account.ModeUsers, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestAdminCLICreate(t *testing.T) {
	ctx := context.Background()
	pool := provisionAdminCLIDB(t)
	svc := newAdminService(t, pool)
	q := db.New(pool)

	const email = "root@example.com"
	const password = "correct-horse-battery"

	id, err := svc.CreateAdmin(ctx, "Root", email, password)
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	if id == 0 {
		t.Fatal("CreateAdmin returned id 0")
	}

	u, err := q.GetUserByEmail(ctx, email)
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if u.Role != "admin" {
		t.Errorf("role = %q, want admin", u.Role)
	}
	if !u.Verified {
		t.Error("verified = false, want true")
	}

	// The stored hash must actually verify the password we set: a bootstrap that writes an
	// admin who cannot log in is worse than none.
	sess, err := svc.Login(ctx, email, password)
	if err != nil {
		t.Fatalf("Login after CreateAdmin: %v", err)
	}
	if sess.UserID != id {
		t.Errorf("session UserID = %d, want %d", sess.UserID, id)
	}

	// A second create on the same address is a loud failure, not a silent duplicate.
	if _, err := svc.CreateAdmin(ctx, "Root Again", email, password); !errors.Is(err, accounts.ErrEmailTaken) {
		t.Fatalf("duplicate CreateAdmin error = %v, want ErrEmailTaken", err)
	}
}

func TestAdminCLICreateBypassesUserCap(t *testing.T) {
	ctx := context.Background()
	pool := provisionAdminCLIDB(t)
	svc := newAdminService(t, pool)
	q := db.New(pool)

	// Fill the instance to a num_users cap of 1 with an ordinary player.
	if _, err := pool.Exec(ctx, `INSERT INTO config (key, value) VALUES ('num_users', '1')`); err != nil {
		t.Fatalf("seed num_users: %v", err)
	}
	hash := "x"
	if _, err := q.CreateUser(ctx, db.CreateUserParams{
		Name: "player", Email: "player@example.com", PasswordHash: &hash, Verified: true,
	}); err != nil {
		t.Fatalf("seed player: %v", err)
	}

	// The cap is real: a second ordinary registration is rejected by the trigger.
	_, err := q.CreateUser(ctx, db.CreateUserParams{
		Name: "blocked", Email: "blocked@example.com", PasswordHash: &hash, Verified: true,
	})
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.ConstraintName != "users_caps" {
		t.Fatalf("over-cap CreateUser error = %v, want users_caps check violation", err)
	}

	// The admin bootstrap must punch through it — the first admin cannot be locked out of a
	// full instance.
	id, err := svc.CreateAdmin(ctx, "Root", "root@example.com", "correct-horse-battery")
	if err != nil {
		t.Fatalf("CreateAdmin at cap: %v", err)
	}
	u, err := q.GetUserByEmail(ctx, "root@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if u.ID != id || u.Role != "admin" {
		t.Errorf("admin row = (id %d, role %q), want (id %d, admin)", u.ID, u.Role, id)
	}
}

func TestAdminCLIPromote(t *testing.T) {
	ctx := context.Background()
	pool := provisionAdminCLIDB(t)
	svc := newAdminService(t, pool)
	q := db.New(pool)

	// Unknown address is a loud error, not a no-op.
	if _, _, err := svc.PromoteToAdmin(ctx, "ghost@example.com"); !errors.Is(err, accounts.ErrNoSuchUser) {
		t.Fatalf("promote unknown error = %v, want ErrNoSuchUser", err)
	}

	hash := "x"
	uid, err := q.CreateUser(ctx, db.CreateUserParams{
		Name: "player", Email: "player@example.com", PasswordHash: &hash, Verified: false,
	})
	if err != nil {
		t.Fatalf("seed player: %v", err)
	}

	id, already, err := svc.PromoteToAdmin(ctx, "player@example.com")
	if err != nil {
		t.Fatalf("PromoteToAdmin: %v", err)
	}
	if already {
		t.Error("alreadyAdmin = true on first promotion")
	}
	if id != uid.ID {
		t.Errorf("promoted id = %d, want %d", id, uid.ID)
	}
	u, err := q.GetUserByEmail(ctx, "player@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if u.Role != "admin" || !u.Verified {
		t.Errorf("after promote role=%q verified=%v, want admin/true", u.Role, u.Verified)
	}

	// Promoting again is idempotent, not an error.
	if _, already, err := svc.PromoteToAdmin(ctx, "player@example.com"); err != nil || !already {
		t.Fatalf("second promote = (already %v, err %v), want (true, nil)", already, err)
	}
}
