// Package app is the composition root. It is the one place that knows how every other
// package fits together, so that no feature package has to import another just to be
// wired. The role topology — serve, worker, serve --with-worker — is expressed as a
// choice of fx modules, and the same choice is what the graph-validation tests check.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/anticheat"
	"github.com/starvy/flagfish/internal/board"
	"github.com/starvy/flagfish/internal/catalog"
	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/files"
	"github.com/starvy/flagfish/internal/gameplay"
	"github.com/starvy/flagfish/internal/health"
	"github.com/starvy/flagfish/internal/httpapi"
	"github.com/starvy/flagfish/internal/jobs"
	"github.com/starvy/flagfish/internal/mail"
	"github.com/starvy/flagfish/internal/metrics"
	"github.com/starvy/flagfish/internal/notify"
	"github.com/starvy/flagfish/internal/solvefeed"
	"github.com/starvy/flagfish/internal/opsjob"
	"github.com/starvy/flagfish/internal/stats"
	"github.com/starvy/flagfish/internal/storage"
)

// Version is the build's version string. main sets it from its own -ldflags-stamped var before the
// graph is built; it is supplied into every role so the backup/import workers stamp the same value
// the CLI does. "dev" for a plain build.
var Version = "dev"

// ServeConfig is the resolved serve-role configuration. The flags and environment are
// parsed in main; this is the settled result handed to the graph.
type ServeConfig struct {
	Addr           string
	TrustedProxies []*net.IPNet
	WithWorker     bool
}

// Serve runs the API process until ctx is cancelled, optionally with the worker in-process.
func Serve(ctx context.Context, env *config.Env, log *slog.Logger, sc ServeConfig) error {
	return run(ctx, fx.New(ServeOptions(ctx, env, log, sc)...))
}

// Worker runs a standalone job worker until ctx is cancelled.
func Worker(ctx context.Context, env *config.Env, log *slog.Logger) error {
	return run(ctx, fx.New(WorkerOptions(ctx, env, log)...))
}

// ServeOptions is the serve-role graph. It is exported so the validation test can check
// exactly the graph the binary runs, rather than a re-derived approximation of it.
func ServeOptions(ctx context.Context, env *config.Env, log *slog.Logger, sc ServeConfig) []fx.Option {
	opts := append(
		baseOptions(ctx, env, log),
		config.Module,
		accounts.Module,
		jobs.InserterModule,
		mail.Module,
		gameplay.Module,
		catalog.Module,
		board.Module,
		adminops.Module,
		anticheat.Module,
		stats.Module,
		files.Module,
		metrics.Module,
		opsjob.Module,

		fx.Provide(provideStore),
		fx.Supply(httpapi.ListenAddr(sc.Addr)),
		fx.Supply(sc.TrustedProxies),
		fx.Supply(jobs.RateWindow(env.RateWindow)),
		fx.Supply(accounts.LimiterConfig{Limit: env.RateLimit, Window: env.RateWindow}),
		fx.Supply(accounts.AuthLimiterConfig{Limit: env.AuthRateLimit, Window: env.RateWindow}),
		fx.Supply(accounts.AuthFailureLimiterConfig{Limit: env.AuthIPFailureLimit, Window: env.RateWindow}),
		fx.Supply(accounts.AuthIPLimiterConfig{Limit: env.AuthIPRateLimit, Window: env.RateWindow}),
		fx.Provide(provideMode),
		fx.Invoke(assertMode),
		fx.Invoke(func(cfg *config.Manager) {
			log.Info("serving", "addr", sc.Addr, "with_worker", sc.WithWorker, "ctf", cfg.Current().CTFName)
		}),
	)

	// Order is load-bearing. fx stops lifecycle hooks in REVERSE registration order, so
	// the HTTP server must be registered LAST to shut down FIRST: on SIGTERM it stops
	// accepting, drains in-flight requests, and only then does the worker drain its jobs
	// and the pool close. Registered the other way round, the worker would stop first
	// while the listener kept taking traffic, and a long job drain could exhaust the
	// shutdown budget before the HTTP server ever got to drain.
	if sc.WithWorker {
		opts = append(opts, jobs.InProcessWorkerModule)
	}
	// notify is listed after httpapi so it stops FIRST: on shutdown the broadcaster closes every SSE
	// subscriber, which lets those long-lived handlers return before the HTTP server drains. The
	// other way round, srv.Shutdown would wait on streams that never end until the shutdown budget
	// ran out. Its LISTEN pump starting a moment after the listener binds is harmless — a fresh
	// server has no connected clients to miss a notification.
	// solvefeed sits alongside notify, and after httpapi, for the same reason: it too closes
	// long-lived SSE subscribers on shutdown, and those handlers have to return before the HTTP
	// server drains rather than after the budget expires.
	opts = append(opts, httpapi.Module, notify.Module, solvefeed.Module)
	return opts
}

