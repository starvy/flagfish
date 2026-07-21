// Package opsjob is the enqueue-and-report side of the async admin operations: backup, restore, and
// foreign import. The work itself runs in a River worker (internal/jobs); this package turns an
// admin request into a task row plus an enqueued job, reads that row back for a progress poll, and
// streams a finished backup out of the object store.
//
// The single-in-flight guard is the database's, not this package's: EnqueueTask inserts into a table
// whose partial unique index permits one queued-or-running task per kind, and the unique violation
// that a second concurrent enqueue raises is surfaced as ErrInFlight — never a check-then-insert
// race this code would have to win.
package opsjob

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"go.uber.org/fx"

	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/jobs"
	"github.com/starvy/flagfish/internal/platform/exporter"
	"github.com/starvy/flagfish/internal/storage"
)

var (
	// ErrInFlight is the single-in-flight refusal: a task of this kind is already queued or running.
	// It maps to 409 — the operation is not queued behind the running one, it is declined.
	ErrInFlight = errors.New("opsjob: an operation of this kind is already in flight")
	// ErrTaskNotFound is an unknown task id on a progress poll or a download.
	ErrTaskNotFound = errors.New("opsjob: task not found")
	// ErrBackupNotReady is a download of a task that is not a finished backup: still running, failed,
	// or not an export at all. The artifact only exists once the export succeeded.
	ErrBackupNotReady = errors.New("opsjob: backup is not ready to download")
	// ErrStorageUnavailable is a backup/restore attempted with no object storage configured. Archives
	// and file blobs both live there, so there is nowhere to put or read them.
	ErrStorageUnavailable = errors.New("opsjob: object storage is not configured")
)

