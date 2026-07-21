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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/domain/policy"
	"github.com/starvy/flagfish/internal/migrate"
)

// adminSpec is the console bootstrap as `flagfish admin create` calls it: an admin, and the instance
// that admin makes live.
func adminSpec(email, password string, mode account.Mode, explicit bool) accounts.AdminSpec {
	return accounts.AdminSpec{
		Name:     email,
		Email:    email,
		Password: password,
		Instance: accounts.InstanceSpec{Mode: mode, ModeExplicit: explicit, Version: "test"},
	}
}

func defaultInstanceSpec() accounts.InstanceSpec {
	return accounts.InstanceSpec{Mode: account.ModeUsers, Version: "test"}
}

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

	res, err := svc.CreateAdmin(ctx, adminSpec(email, password, account.ModeUsers, false))
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	id := res.UserID
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
	if _, err := svc.CreateAdmin(ctx, adminSpec(email, password, account.ModeUsers, false)); !errors.Is(err, accounts.ErrEmailTaken) {
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
	res, err := svc.CreateAdmin(ctx, adminSpec("root@example.com", "correct-horse-battery", account.ModeUsers, false))
	if err != nil {
		t.Fatalf("CreateAdmin at cap: %v", err)
	}
	u, err := q.GetUserByEmail(ctx, "root@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if u.ID != res.UserID || u.Role != "admin" {
		t.Errorf("admin row = (id %d, role %q), want (id %d, admin)", u.ID, u.Role, res.UserID)
	}
}

func TestAdminCLIPromote(t *testing.T) {
	ctx := context.Background()
	pool := provisionAdminCLIDB(t)
	svc := newAdminService(t, pool)
	q := db.New(pool)

	// Unknown address is a loud error, not a no-op.
	if _, err := svc.PromoteToAdmin(ctx, "ghost@example.com", defaultInstanceSpec()); !errors.Is(err, accounts.ErrNoSuchUser) {
		t.Fatalf("promote unknown error = %v, want ErrNoSuchUser", err)
	}
	// ...and it commits nothing: a failed promotion must not leave the instance half set up.
	if setupDone(t, pool) {
		t.Fatal("a failed promotion marked the instance set up")
	}

	hash := "x"
	uid, err := q.CreateUser(ctx, db.CreateUserParams{
		Name: "player", Email: "player@example.com", PasswordHash: &hash, Verified: false,
	})
	if err != nil {
		t.Fatalf("seed player: %v", err)
	}

	res, err := svc.PromoteToAdmin(ctx, "player@example.com", defaultInstanceSpec())
	if err != nil {
		t.Fatalf("PromoteToAdmin: %v", err)
	}
	if res.AlreadyAdmin {
		t.Error("alreadyAdmin = true on first promotion")
	}
	if res.UserID != uid.ID {
		t.Errorf("promoted id = %d, want %d", res.UserID, uid.ID)
	}
	u, err := q.GetUserByEmail(ctx, "player@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if u.Role != "admin" || !u.Verified {
		t.Errorf("after promote role=%q verified=%v, want admin/true", u.Role, u.Verified)
	}

	// Promoting again is idempotent, not an error.
	res, err = svc.PromoteToAdmin(ctx, "player@example.com", defaultInstanceSpec())
	if err != nil || !res.AlreadyAdmin {
		t.Fatalf("second promote = (already %v, err %v), want (true, nil)", res.AlreadyAdmin, err)
	}
}

// The bug this suite exists for: a freshly migrated database denies every route — /login and
// /register included — because config.setup is false and the policy gate fails closed on it. Nothing
// in the product ever wrote that key, so `migrate && admin create && serve` produced a bricked
// instance. The bootstrap must leave it live.
func TestAdminCLICreateCompletesSetup(t *testing.T) {
	ctx := context.Background()
	pool := provisionAdminCLIDB(t)
	svc := newAdminService(t, pool)

	// Before: the instance is not set up, and the gate proves what that means.
	before := loadConfig(t, pool)
	if before.SetupDone {
		t.Fatal("a freshly migrated database reports SetupDone = true; the fixture is lying")
	}
	for _, class := range []policy.RouteClass{policy.ClassLogin, policy.ClassRegister} {
		if out := decideAnon(before, class); out.Allow {
			t.Fatalf("%v was allowed before setup; this test cannot prove anything", class)
		}
	}

	if _, err := svc.CreateAdmin(ctx, adminSpec("root@example.com", "correct-horse-battery", account.ModeUsers, false)); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}

	// After: setup is done, and the routes a player needs to get in are open.
	after := loadConfig(t, pool)
	if !after.SetupDone {
		t.Fatal("SetupDone = false after `admin create`: the instance is still bricked — " +
			"every route, including /login, answers 403 setup-incomplete")
	}
	for _, class := range []policy.RouteClass{policy.ClassLogin, policy.ClassRegister} {
		if out := decideAnon(after, class); !out.Allow {
			t.Fatalf("%v denied after `admin create`: %v (status %d)", class, out.Reason, out.Status)
		}
	}

	// The other half of a playable instance: the singleton every scoring query keys on. Without a
	// row here the mode is not merely defaulted — the gameplay SQL's CROSS JOIN instance matches
	// nothing and no one can solve anything.
	if got := instanceMode(t, pool); got != account.ModeUsers.String() {
		t.Fatalf("instance.user_mode = %q, want %q", got, account.ModeUsers)
	}
	if after.Mode != account.ModeUsers {
		t.Fatalf("Snapshot.Mode = %s, want users", after.Mode)
	}
}

