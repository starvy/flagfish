package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"go.uber.org/fx"

	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/mail"
	"github.com/starvy/flagfish/internal/notify"
	"github.com/starvy/flagfish/internal/storage"
)

// bindNotifier exposes notify.Service as the narrow AdminNotifier the pool-exhaustion worker needs.
func bindNotifier(s *notify.Service) AdminNotifier { return s }

// Inserter and Worker are the two River roles as distinct Go types, so a process running
// both (serve --with-worker) can hold both in one graph without the container having to
// tell two *river.Client[pgx.Tx] values apart. The API enqueues through an Inserter; the
// Worker runs what was enqueued.
type (
	Inserter struct{ *river.Client[pgx.Tx] }
	Worker   struct{ *river.Client[pgx.Tx] }
)

// InserterModule provides the insert-only client the API role holds. It is built eagerly
// at boot: constructing it proves the River schema and pool are usable before the first
// submit transaction tries to enqueue inside itself.
var InserterModule = fx.Module(
	"jobs.inserter",
	fx.Provide(newInserter),
	fx.Invoke(func(*Inserter) {}),
)

func newInserter(pool *pgxpool.Pool) (*Inserter, error) {
	c, err := NewInsertOnly(pool)
	if err != nil {
		return nil, err
	}
	return &Inserter{Client: c}, nil
}

// WorkerModule provides and starts the worker client for a standalone `worker` process.
// With no workers registered it fails to construct — which is the point: a worker process
// that looks healthy and runs nothing is worse than one that refuses to start.
var WorkerModule = fx.Module(
	"jobs.worker",
	fx.Provide(NewHTTPPoster),
	// The standalone worker has no HTTP layer and so no notify.Module; it provides the
	// publish-only notify service itself, without the broadcaster's LISTEN pump it has no use for.
	fx.Provide(notify.NewService),
	fx.Provide(bindNotifier),
	fx.Provide(newWorker),
	fx.Invoke(func(*Worker) {}),
)

func newWorker(root context.Context, lc fx.Lifecycle, pool *pgxpool.Pool, log *slog.Logger, mailer mail.Mailer, cfg *config.Manager, poster WebhookPoster, notifier AdminNotifier, store storage.Store, version ProductVersion) (*Worker, error) {
	c, err := NewWorker(pool, WorkerDeps{Mailer: mailer, Config: cfg, Poster: poster, Notifier: notifier, Log: log, Pool: pool, Store: store, Version: version})
	if err != nil {
		return nil, err
	}
	appendWorkerLifecycle(root, lc, c, log)
	return &Worker{Client: c}, nil
}

// InProcessWorkerModule is the worker for serve --with-worker. Unlike the standalone
// role, "nothing registered" is tolerated here: the operator asked for one container, and
// an API that refuses to serve because there are no jobs yet would be the wrong call. It
// warns and serves.
var InProcessWorkerModule = fx.Module(
	"jobs.worker-inprocess",
	fx.Provide(NewHTTPPoster),
	// notify.Service comes from notify.Module in the serve graph; bind it to the narrow seam.
	fx.Provide(bindNotifier),
	fx.Invoke(startInProcessWorker),
)

func startInProcessWorker(root context.Context, lc fx.Lifecycle, pool *pgxpool.Pool, log *slog.Logger, mailer mail.Mailer, cfg *config.Manager, poster WebhookPoster, notifier AdminNotifier, store storage.Store, version ProductVersion) error {
	c, err := NewWorker(pool, WorkerDeps{Mailer: mailer, Config: cfg, Poster: poster, Notifier: notifier, Log: log, Pool: pool, Store: store, Version: version})
	if errors.Is(err, ErrNoWorkers) {
		log.Warn("no workers are registered yet; serving without an in-process worker")
		return nil
	}
	if err != nil {
		return err
	}
	appendWorkerLifecycle(root, lc, c, log)
	return nil
}

func appendWorkerLifecycle(root context.Context, lc fx.Lifecycle, c *river.Client[pgx.Tx], log *slog.Logger) {
	// River keeps this context for the client's whole life — its fetch loops and LISTEN
	// notifier run under it, and cancelling it is River's abrupt-shutdown path. So it
	// outlives every OnStart context (those are cancelled once Start returns), and we
	// cancel it ourselves only after OnStop has drained the in-flight jobs.
	clientCtx, cancel := context.WithCancel(context.WithoutCancel(root))
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			if err := c.Start(clientCtx); err != nil {
				cancel()
				return fmt.Errorf("jobs: worker start: %w", err)
			}
			log.Info("worker started")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			defer cancel()
			// Stop waits for in-flight jobs; an import cut mid-flight is a restore from
			// backup, so the wait is worth it.
			if err := c.Stop(ctx); err != nil {
				return fmt.Errorf("jobs: worker did not stop cleanly; a job may have been cut mid-flight: %w", err)
			}
			return nil
		},
	})
}
