package jobs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/platform/exporter"
	"github.com/starvy/flagfish/internal/platform/importer"
	"github.com/starvy/flagfish/internal/storage"
)

// ProductVersion is the build's version string, stamped into a backup manifest and into
// instance.version on import — the same value the CLI passes. A named type so fx can supply it
// without colliding with every other string in the graph.
type ProductVersion string

// BackupObjectKey is where a completed backup's archive lives in the object store, one per task id.
// The task id is unique, so the key never collides and the store's dedupe can never hand back a
// stale archive. Both the worker that writes it and the download handler that reads it derive the
// key here, so the convention has exactly one home.
func BackupObjectKey(taskID int64) string {
	return fmt.Sprintf("ops-backup-%020d.zip", taskID)
}

// opsAttempts is 1 for every ops job: a backup, restore, or import is an operator action with a
// visible task row, not something to silently re-run. A failure is stamped on the task and the kind
// is freed; the operator decides whether to try again.
var opsInsertOpts = &river.InsertOpts{Queue: river.QueueDefault, MaxAttempts: 1}

// RunExport backs the instance up into the object store. TaskID owns the state a poller reads.
type RunExport struct {
	TaskID  int64  `json:"task_id"`
	Profile string `json:"profile"`
}

func (RunExport) Kind() string                 { return "ops_export" }
func (RunExport) InsertOpts() river.InsertOpts { return *opsInsertOpts }

// RunRestore rehydrates a --backup archive that was uploaded to UploadKey in the object store.
type RunRestore struct {
	TaskID    int64  `json:"task_id"`
	UploadKey string `json:"upload_key"`
}

func (RunRestore) Kind() string                 { return "ops_restore" }
func (RunRestore) InsertOpts() river.InsertOpts { return *opsInsertOpts }

// RunImport imports a foreign (CTFd) archive that was uploaded to UploadKey. The force/assume knobs
// mirror the `flagfish import` flags exactly.
type RunImport struct {
	TaskID                    int64  `json:"task_id"`
	UploadKey                 string `json:"upload_key"`
	AssumeRevision            string `json:"assume_revision,omitempty"`
	ForceUnknownChallengeType string `json:"force_unknown_challenge_type,omitempty"`
}

func (RunImport) Kind() string                 { return "ops_import" }
func (RunImport) InsertOpts() river.InsertOpts { return *opsInsertOpts }

// opsWorker is the shared machinery of the three ops workers: the pool, the store, and the task-row
// writer. The task writer runs on db.New(pool), which draws its own connection from the pool — never
// the connection a restore transaction is holding — so progress is visible while that transaction is
// still open. This is the single reason the workers hold a pool rather than a transaction.
type opsWorker struct {
	Pool    *pgxpool.Pool
	Store   storage.Store
	Version ProductVersion
	Log     *slog.Logger
}

func (o *opsWorker) tasks() *db.Queries { return db.New(o.Pool) }

// start claims the queued task for this worker. A failure here is fatal to the job: without the row
// in 'running' the progress and completion writes below would no-op and the task would look stuck.
func (o *opsWorker) start(ctx context.Context, q *db.Queries, taskID int64, detail string) error {
	d := detail
	if err := q.StartTask(ctx, db.StartTaskParams{ID: taskID, Progress: 0, Detail: &d}); err != nil {
		return fmt.Errorf("jobs: start task %d: %w", taskID, err)
	}
	return nil
}

// progress returns the reporter handed to the exporter. A progress-write hiccup is logged, never
// returned: it must not fail a restore that is otherwise succeeding.
func (o *opsWorker) progress(q *db.Queries, taskID int64) exporter.Progress {
	return func(ctx context.Context, detail string, percent int) {
		d := detail
		// A progress reading is a 0-100 percentage; clamp it so a stray value can never wrap the
		// int32 column into a negative or nonsensical progress.
		//nolint:gosec // clamped to 0-100 on the line above; the int32 conversion cannot overflow.
		pct := int32(min(max(percent, 0), 100))
		if err := q.SetTaskProgress(ctx, db.SetTaskProgressParams{ID: taskID, Progress: pct, Detail: &d}); err != nil {
			o.Log.WarnContext(ctx, "task progress write failed", "task", taskID, "error", err)
		}
	}
}

// fail stamps the cause on the task and returns it. The error is returned, not swallowed: River
// records the failed attempt (opsAttempts is 1, so it is not retried) and the task row carries the
// reason for the operator. The detail text is admin-only, so a wrapped driver message is acceptable.
func (o *opsWorker) fail(ctx context.Context, q *db.Queries, taskID int64, cause error) error {
	msg := cause.Error()
	if err := q.FailTask(ctx, db.FailTaskParams{ID: taskID, Error: &msg}); err != nil {
		o.Log.ErrorContext(ctx, "marking task failed also failed", "task", taskID, "error", err)
	}
	return cause
}

func (o *opsWorker) finish(ctx context.Context, q *db.Queries, taskID int64, detail string) error {
	d := detail
	if err := q.FinishTask(ctx, db.FinishTaskParams{ID: taskID, Detail: &d}); err != nil {
		return fmt.Errorf("jobs: finish task %d: %w", taskID, err)
	}
	return nil
}

// RunExportWorker produces a backup archive and uploads it to the object store under
// BackupObjectKey. The archive is streamed through a temp file, never held in memory: a full-fidelity
// backup of a large event is larger than we want on the heap.
type RunExportWorker struct {
	river.WorkerDefaults[RunExport]
	opsWorker
}

