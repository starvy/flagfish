package config

import (
	"context"
	"log/slog"

	"go.uber.org/fx"
)

// Module wires the config layer: the Postgres-backed store and watcher, and the Manager
// that owns the atomic snapshot. The one long-running goroutine — the LISTEN/NOTIFY
// watcher — is bound to the app lifecycle here, so it starts with the process and is
// cancelled cleanly on shutdown rather than leaking past it.
var Module = fx.Module(
	"config",
	fx.Provide(
		fx.Annotate(NewPGStore, fx.As(new(Store))),
		fx.Annotate(NewPGWatcher, fx.As(new(Watcher))),
		newManager,
	),
)

// newManager loads and validates the config table once (a bad value is fatal here, at
// boot, with the key named), then registers the watcher goroutine on the lifecycle.
func newManager(root context.Context, lc fx.Lifecycle, store Store, w Watcher, log *slog.Logger) (*Manager, error) {
	m, err := New(root, store, log)
	if err != nil {
		return nil, err
	}

	// The watcher outlives every OnStart context (those are cancelled once Start
	// returns), so it runs on a context we cancel ourselves at OnStop.
	watchCtx, cancel := context.WithCancel(context.WithoutCancel(root))
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				if err := m.Run(watchCtx, w); err != nil && watchCtx.Err() == nil {
					log.Error("config watcher stopped", "error", err)
				}
			}()
			return nil
		},
		OnStop: func(context.Context) error {
			cancel()
			return nil
		},
	})
	return m, nil
}
