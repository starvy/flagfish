// Package metrics is the Prometheus surface: a /metrics handler and a /readyz check, plus the
// instruments the two load-bearing spans and the pool gauges write to. Everything here is driven
// from the service and handler boundary — nothing in internal/domain, and nothing on the
// wrong-answer pre-lock leg of the submit hot path, ever touches it.
package metrics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/fx"

	"github.com/starvy/flagfish/internal/health"
)

// Module provides the metrics surface to the graph.
var Module = fx.Module("metrics", fx.Provide(New))

// durationBuckets is tuned for the hot path: a healthy submit or standings read is single-digit
// milliseconds, so the buckets crowd there and still reach the seconds a pool-starved request costs.
var durationBuckets = []float64{
	0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5,
}

// ErrListenersDown reports that the pool is fine but a LISTEN subscription this process depends on
// is not established. It is a distinct error because it is a distinct failure: the database is
// reachable, and the replica is still serving a config snapshot the rest of the fleet has moved on
// from.
var ErrListenersDown = errors.New("metrics: a Postgres LISTEN subscription is down")

// Metrics is the registry plus the two hot-path histograms. Gauges are pulled at scrape time by a
// collector, so they are not fields here.
type Metrics struct {
	reg       *prometheus.Registry
	pool      *pgxpool.Pool
	listeners *health.Registry

	submit     *prometheus.HistogramVec
	scoreboard prometheus.Histogram
}

// New builds the registry, registers the runtime and database collectors, and returns the handle the
// boundaries instrument through. ctx bounds the per-scrape database queries the collector runs; it is
// the app root context, so a shutting-down process stops scraping cleanly.
//
// listeners is the LISTEN subscriptions readiness must consult. Nil means none are registered, which
// is honest for a harness that runs no pumps — not a licence to report ready on a broken one.
func New(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, listeners *health.Registry) *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m := &Metrics{
		reg:       reg,
		pool:      pool,
		listeners: listeners,
		submit: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "flagfish",
			Name:      "submit_duration_seconds",
			Help:      "Flag submission latency, end to end, by outcome.",
			Buckets:   durationBuckets,
		}, []string{"result"}),
		scoreboard: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "flagfish",
			Name:      "scoreboard_duration_seconds",
			Help:      "Standings query latency.",
			Buckets:   durationBuckets,
		}),
	}
	reg.MustRegister(m.submit, m.scoreboard)
	reg.MustRegister(newDBCollector(ctx, pool, log))
	reg.MustRegister(newListenerCollector(listeners))
	return m
}

// Handler serves the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// Ready reports whether this replica should be handed traffic. It backs /readyz: liveness stays
// green on a dead pool, but readiness must not, or a replica with a broken pool keeps serving.
//
// The subscription check is the half a ping cannot do. A dropped LISTEN connection leaves the pool
// perfectly healthy — it just opens another one — while this process stops hearing config changes
// and stops delivering notifications. Green on that is worse than no probe at all, because it is a
// probe actively asserting something false.
func (m *Metrics) Ready(ctx context.Context) error {
	if err := m.pool.Ping(ctx); err != nil {
		return fmt.Errorf("metrics: readiness ping: %w", err)
	}
	if down := m.listeners.Down(); len(down) > 0 {
		return fmt.Errorf("%w: %s", ErrListenersDown, strings.Join(down, ", "))
	}
	return nil
}

// ObserveSubmit records one submission's latency under its outcome. Nil-safe, so a server wired
// without metrics — the older test harnesses — simply records nothing. The caller times the whole
// service call from the handler and observes after it returns, so nothing is added to the
// wrong-answer pre-lock leg of the submit transaction.
func (m *Metrics) ObserveSubmit(result string, d time.Duration) {
	if m == nil {
		return
	}
	m.submit.WithLabelValues(result).Observe(d.Seconds())
}

// ObserveScoreboard records one standings read.
func (m *Metrics) ObserveScoreboard(d time.Duration) {
	if m == nil {
		return
	}
	m.scoreboard.Observe(d.Seconds())
}
