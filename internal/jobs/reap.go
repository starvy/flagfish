package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/starvy/flagfish/internal/db"
)

const (
	// reapInterval is how often the sweep runs. Expired rows are harmless individually and lethal
	// in aggregate — rate_limits gains a row per bucket per window and is read on every submit —
	// so the interval only has to be short enough that the table never gets far ahead of the
	// reaper, not short enough to keep it empty.
	reapInterval = 15 * time.Minute

	// reapBatch is how many rows one DELETE may take. Small enough that the locks and the WAL it
	// writes are invisible next to a live submit, large enough that a real backlog drains.
	reapBatch = 5_000

	// reapMaxBatches bounds one run. The first sweep of a table that has never been swept can be
	// millions of rows; it drains over several runs rather than in one transaction that sits on
	// the hot path for minutes.
	reapMaxBatches = 20

	// rateLimitFloor is the least time a rate-limit row is kept regardless of the configured
	// window. The arithmetic below already keeps a row for two full windows, but that reasoning
	// assumes every replica agrees on the time; the floor is what stops a reaper on a fast clock
	// from deleting a counter an attacker is still spending against.
	rateLimitFloor = 15 * time.Minute
)

// ReapExpired is the periodic maintenance sweep: expired sessions, expired API tokens, and
// rate-limit rows whose window is long past.
//
// It carries no arguments. Everything it needs — the batch bounds, the rate-limit retention — is a
// property of the process that runs it, not of the enqueue, and putting them in the args would mean
// a job enqueued before a config change ran with the old ones.
type ReapExpired struct{}

// Kind is the River job kind. Stable across renames of the Go type.
func (ReapExpired) Kind() string { return "reap_expired" }

// InsertOpts deduplicates within half the interval. Several worker processes restarting together
// each hedge with a run-on-start insert, and one sweep is enough; the window is deliberately shorter
// than the interval so a scheduled run is never swallowed by the previous one's period.
func (ReapExpired) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:      river.QueueDefault,
		UniqueOpts: river.UniqueOpts{ByPeriod: reapInterval / 2},
	}
}

// ReapExpiredWorker deletes the rows nothing will ever read again.
//
// Every delete here is bounded and looped rather than issued whole. That is the entire design
// constraint: rate_limits is on the submit path, and a single unbounded DELETE over a table that
// has grown for a whole event takes locks and writes WAL for long enough to be an outage.
type ReapExpiredWorker struct {
	river.WorkerDefaults[ReapExpired]

	Pool *pgxpool.Pool
	Log  *slog.Logger

	// RateWindow is the limiter's fixed window, which is what makes a rate-limit row dead: it is
	// live for the window it names, and a refund can still land on the one before.
	RateWindow time.Duration

	// Batch and MaxBatches override the defaults. Zero means the default; they exist so a test can
	// prove the bounding rather than take it on faith.
	Batch      int32
	MaxBatches int
}

func (w *ReapExpiredWorker) Work(ctx context.Context, _ *river.Job[ReapExpired]) error {
	q := db.New(w.Pool)
	batch, maxBatches := w.bounds()
	cutoff := time.Now().Add(-w.rateLimitRetention())

	sessions, sessionsDone, err := w.sweep(ctx, "sessions", batch, maxBatches,
		func(ctx context.Context, n int32) (int64, error) { return q.DeleteExpiredSessions(ctx, n) })
	if err != nil {
		return fmt.Errorf("jobs: reap expired sessions: %w", err)
	}

	tokens, tokensDone, err := w.sweep(ctx, "api_tokens", batch, maxBatches,
		func(ctx context.Context, n int32) (int64, error) { return q.DeleteExpiredAPITokens(ctx, n) })
	if err != nil {
		return fmt.Errorf("jobs: reap expired api tokens: %w", err)
	}

	limits, limitsDone, err := w.sweep(ctx, "rate_limits", batch, maxBatches,
		func(ctx context.Context, n int32) (int64, error) {
			return q.DeleteOldRateLimits(ctx, db.DeleteOldRateLimitsParams{
				Before:    pgtype.Timestamptz{Time: cutoff, Valid: true},
				BatchSize: n,
			})
		})
	if err != nil {
		return fmt.Errorf("jobs: reap old rate limits: %w", err)
	}

	// Logged every run, zeros included. A reaper that only speaks up when it finds something is
	// indistinguishable from a reaper that is not running at all — which is the bug this fixes.
	w.log().InfoContext(ctx, "reaped expired rows",
		"sessions", sessions, "api_tokens", tokens, "rate_limits", limits,
		"rate_limit_cutoff", cutoff,
		"complete", sessionsDone && tokensDone && limitsDone)
	return nil
}

// sweep deletes in batches until a batch comes back short — meaning the table is clean — or the
// per-run budget runs out. done reports which of the two happened.
func (w *ReapExpiredWorker) sweep(
	ctx context.Context, table string, batch int32, maxBatches int,
	del func(ctx context.Context, batch int32) (int64, error),
) (total int64, done bool, err error) {
	for range maxBatches {
		n, derr := del(ctx, batch)
		total += n
		if derr != nil {
			return total, false, derr
		}
		if n < int64(batch) {
			return total, true, nil
		}
	}
	// Not an error — the next run continues — but the operator should know the backlog is bigger
	// than one run, because that is the shape of a table that has been growing unswept for months.
	w.log().WarnContext(ctx, "reaper hit its per-run budget; rows remain",
		"table", table, "deleted", total)
	return total, false, nil
}

// rateLimitRetention is how long a rate-limit row is kept after its window opened. A row is live for
// exactly one window, and Refund can still touch the previous one when a request straddled the
// boundary, so two windows is the floor by construction — and never less than rateLimitFloor.
func (w *ReapExpiredWorker) rateLimitRetention() time.Duration {
	return max(2*w.RateWindow, rateLimitFloor)
}

func (w *ReapExpiredWorker) bounds() (batch int32, maxBatches int) {
	batch, maxBatches = w.Batch, w.MaxBatches
	if batch <= 0 {
		batch = reapBatch
	}
	if maxBatches <= 0 {
		maxBatches = reapMaxBatches
	}
	return batch, maxBatches
}

func (w *ReapExpiredWorker) log() *slog.Logger {
	if w.Log == nil {
		return slog.Default()
	}
	return w.Log
}

// periodicJobs is the schedule the worker role owns. It is attached to the worker client and
// nowhere else, so an API-only process never enqueues maintenance for itself to not run.
func periodicJobs(deps WorkerDeps) []*river.PeriodicJob {
	if deps.Pool == nil {
		return nil
	}
	return []*river.PeriodicJob{
		river.NewPeriodicJob(
			river.PeriodicInterval(reapInterval),
			func() (river.JobArgs, *river.InsertOpts) { return ReapExpired{}, nil },
			// Run on start as well: a fleet that was down over a maintenance window comes back to a
			// backlog, and waiting a quarter of an hour to start on it is a choice nobody made.
			&river.PeriodicJobOpts{RunOnStart: true},
		),
	}
}
