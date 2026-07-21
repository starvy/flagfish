//go:build integration

// White-box worker tests: the three ops workers are driven directly with a hand-built river.Job, the
// same way pool_alert_test drives its worker, against a real Postgres and a real object store. This
// is where the backup and restore actually run end to end; the enqueue and download sides live in
// internal/opsjob.
package jobs

import (
	"archive/zip"
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

	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/migrate"
	"github.com/starvy/flagfish/internal/platform/exporter"
	"github.com/starvy/flagfish/internal/storage"
)

const opsDBName = "flagfish_test_jobsops"

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
			if !errors.As(err, &pgErr) || pgErr.Code != "42P04" {
				admin.Close(ctx)
				t.Fatalf("create database: %v", err)
			}
		}
		admin.Close(ctx)
		if err := migrate.Run(ctx, dsn(t), testLog()); err != nil {
			t.Fatalf("migrate: %v", err)
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

func newOps(p *pgxpool.Pool, store storage.Store) opsWorker {
	return opsWorker{Pool: p, Store: store, Version: "test", Log: testLog()}
}

func queueTask(t *testing.T, ctx context.Context, p *pgxpool.Pool, kind string) int64 {
	t.Helper()
	row, err := db.New(p).EnqueueTask(ctx, db.EnqueueTaskParams{Kind: kind})
	if err != nil {
		t.Fatalf("enqueue %s task: %v", kind, err)
	}
	return row.ID
}

// The export worker runs a real backup, marks the task succeeded at 100%, and leaves a valid zip at
// the task's backup key.
func TestRunExportWorker_ProducesArchive(t *testing.T) {
	ctx := context.Background()
	p := pool(t)
	store := testStore(t)
	seed(t, ctx, p)

	taskID := queueTask(t, ctx, p, "export")
	// The bucket is shared across the integration suites while task-id sequences reset per database,
	// so a stale object at this key from another suite would be served back by the store's dedupe.
	// Production task ids are globally unique per instance; this Delete only isolates the test.
	if err := store.Delete(ctx, BackupObjectKey(taskID)); err != nil {
		t.Fatalf("pre-test cleanup: %v", err)
	}

	w := &RunExportWorker{opsWorker: newOps(p, store)}
	if err := w.Work(ctx, &river.Job[RunExport]{Args: RunExport{TaskID: taskID, Profile: "backup"}}); err != nil {
		t.Fatalf("export worker: %v", err)
	}

	got, err := db.New(p).GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != "succeeded" || got.Progress != 100 {
		t.Fatalf("task = state %q progress %d, want succeeded/100", got.State, got.Progress)
	}

	rc, err := store.Get(ctx, BackupObjectKey(taskID))
	if err != nil {
		t.Fatalf("backup artifact missing: %v", err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read backup artifact: %v", err)
	}
	if _, err := zip.NewReader(bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("backup artifact is not a valid zip: %v", err)
	}
	if err := store.Delete(ctx, BackupObjectKey(taskID)); err != nil {
		t.Fatalf("cleanup backup artifact: %v", err)
	}
}

// The restore worker downloads the stashed archive, restores it in one transaction, marks the task
// succeeded, and deletes the one-shot upload.
func TestRunRestoreWorker_RoundTrip(t *testing.T) {
	ctx := context.Background()
	p := pool(t)
	store := testStore(t)
	seed(t, ctx, p)

	var buf bytes.Buffer
	if _, err := exporter.Export(ctx, p, store, exporter.ProfileBackup, "test", &buf); err != nil {
		t.Fatalf("export: %v", err)
	}
	uploadKey := "ops-upload-test-restore.zip"
	if err := store.Put(ctx, uploadKey, int64(buf.Len()), bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("stash upload: %v", err)
	}

	// Wipe config so the restore has something to bring back.
	if _, err := p.Exec(ctx, `TRUNCATE config`); err != nil {
		t.Fatalf("wipe config: %v", err)
	}

	taskID := queueTask(t, ctx, p, "import")
	w := &RunRestoreWorker{opsWorker: newOps(p, store)}
	if err := w.Work(ctx, &river.Job[RunRestore]{Args: RunRestore{TaskID: taskID, UploadKey: uploadKey}}); err != nil {
		t.Fatalf("restore worker: %v", err)
	}

	got, err := db.New(p).GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != "succeeded" || got.Progress != 100 {
		t.Fatalf("task = state %q progress %d, want succeeded/100", got.State, got.Progress)
	}
	var n int64
	if err := p.QueryRow(ctx, `SELECT count(*) FROM config`).Scan(&n); err != nil {
		t.Fatalf("count config: %v", err)
	}
	if n != 1 {
		t.Fatalf("config rows after restore = %d, want 1", n)
	}
	present, serr := store.Stat(ctx, uploadKey)
	if serr != nil {
		t.Fatalf("stat upload: %v", serr)
	}
	if present {
		t.Fatal("restore worker must delete the one-shot upload")
	}
}

// A restore of a non-archive fails loudly: the task is marked failed with the reason, and the error
// is returned so River records the failed attempt.
func TestRunRestoreWorker_FailsLoudly(t *testing.T) {
	ctx := context.Background()
	p := pool(t)
	store := testStore(t)
	seed(t, ctx, p)

	uploadKey := "ops-upload-test-garbage.zip"
	garbage := []byte("notazip")
	if err := store.Put(ctx, uploadKey, int64(len(garbage)), bytes.NewReader(garbage)); err != nil {
		t.Fatalf("stash: %v", err)
	}
	taskID := queueTask(t, ctx, p, "import")
	w := &RunRestoreWorker{opsWorker: newOps(p, store)}
	if err := w.Work(ctx, &river.Job[RunRestore]{Args: RunRestore{TaskID: taskID, UploadKey: uploadKey}}); err == nil {
		t.Fatal("restore of a non-archive must return an error")
	}
	got, err := db.New(p).GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != "failed" || got.Error == nil || *got.Error == "" {
		t.Fatalf("task = %+v, want failed with an error message", got)
	}
}