func (w *RunExportWorker) Work(ctx context.Context, job *river.Job[RunExport]) error {
	q := w.tasks()
	taskID := job.Args.TaskID
	if err := w.start(ctx, q, taskID, "starting backup"); err != nil {
		return err
	}

	profile, err := exporter.ParseProfile(job.Args.Profile)
	if err != nil {
		return w.fail(ctx, q, taskID, err)
	}

	tmp, err := os.CreateTemp("", "flagfish-backup-*.zip")
	if err != nil {
		return w.fail(ctx, q, taskID, fmt.Errorf("jobs: create temp archive: %w", err))
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	rep, err := exporter.ExportWithProgress(ctx, w.Pool, w.Store, profile, string(w.Version), tmp, w.progress(q, taskID))
	if err != nil {
		return w.fail(ctx, q, taskID, err)
	}

	info, err := tmp.Stat()
	if err != nil {
		return w.fail(ctx, q, taskID, fmt.Errorf("jobs: stat archive: %w", err))
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return w.fail(ctx, q, taskID, fmt.Errorf("jobs: rewind archive: %w", err))
	}
	if err := w.Store.Put(ctx, BackupObjectKey(taskID), info.Size(), tmp); err != nil {
		return w.fail(ctx, q, taskID, fmt.Errorf("jobs: upload archive: %w", err))
	}

	return w.finish(ctx, q, taskID, fmt.Sprintf("backup ready — %s profile, %d file(s), %d byte(s)", rep.Profile, rep.Files, info.Size()))
}

// RunRestoreWorker restores an uploaded --backup archive. The stashed upload is deleted afterwards,
// on every path: it was a one-shot transfer medium, and the admin can re-upload to retry.
type RunRestoreWorker struct {
	river.WorkerDefaults[RunRestore]
	opsWorker
}

func (w *RunRestoreWorker) Work(ctx context.Context, job *river.Job[RunRestore]) error {
	q := w.tasks()
	taskID := job.Args.TaskID
	defer w.discardUpload(ctx, job.Args.UploadKey)

	if err := w.start(ctx, q, taskID, "downloading archive"); err != nil {
		return err
	}

	tmp, size, err := w.fetchUpload(ctx, job.Args.UploadKey)
	if err != nil {
		return w.fail(ctx, q, taskID, err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	rep, err := exporter.RestoreWithProgress(ctx, w.Pool, w.Store, tmp, size, w.progress(q, taskID))
	if err != nil {
		return w.fail(ctx, q, taskID, err)
	}
	return w.finish(ctx, q, taskID, fmt.Sprintf("restore complete — user_mode %s, %d file(s)", rep.UserMode, rep.Files))
}

// RunImportWorker imports a foreign (CTFd) archive. Import is a bulk COPY, not one big transaction we
// stream progress through, so the reporting is coarse: downloading, then importing, then done.
type RunImportWorker struct {
	river.WorkerDefaults[RunImport]
	opsWorker
}

func (w *RunImportWorker) Work(ctx context.Context, job *river.Job[RunImport]) error {
	q := w.tasks()
	taskID := job.Args.TaskID
	defer w.discardUpload(ctx, job.Args.UploadKey)

	if err := w.start(ctx, q, taskID, "downloading archive"); err != nil {
		return err
	}

	tmp, _, err := w.fetchUpload(ctx, job.Args.UploadKey)
	if err != nil {
		return w.fail(ctx, q, taskID, err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	w.progress(q, taskID)(ctx, "importing archive", 40)
	rep, err := importer.Run(ctx, w.Pool, tmp.Name(), importer.Options{
		Version:                   string(w.Version),
		AssumeRevision:            job.Args.AssumeRevision,
		ForceUnknownChallengeType: job.Args.ForceUnknownChallengeType,
	})
	if err != nil {
		return w.fail(ctx, q, taskID, err)
	}
	return w.finish(ctx, q, taskID, fmt.Sprintf("import complete — source revision %s", rep.SourceRevision))
}

// fetchUpload streams the stashed archive out of the object store into a temp file, which gives the
// restorer the io.ReaderAt and size it needs (the store hands back a plain stream).
func (o *opsWorker) fetchUpload(ctx context.Context, key string) (*os.File, int64, error) {
	rc, err := o.Store.Get(ctx, key)
	if err != nil {
		return nil, 0, fmt.Errorf("jobs: fetch uploaded archive: %w", err)
	}
	defer func() { _ = rc.Close() }()

	tmp, err := os.CreateTemp("", "flagfish-upload-*.zip")
	if err != nil {
		return nil, 0, fmt.Errorf("jobs: create temp upload: %w", err)
	}
	size, err := io.Copy(tmp, rc)
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, 0, fmt.Errorf("jobs: buffer uploaded archive: %w", err)
	}
	return tmp, size, nil
}

// discardUpload removes the one-shot upload object. It runs with the parent's cancellation detached
// so a shutdown mid-job still cleans up; a delete failure is a logged leak, not a job failure.
func (o *opsWorker) discardUpload(ctx context.Context, key string) {
	if err := o.Store.Delete(context.WithoutCancel(ctx), key); err != nil {
		o.Log.WarnContext(ctx, "could not delete uploaded archive", "key", key, "error", err)
	}
}
