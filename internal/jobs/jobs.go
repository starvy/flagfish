// Package jobs is the River side of the house: email, webhooks, first-blood
// announcements, imports, maintenance.
//
// The reason River is here rather than a broker is one line in the submit
// transaction:
//
//	river.InsertTx(ctx, tx, AnnounceFirstBlood{…})
//
// The enqueue is INSIDE the transaction. If the transaction rolls back, the
// announcement was never enqueued — you cannot announce a first blood for a solve
// that did not happen. Kafka or RabbitMQ would put the enqueue outside the
// transaction and hand that bug straight back.
package jobs

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/mail"
)

// WorkerDeps is everything the registered workers need to do their jobs. It is a struct
// rather than a growing parameter list so a new worker's dependency is one field, added
// in one place, not threaded through three constructors.
type WorkerDeps struct {
	Mailer   mail.Mailer
	Config   *config.Manager
	Poster   WebhookPoster
	Notifier AdminNotifier
	Log      *slog.Logger
}

// ErrNoWorkers is returned when the worker role is started but nothing is
// registered. It is an error rather than a silent idle loop: a worker process that
// looks healthy and does nothing is how a queue backs up unnoticed for a whole event.
var ErrNoWorkers = errors.New("jobs: no workers are registered")

// Workers builds the registry.
//
// river.AddWorker[T] is generic, so registration cannot be data-driven; each
// job type is registered here, and that list IS the worker manifest.
func Workers(deps WorkerDeps) (workers *river.Workers, registered int) {
	w := river.NewWorkers()
	registered = 0

	river.AddWorker(w, &SendEmailWorker{Mailer: deps.Mailer})
	registered++

	river.AddWorker(w, &AnnounceFirstBloodWorker{
		Config: deps.Config,
		Poster: deps.Poster,
		Log:    deps.Log,
	})
	registered++

	river.AddWorker(w, &PoolExhaustedAlertWorker{
		Notifier: deps.Notifier,
		Log:      deps.Log,
	})
	registered++

	// river.AddWorker(w, &importer.RunWorker{…}); registered++

	return w, registered
}

// NewInsertOnly is the client the API role holds: it INSERTS jobs and never runs
// them (Workers nil, no queues). One binary, two roles — the API process must be
// able to enqueue a first-blood announcement inside the submit transaction without
// also volunteering to send it.
func NewInsertOnly(pool *pgxpool.Pool) (*river.Client[pgx.Tx], error) {
	c, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return nil, fmt.Errorf("jobs: insert-only client: %w", err)
	}
	return c, nil
}

// NewWorker is the client the worker role holds: it runs the registered workers.
func NewWorker(pool *pgxpool.Pool, deps WorkerDeps) (*river.Client[pgx.Tx], error) {
	workers, n := Workers(deps)
	if n == 0 {
		return nil, ErrNoWorkers
	}

	c, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 10}},
		Workers: workers,
	})
	if err != nil {
		return nil, fmt.Errorf("jobs: worker client: %w", err)
	}
	return c, nil
}
