//go:build integration

// White-box tests: they reach the unexported Service fields to swap in a capturing enqueuer, so the
// task-row lifecycle and the single-in-flight guard can be exercised against a real Postgres without
// standing up a River client. The workers themselves are driven end-to-end in internal/jobs; here we
// prove the enqueue side, the download side, and the second-connection progress guarantee.
package opsjob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/jobs"
	"github.com/starvy/flagfish/internal/migrate"
	"github.com/starvy/flagfish/internal/platform/exporter"
	"github.com/starvy/flagfish/internal/storage"
)

// A restore truncates the whole instance, so this suite runs against its own database, never the
// shared flagfish_test the other suites use.
const opsDBName = "flagfish_test_opsjob"

var provisionOnce sync.Once

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func dsn(t *testing.T) string {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	own := *u
	own.Path = "/" + opsDBName
	return own.String()
}

func provision(t *testing.T) {
	t.Helper()
	provisionOnce.Do(func() {
		ctx := context.Background()
		admin, err := pgx.Connect(ctx, os.Getenv("TEST_DATABASE_URL"))
		if err != nil {
			t.Fatalf("connect admin: %v", err)
		}
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+opsDBName); err != nil {
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "42P04" { // duplicate_database
				admin.Close(ctx)
				t.Fatalf("create database: %v", err)
			}
		}
		admin.Close(ctx)
		if err := migrate.Run(ctx, dsn(t), testLog()); err != nil {
			t.Fatalf("migrate ops db: %v", err)
		}
	})
}

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	provision(t)
	p, err := pgxpool.New(context.Background(), dsn(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

func testStore(t *testing.T) storage.Store {
	t.Helper()
	ep := os.Getenv("TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("TEST_S3_ENDPOINT is not set")
	}
	cfg := storage.S3Config{
		Endpoint:  ep,
		Bucket:    envOr("TEST_S3_BUCKET", "flagfish-test"),
		Region:    "us-east-1",
		AccessKey: envOr("TEST_S3_ACCESS_KEY", "flagfishtest"),
		SecretKey: envOr("TEST_S3_SECRET_KEY", "flagfishtest123"),
		UseSSL:    false,
		PathStyle: true,
	}
	if err := storage.EnsureBucket(context.Background(), cfg); err != nil {
		t.Fatalf("ensure bucket: %v", err)
	}
	store, err := storage.NewS3Store(cfg)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return store
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// seed installs a minimal, deterministic instance under the replica role so no trigger fires. It is
// enough for a real export/restore round-trip: an instance row and a config row that must come back.
func seed(t *testing.T, ctx context.Context, p *pgxpool.Pool) {
	t.Helper()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	exec := func(sql string, args ...any) {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed %q: %v", sql, err)
		}
	}
	exec(`SET LOCAL session_replication_role = replica`)
	exec(`TRUNCATE instance, config, brackets, teams, users, fields, field_entries, api_tokens,
		tracking, challenges, files, tags, flags, hints, challenge_instances, flag_issues,
		submissions, solves, awards, hint_unlocks, notifications, audit_log, sessions,
		email_tokens, rate_limits, tasks RESTART IDENTITY CASCADE`)
	exec(`INSERT INTO instance (id, user_mode, version) VALUES (true, 'users', 'seed')`)
	exec(`INSERT INTO config (id, key, value) VALUES (1, 'user_mode', 'users')`)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
}

// capturingEnq stands in for the River inserter: it records the enqueued job args so a test can read
// back what would have run, and never touches River's schema.
type capturingEnq struct {
	mu   sync.Mutex
	args []river.JobArgs
}

func (c *capturingEnq) InsertTx(_ context.Context, _ pgx.Tx, a river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.args = append(c.args, a)
	return &rivertype.JobInsertResult{}, nil
}

func (c *capturingEnq) last() river.JobArgs {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.args) == 0 {
		return nil
	}
	return c.args[len(c.args)-1]
}

func newService(p *pgxpool.Pool, store storage.Store) (*Service, *capturingEnq) {
	enq := &capturingEnq{}
	return &Service{pool: p, q: db.New(p), store: store, enq: enq, log: testLog()}, enq
}

const adminID int64 = 0 // no users seeded; created_by is nullable and unchecked under replica

// A backup is enqueued as a queued export task, and a second backup while it is in flight is refused
// — not queued behind the first. The refusal is the tasks_one_in_flight unique index, surfaced as
// ErrInFlight.
func TestEnqueueBackup_SingleInFlight(t *testing.T) {
	ctx := context.Background()
	p := pool(t)
	store := testStore(t)
	seed(t, ctx, p)

	svc, enq := newService(p, store)

	first, err := svc.EnqueueBackup(ctx, adminID, exporter.ProfileBackup)
	if err != nil {
		t.Fatalf("first backup: %v", err)
	}
	if first.Kind != "export" || first.State != "queued" {
		t.Fatalf("first task = %+v, want export/queued", first)
	}
	if _, ok := enq.last().(jobs.RunExport); !ok {
		t.Fatalf("expected a RunExport enqueued, got %T", enq.last())
	}

	if _, err := svc.EnqueueBackup(ctx, adminID, exporter.ProfileBackup); !errors.Is(err, ErrInFlight) {
		t.Fatalf("second backup err = %v, want ErrInFlight", err)
	}
}

// EnqueueRestore stashes the uploaded archive in the object store under the key it hands to the job,
// so the worker in another process can read it back.
func TestEnqueueRestore_StashesUpload(t *testing.T) {
	ctx := context.Background()
	p := pool(t)
	store := testStore(t)
	seed(t, ctx, p)

	svc, enq := newService(p, store)
	payload := []byte("archive-bytes-marker")
	task, err := svc.EnqueueRestore(ctx, adminID, bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("enqueue restore: %v", err)
	}
	if task.Kind != "import" {
		t.Fatalf("restore task kind = %q, want import", task.Kind)
	}
	args, ok := enq.last().(jobs.RunRestore)
	if !ok {
		t.Fatalf("expected a RunRestore enqueued, got %T", enq.last())
	}
	rc, err := store.Get(ctx, args.UploadKey)
	if err != nil {
		t.Fatalf("stashed upload not in store: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read stashed upload: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("stashed upload = %q, want %q", got, payload)
	}
	if err := store.Delete(ctx, args.UploadKey); err != nil {
		t.Fatalf("cleanup upload: %v", err)
	}
}

// EnqueueImport shares the 'import' kind with restore, so the two are mutually exclusive — only one
// thing loads the database at a time.
func TestEnqueueImport_SharesKindWithRestore(t *testing.T) {
	ctx := context.Background()
	p := pool(t)
	store := testStore(t)
	seed(t, ctx, p)

	svc, _ := newService(p, store)
	imp, err := svc.EnqueueImport(ctx, adminID, bytes.NewReader([]byte("PK-marker")), 9, "", "")
	if err != nil {
		t.Fatalf("enqueue import: %v", err)
	}
	if imp.Kind != "import" {
		t.Fatalf("import task kind = %q, want import", imp.Kind)
	}
	if _, err := svc.EnqueueRestore(ctx, adminID, bytes.NewReader([]byte("x")), 1); !errors.Is(err, ErrInFlight) {
		t.Fatalf("restore during import err = %v, want ErrInFlight", err)
	}
}

// OpenBackup streams a finished export's artifact and refuses a task that is not a succeeded export.
func TestOpenBackup(t *testing.T) {
	ctx := context.Background()
	p := pool(t)
	store := testStore(t)
	seed(t, ctx, p)

	svc, _ := newService(p, store)
	q := db.New(p)

	// A finished backup: put its artifact where the worker would, then mark the task succeeded.
	done, err := svc.EnqueueBackup(ctx, adminID, exporter.ProfileBackup)
	if err != nil {
		t.Fatalf("enqueue backup: %v", err)
	}
	artifact := []byte("PK\x03\x04-pretend-zip")
	key := jobs.BackupObjectKey(done.ID)
	// The bucket is shared with the jobs suite, whose task ids reset per database; clear any prior
	// object at this key and remove ours afterwards so neither suite serves the other's bytes.
	if err = store.Delete(ctx, key); err != nil {
		t.Fatalf("pre-test cleanup: %v", err)
	}
	if err = store.Put(ctx, key, int64(len(artifact)), bytes.NewReader(artifact)); err != nil {
		t.Fatalf("put artifact: %v", err)
	}
	t.Cleanup(func() {
		if derr := store.Delete(context.Background(), key); derr != nil {
			t.Errorf("cleanup: delete %s: %v", key, derr)
		}
	})
	if err = q.FinishTask(ctx, db.FinishTaskParams{ID: done.ID}); err != nil {
		t.Fatalf("finish task: %v", err)
	}

	rc, name, err := svc.OpenBackup(ctx, done.ID)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	rc.Close()
	if !bytes.Equal(body, artifact) {
		t.Fatalf("downloaded %q = %q, want %q", name, body, artifact)
	}

	// A still-queued export has no artifact yet.
	queued, err := svc.EnqueueBackup(ctx, adminID, exporter.ProfileBackup)
	if err != nil {
		t.Fatalf("second backup: %v", err)
	}
	if _, _, err := svc.OpenBackup(ctx, queued.ID); !errors.Is(err, ErrBackupNotReady) {
		t.Fatalf("OpenBackup(queued) = %v, want ErrBackupNotReady", err)
	}
	if _, _, err := svc.OpenBackup(ctx, 999999); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("OpenBackup(unknown) = %v, want ErrTaskNotFound", err)
	}
}

// The trap the schema warns about: a restore is one transaction, so its own progress UPDATEs are
// invisible until it commits. This asserts the fix — progress written on a SEPARATE connection is
// visible to a poller WHILE the restore transaction is still open. If the progress write had gone on
// the restore's own transaction, the cross-connection read below would see nothing until commit.
func TestRestoreProgress_VisibleBeforeCommit(t *testing.T) {
	ctx := context.Background()
	p := pool(t)
	store := testStore(t)
	seed(t, ctx, p)

	var buf bytes.Buffer
	if _, err := exporter.Export(ctx, p, store, exporter.ProfileBackup, "test", &buf); err != nil {
		t.Fatalf("export: %v", err)
	}
	archive := buf.Bytes()

	q := db.New(p)
	row, err := q.EnqueueTask(ctx, db.EnqueueTaskParams{Kind: "import"})
	if err != nil {
		t.Fatalf("enqueue task: %v", err)
	}
	taskID := row.ID
	if err := q.StartTask(ctx, db.StartTaskParams{ID: taskID, Progress: 0}); err != nil {
		t.Fatalf("start task: %v", err)
	}

	observedMidTx := false
	progress := func(ctx context.Context, detail string, percent int) {
		// Write on the pool (its own connection), exactly as the worker does.
		d := detail
		if err := q.SetTaskProgress(ctx, db.SetTaskProgressParams{ID: taskID, Progress: int32(percent), Detail: &d}); err != nil {
			t.Errorf("progress write: %v", err)
			return
		}
		// Only the table phase is guaranteed inside the open, uncommitted restore transaction.
		if percent < 10 || percent >= 95 {
			return
		}
		// Read back on a DIFFERENT pooled connection than the restore transaction holds. Seeing the
		// freshly written running/progress here proves it was committed on a second connection.
		poll, perr := q.GetTask(ctx, taskID)
		if perr != nil {
			t.Errorf("poll task: %v", perr)
			return
		}
		if poll.State == "running" && poll.Progress == int32(percent) {
			observedMidTx = true
		}
	}

	if _, err := exporter.RestoreWithProgress(ctx, p, store, bytes.NewReader(archive), int64(len(archive)), progress); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !observedMidTx {
		t.Fatal("progress was never visible mid-transaction: the second-connection write is not working")
	}
	// The restore does NOT wipe its own task row: tasks is excluded from the truncate.
	if _, err := q.GetTask(ctx, taskID); err != nil {
		t.Fatalf("task row must survive the restore, got %v", err)
	}
}

func TestGet_UnknownTask(t *testing.T) {
	ctx := context.Background()
	p := pool(t)
	store := testStore(t)
	svc, _ := newService(p, store)
	if _, err := svc.Get(ctx, 999999); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("Get(unknown) = %v, want ErrTaskNotFound", err)
	}
}
