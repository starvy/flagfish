package notify

import (
	"context"
	"log/slog"

	"go.uber.org/fx"
)

// Module wires the notifications bus: the publish/read service and the broadcaster, plus the one
// long-running goroutine — the LISTEN pump — bound to the app lifecycle so it starts with the
// process and is cancelled cleanly on shutdown rather than leaking past it.
var Module = fx.Module(
	"notify",
	fx.Provide(NewService, NewBroadcaster),
	fx.Invoke(runBroadcaster),
)

func runBroadcaster(root context.Context, lc fx.Lifecycle, b *Broadcaster, log *slog.Logger) {
	// The pump outlives every OnStart context (those are cancelled once Start returns), so it runs
	// on a context we cancel ourselves at OnStop.
	pumpCtx, cancel := context.WithCancel(context.WithoutCancel(root))
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				if err := b.Run(pumpCtx); err != nil && pumpCtx.Err() == nil {
					log.Error("notify broadcaster stopped", "error", err)
				}
			}()
			return nil
		},
		OnStop: func(context.Context) error {
			// Stop the pump, then close every subscriber so the SSE handlers return at once. This
			// hook must run before the HTTP server drains, or those handlers block the drain until
			// its timeout — which is why this module is wired after httpapi in the serve graph.
			cancel()
			b.closeAll()
			return nil
		},
	})
}