// --mode teams sets the model the instance plays under, and the snapshot sources it from the
// instance singleton — never from a config key.
func TestAdminCLICreateTeamsMode(t *testing.T) {
	ctx := context.Background()
	pool := provisionAdminCLIDB(t)
	svc := newAdminService(t, pool)

	res, err := svc.CreateAdmin(ctx, adminSpec("root@example.com", "correct-horse-battery", account.ModeTeams, true))
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	if res.Mode != account.ModeTeams {
		t.Fatalf("bootstrapped mode = %s, want teams", res.Mode)
	}
	if got := instanceMode(t, pool); got != account.ModeTeams.String() {
		t.Fatalf("instance.user_mode = %q, want teams", got)
	}

	snap := loadConfig(t, pool)
	if !snap.SetupDone || snap.Mode != account.ModeTeams {
		t.Fatalf("snapshot = (setup %v, mode %s), want (true, teams)", snap.SetupDone, snap.Mode)
	}
	// The mode has ONE home. The bootstrap must not have written a config user_mode key alongside it.
	if v, ok := snap.Raw("user_mode"); ok {
		t.Fatalf("the bootstrap wrote a config user_mode = %q; the account model lives in the instance", v)
	}
}

// Re-running the bootstrap on a live instance is safe, and the model it already plays is a fact: a
// second admin, or a promotion, leaves setup true and user_mode untouched. An operator who
// *explicitly* asks for the other model gets a loud refusal rather than an instance that quietly
// ignored the flag.
func TestAdminCLIBootstrapIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := provisionAdminCLIDB(t)
	svc := newAdminService(t, pool)

	if _, err := svc.CreateAdmin(ctx, adminSpec("root@example.com", "correct-horse-battery", account.ModeTeams, true)); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}

	// A second, defaulted-mode bootstrap yields to the instance instead of trying to flip it.
	res, err := svc.CreateAdmin(ctx, adminSpec("second@example.com", "correct-horse-battery", account.ModeUsers, false))
	if err != nil {
		t.Fatalf("second CreateAdmin: %v", err)
	}
	if res.Mode != account.ModeTeams {
		t.Fatalf("second bootstrap reported mode %s, want the instance's own (teams)", res.Mode)
	}

	// An explicit --mode users against a teams instance is a refusal, and it rolls the whole thing
	// back: no admin, no setup key rewritten.
	_, err = svc.CreateAdmin(ctx, adminSpec("third@example.com", "correct-horse-battery", account.ModeUsers, true))
	if !errors.Is(err, accounts.ErrModeConflict) {
		t.Fatalf("explicit mode conflict error = %v, want ErrModeConflict", err)
	}
	if _, gerr := db.New(pool).GetUserByEmail(ctx, "third@example.com"); !errors.Is(gerr, pgx.ErrNoRows) {
		t.Fatalf("the refused bootstrap committed its admin anyway (err = %v)", gerr)
	}

	promoted, err := svc.PromoteToAdmin(ctx, "second@example.com", defaultInstanceSpec())
	if err != nil {
		t.Fatalf("PromoteToAdmin on a live instance: %v", err)
	}
	if !promoted.AlreadyAdmin || promoted.Mode != account.ModeTeams {
		t.Fatalf("promote = (already %v, mode %s), want (true, teams)", promoted.AlreadyAdmin, promoted.Mode)
	}

	snap := loadConfig(t, pool)
	if !snap.SetupDone || snap.Mode != account.ModeTeams {
		t.Fatalf("after re-runs snapshot = (setup %v, mode %s), want (true, teams)", snap.SetupDone, snap.Mode)
	}
}

// loadConfig builds the snapshot exactly as the server does at boot: config rows plus the instance
// singleton, through the real Postgres store.
func loadConfig(t *testing.T, pool *pgxpool.Pool) *config.Snapshot {
	t.Helper()
	m, err := config.New(context.Background(), config.NewPGStore(pool), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("config load: %v", err)
	}
	return m.Current()
}

// decideAnon runs the real route gate for an anonymous caller, which is the only thing that proves
// "usable": SetupDone is a bool, but a 403 on /login is the bug.
func decideAnon(snap *config.Snapshot, class policy.RouteClass) policy.Outcome {
	return policy.Decide(policy.Policy{
		E: snap.Event(time.Now()),
		P: policy.Principal{},
		R: policy.Request{Class: class, Surface: policy.SurfacePublic},
	})
}

func setupDone(t *testing.T, pool *pgxpool.Pool) bool {
	t.Helper()
	var v string
	err := pool.QueryRow(context.Background(), `SELECT value FROM config WHERE key = 'setup'`).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("read setup key: %v", err)
	}
	return v == "true"
}

func instanceMode(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var mode string
	if err := pool.QueryRow(context.Background(), `SELECT user_mode FROM instance`).Scan(&mode); err != nil {
		t.Fatalf("read instance.user_mode: %v", err)
	}
	return mode
}