// WorkerOptions is the standalone worker graph: a pool and the worker client, nothing
// that serves HTTP.
func WorkerOptions(ctx context.Context, env *config.Env, log *slog.Logger) []fx.Option {
	// The worker sends mail, so it needs the mailer, which needs the config snapshot it
	// is built from — both roles pull the same SMTP settings out of the same table. It also needs
	// the object store: the backup/restore/import workers read and write archives and file blobs
	// through it, and without it those jobs are not registered at all.
	return append(baseOptions(ctx, env, log), config.Module, mail.Module,
		fx.Supply(jobs.RateWindow(env.RateWindow)),
		fx.Provide(provideStore), jobs.WorkerModule)
}

func baseOptions(ctx context.Context, env *config.Env, log *slog.Logger) []fx.Option {
	return []fx.Option{
		// Route fx's own lifecycle chatter through our logger at debug, so it is visible
		// when an operator turns the level up and silent otherwise.
		fx.WithLogger(func() fxevent.Logger {
			l := &fxevent.SlogLogger{Logger: log}
			l.UseLogLevel(slog.LevelDebug)
			return l
		}),
		// The root context is a graph value (constructors take it); lifecycle hooks get
		// fx's own start/stop contexts instead.
		fx.Provide(func() context.Context { return ctx }),
		fx.Supply(env),
		fx.Supply(log),
		fx.Supply(jobs.ProductVersion(Version)),
		// Every role that LISTENs registers here, and readiness reads it. It is in the base options
		// rather than beside the probe because the worker role subscribes too — it just has no
		// /readyz to answer with.
		fx.Provide(health.NewRegistry),
		fx.Provide(providePool),
	}
}

func providePool(ctx context.Context, lc fx.Lifecycle, env *config.Env) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(env.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("database: parse dsn: %w", err)
	}
	// Size the pool from the environment rather than pgx's NumCPU-shaped default, which queues
	// the whole fleet on the pool at CTF start. These win over any ?pool_max_conns= in the DSN
	// on purpose — the env var is the one documented knob.
	cfg.MaxConns = env.DBMaxConns
	cfg.MinConns = env.DBMinConns
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := pool.Ping(ctx); err != nil {
				return fmt.Errorf("database unreachable: %w", err)
			}
			return nil
		},
		OnStop: func(context.Context) error {
			pool.Close()
			return nil
		},
	})
	return pool, nil
}

// provideStore builds the object store from the environment. A malformed toggle or a partial
// configuration fails here, loudly, at boot; an empty one yields a disabled store that boots but
// refuses file operations.
func provideStore(log *slog.Logger) (storage.Store, error) {
	cfg, err := storage.ConfigFromEnv()
	if err != nil {
		return nil, err
	}
	return storage.NewStore(cfg, log)
}

// provideMode reads the account mode from the config snapshot. It is fixed at setup and
// immutable, which is what makes it safe to resolve once at wiring time.
func provideMode(cfg *config.Manager) account.Mode { return cfg.Current().Mode }

// assertMode refuses to boot if the configured user_mode contradicts the data — a config
// flipped behind our back, or an archive imported into the wrong instance, would silently
// make every account-scoped query answer a different question than it was written to.
func assertMode(ctx context.Context, pool *pgxpool.Pool, cfg *config.Manager) error {
	var teams int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM teams`).Scan(&teams); err != nil {
		return fmt.Errorf("boot check failed (have the migrations been applied? try `flagfish migrate`): %w", err)
	}
	return account.AssertModeAtBoot(cfg.Current().Mode, teams)
}

func run(ctx context.Context, app *fx.App) error {
	if err := app.Err(); err != nil {
		return fmt.Errorf("app: build graph: %w", err)
	}

	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := app.Start(startCtx); err != nil {
		return fmt.Errorf("app: start: %w", err)
	}

	<-ctx.Done()

	// Detach from the cancelled parent: shutdown needs a live context to drain
	// connections and in-flight jobs, and the parent is cancelled precisely because we
	// are shutting down. This is the ONE shutdown budget — fx drains every OnStop hook
	// sequentially against it (HTTP drain, then worker job drain, then pool close), so
	// it has to cover all three, not just one.
	stopCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 50*time.Second)
	defer stop()
	if err := app.Stop(stopCtx); err != nil {
		return fmt.Errorf("app: stop: %w", err)
	}
	return nil
}
