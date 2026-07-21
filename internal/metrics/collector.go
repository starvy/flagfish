package metrics

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/starvy/flagfish/internal/health"
)

// listenerCollector exposes the LISTEN subscriptions' state. It is the metric that answers "when
// did this replica go deaf, and how often does it happen" — readiness only says yes or no, and the
// resubscribe count is what tells an operator a flapping network apart from one bad night.
type listenerCollector struct {
	reg *health.Registry

	subscribed *prometheus.Desc
	subscribes *prometheus.Desc
}

func newListenerCollector(reg *health.Registry) *listenerCollector {
	return &listenerCollector{
		reg: reg,
		subscribed: prometheus.NewDesc("flagfish_listener_subscribed",
			"1 when the process holds a live LISTEN subscription on this channel, 0 when it does not.",
			[]string{"channel"}, nil),
		subscribes: prometheus.NewDesc("flagfish_listener_subscribes_total",
			"Times this process has established its LISTEN subscription; anything above 1 is a reconnect.",
			[]string{"channel"}, nil),
	}
}

func (c *listenerCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.subscribed
	ch <- c.subscribes
}

func (c *listenerCollector) Collect(ch chan<- prometheus.Metric) {
	for _, l := range c.reg.All() {
		var up float64
		if l.Subscribed() {
			up = 1
		}
		ch <- prometheus.MustNewConstMetric(c.subscribed, prometheus.GaugeValue, up, l.Name())
		ch <- prometheus.MustNewConstMetric(c.subscribes, prometheus.CounterValue, float64(l.Subscribes()), l.Name())
	}
}

// dbCollector emits the gauges that are cheapest read fresh at scrape time rather than tracked on
// the hot path: pool saturation, job-queue depth, and the instance-pool utilisation that must be
// right before the event — pool exhaustion mid-CTF is a hard failure by design. Pull, not push:
// there is no counter to keep in sync and no staleness window.
type dbCollector struct {
	// ctx is the app root context, held so each scrape's queries inherit a parent that a
	// shutting-down process cancels. A Collector's Collect carries no context of its own.
	ctx  context.Context
	pool *pgxpool.Pool
	log  *slog.Logger

	poolConns  *prometheus.Desc
	poolMax    *prometheus.Desc
	riverJobs  *prometheus.Desc
	instTotal  *prometheus.Desc
	instIssued *prometheus.Desc
}

func newDBCollector(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) *dbCollector {
	return &dbCollector{
		ctx: ctx, pool: pool, log: log,
		poolConns: prometheus.NewDesc("flagfish_db_pool_connections",
			"Connections in the pgx pool, by state.", []string{"state"}, nil),
		poolMax: prometheus.NewDesc("flagfish_db_pool_max_connections",
			"Configured maximum size of the pgx pool.", nil, nil),
		riverJobs: prometheus.NewDesc("flagfish_river_jobs",
			"River jobs, by state.", []string{"state"}, nil),
		instTotal: prometheus.NewDesc("flagfish_instance_pool_total",
			"Instances in a unique-flag challenge's pool.", []string{"challenge_id"}, nil),
		instIssued: prometheus.NewDesc("flagfish_instance_pool_issued",
			"Instances already issued to an account.", []string{"challenge_id"}, nil),
	}
}

func (c *dbCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.poolConns
	ch <- c.poolMax
	ch <- c.riverJobs
	ch <- c.instTotal
	ch <- c.instIssued
}

func (c *dbCollector) Collect(ch chan<- prometheus.Metric) {
	// Pool saturation is read straight off the pool — no query, always emitted.
	st := c.pool.Stat()
	ch <- prometheus.MustNewConstMetric(c.poolConns, prometheus.GaugeValue, float64(st.AcquiredConns()), "acquired")
	ch <- prometheus.MustNewConstMetric(c.poolConns, prometheus.GaugeValue, float64(st.IdleConns()), "idle")
	ch <- prometheus.MustNewConstMetric(c.poolConns, prometheus.GaugeValue, float64(st.ConstructingConns()), "constructing")
	ch <- prometheus.MustNewConstMetric(c.poolConns, prometheus.GaugeValue, float64(st.TotalConns()), "total")
	ch <- prometheus.MustNewConstMetric(c.poolMax, prometheus.GaugeValue, float64(st.MaxConns()))

	// The DB-derived families share one short deadline: a slow scrape must not hold a connection
	// open, and a failing query skips its family loudly rather than failing the whole scrape.
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
	defer cancel()

	c.collectRiver(ctx, ch)
	c.collectInstancePools(ctx, ch)
}

func (c *dbCollector) collectRiver(ctx context.Context, ch chan<- prometheus.Metric) {
	rows, err := c.pool.Query(ctx, `SELECT state::text, count(*) FROM river_job GROUP BY state`)
	if err != nil {
		c.log.WarnContext(ctx, "metrics: river queue-depth query failed", "error", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int64
		if err := rows.Scan(&state, &n); err != nil {
			c.log.WarnContext(ctx, "metrics: river queue-depth scan failed", "error", err)
			return
		}
		ch <- prometheus.MustNewConstMetric(c.riverJobs, prometheus.GaugeValue, float64(n), state)
	}
	if err := rows.Err(); err != nil {
		c.log.WarnContext(ctx, "metrics: river queue-depth iteration failed", "error", err)
	}
}

func (c *dbCollector) collectInstancePools(ctx context.Context, ch chan<- prometheus.Metric) {
	rows, err := c.pool.Query(ctx, `
SELECT ci.challenge_id::text,
       count(*)              AS total,
       count(fi.instance_id) AS issued
  FROM challenge_instances ci
  LEFT JOIN flag_issues fi ON fi.instance_id = ci.id
 GROUP BY ci.challenge_id`)
	if err != nil {
		c.log.WarnContext(ctx, "metrics: instance-pool utilisation query failed", "error", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var challengeID string
		var total, issued int64
		if err := rows.Scan(&challengeID, &total, &issued); err != nil {
			c.log.WarnContext(ctx, "metrics: instance-pool utilisation scan failed", "error", err)
			return
		}
		ch <- prometheus.MustNewConstMetric(c.instTotal, prometheus.GaugeValue, float64(total), challengeID)
		ch <- prometheus.MustNewConstMetric(c.instIssued, prometheus.GaugeValue, float64(issued), challengeID)
	}
	if err := rows.Err(); err != nil {
		c.log.WarnContext(ctx, "metrics: instance-pool utilisation iteration failed", "error", err)
	}
}