// enqueuer is the transactional insert seam River gives us: the task row and the job enqueue commit
// or roll back together, so a refused task never leaves a job queued to run against a row that does
// not exist. *jobs.Inserter satisfies it.
type enqueuer interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// Task is the poll-facing view of a tasks row: enough to render a progress bar and, for a finished
// backup, to offer a download. It carries no database types.
type Task struct {
	ID        int64
	Kind      string
	State     string
	Progress  int32
	Detail    string
	Error     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Downloadable reports whether this task has a backup artifact to fetch.
func (t Task) Downloadable() bool { return t.Kind == "export" && t.State == "succeeded" }

func fromDB(r db.Task) Task {
	t := Task{
		ID: r.ID, Kind: r.Kind, State: r.State, Progress: r.Progress,
		CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
	if r.Detail != nil {
		t.Detail = *r.Detail
	}
	if r.Error != nil {
		t.Error = *r.Error
	}
	return t
}

var Module = fx.Module("opsjob", fx.Provide(New))

// Service owns the pool, the object store, and the River inserter. It holds a pool rather than
// reaching through another service because the task rows, the enqueue, and the blob transfers are
// all its own — no other feature writes tasks.
type Service struct {
	pool  *pgxpool.Pool
	q     *db.Queries
	store storage.Store
	enq   enqueuer
}

func New(pool *pgxpool.Pool, store storage.Store, ins *jobs.Inserter) *Service {
	return &Service{pool: pool, q: db.New(pool), store: store, enq: ins}
}

// EnqueueBackup records an export task and enqueues the job that fulfils it. profile chooses fidelity
// — ProfileBackup is the restorable disaster-recovery artifact, ProfileSafe the shareable field-masked
// one. A second backup while one is in flight is refused with ErrInFlight.
func (s *Service) EnqueueBackup(ctx context.Context, actorID int64, profile exporter.Profile) (Task, error) {
	if _, err := exporter.ParseProfile(string(profile)); err != nil {
		return Task{}, err
	}
	return s.enqueue(ctx, actorID, "export", func(tx pgx.Tx, taskID int64) error {
		_, err := s.enq.InsertTx(ctx, tx, jobs.RunExport{TaskID: taskID, Profile: string(profile)}, nil)
		return err
	})
}

// EnqueueRestore stashes the uploaded archive in the object store, then records an import task and
// enqueues the restore. The upload is stashed before the task so the job args can name it; if the
// enqueue is then refused, the stash is deleted so a declined restore leaks nothing.
func (s *Service) EnqueueRestore(ctx context.Context, actorID int64, archive io.Reader, size int64) (Task, error) {
	key, err := s.stash(ctx, archive, size)
	if err != nil {
		return Task{}, err
	}
	task, err := s.enqueue(ctx, actorID, "import", func(tx pgx.Tx, taskID int64) error {
		_, ierr := s.enq.InsertTx(ctx, tx, jobs.RunRestore{TaskID: taskID, UploadKey: key}, nil)
		return ierr
	})
	if err != nil {
		s.discard(ctx, key)
		return Task{}, err
	}
	return task, nil
}

// EnqueueImport is EnqueueRestore's foreign-archive sibling: a one-way CTFd import rather than a
// restore of our own backup. It shares the 'import' kind, so it is mutually exclusive with a restore
// — only one thing may be loading the database at a time.
func (s *Service) EnqueueImport(ctx context.Context, actorID int64, archive io.Reader, size int64, assumeRevision, forceType string) (Task, error) {
	key, err := s.stash(ctx, archive, size)
	if err != nil {
		return Task{}, err
	}
	task, err := s.enqueue(ctx, actorID, "import", func(tx pgx.Tx, taskID int64) error {
		_, ierr := s.enq.InsertTx(ctx, tx, jobs.RunImport{
			TaskID: taskID, UploadKey: key,
			AssumeRevision: assumeRevision, ForceUnknownChallengeType: forceType,
		}, nil)
		return ierr
	})
	if err != nil {
		s.discard(ctx, key)
		return Task{}, err
	}
	return task, nil
}

// enqueue inserts the task and runs enqueueJob in the same transaction. The task insert may raise the
// single-in-flight unique violation, which becomes ErrInFlight; any enqueue error rolls the task back.
func (s *Service) enqueue(ctx context.Context, actorID int64, kind string, enqueueJob func(tx pgx.Tx, taskID int64) error) (Task, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Task{}, fmt.Errorf("opsjob: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := s.q.WithTx(tx)
	row, err := q.EnqueueTask(ctx, db.EnqueueTaskParams{Kind: kind, CreatedBy: &actorID})
	if err != nil {
		if inFlight(err) {
			return Task{}, ErrInFlight
		}
		return Task{}, fmt.Errorf("opsjob: insert %s task: %w", kind, err)
	}
	if err := enqueueJob(tx, row.ID); err != nil {
		return Task{}, fmt.Errorf("opsjob: enqueue %s job: %w", kind, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Task{}, fmt.Errorf("opsjob: commit: %w", err)
	}
	return fromDB(row), nil
}

// Get reads a task for a progress poll.
func (s *Service) Get(ctx context.Context, id int64) (Task, error) {
	row, err := s.q.GetTask(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, fmt.Errorf("opsjob: get task %d: %w", id, err)
	}
	return fromDB(row), nil
}

// OpenBackup streams a finished backup's archive out of the object store. The caller closes the
// reader. A task that is not a succeeded export has no artifact and is refused with ErrBackupNotReady.
func (s *Service) OpenBackup(ctx context.Context, id int64) (io.ReadCloser, string, error) {
	task, err := s.Get(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if !task.Downloadable() {
		return nil, "", ErrBackupNotReady
	}
	rc, err := s.store.Get(ctx, jobs.BackupObjectKey(id))
	if err != nil {
		if errors.Is(err, storage.ErrNotConfigured) {
			return nil, "", ErrStorageUnavailable
		}
		return nil, "", fmt.Errorf("opsjob: open backup %d: %w", id, err)
	}
	return rc, fmt.Sprintf("flagfish-backup-%d.zip", id), nil
}

// stash writes an uploaded archive to a fresh, random object key. A random key (not the content
// address) keeps a one-shot upload out of the content-addressed blob space, so deleting it after the
// job can never remove a challenge attachment that happened to share its bytes.
func (s *Service) stash(ctx context.Context, r io.Reader, size int64) (string, error) {
	key, err := uploadKey()
	if err != nil {
		return "", err
	}
	if err := s.store.Put(ctx, key, size, r); err != nil {
		if errors.Is(err, storage.ErrNotConfigured) {
			return "", ErrStorageUnavailable
		}
		return "", fmt.Errorf("opsjob: stash upload: %w", err)
	}
	return key, nil
}

// discard best-effort deletes a stashed upload whose task was refused. Detached from the request's
// cancellation so a declined enqueue still cleans up; a delete failure is a logged leak upstream.
func (s *Service) discard(ctx context.Context, key string) {
	_ = s.store.Delete(context.WithoutCancel(ctx), key)
}

func uploadKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("opsjob: generate upload key: %w", err)
	}
	return "ops-upload-" + hex.EncodeToString(b[:]) + ".zip", nil
}

// inFlight reports whether err is the tasks_one_in_flight partial-unique violation.
func inFlight(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "tasks_one_in_flight"
}
