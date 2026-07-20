package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"go.uber.org/fx"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/anticheat"
	"github.com/starvy/flagfish/internal/board"
	"github.com/starvy/flagfish/internal/catalog"
	"github.com/starvy/flagfish/internal/config"
	"github.com/starvy/flagfish/internal/files"
	"github.com/starvy/flagfish/internal/gameplay"
	"github.com/starvy/flagfish/internal/jobs"
	"github.com/starvy/flagfish/internal/metrics"
	"github.com/starvy/flagfish/internal/notify"
)

// ListenAddr is the TCP address the server binds. It is a named type so the graph can
// supply it without colliding with every other string in the process.
type ListenAddr string

// Module builds the router from its injected dependencies and binds the listener on the
// app lifecycle. Auth and Limiter are required here — the router tolerates nil by falling
// back to anonymous/no-limit, but a wired process should never take that path, so the
// graph makes them mandatory and a missing one fails validation, not production.
var Module = fx.Module(
	"httpapi",
	fx.Provide(newServer),
	fx.Invoke(runServer),
)

type serverParams struct {
	fx.In

	Config         *config.Manager
	Env            config.Env
	Auth           Authenticator
	Limiter        Limiter
	AuthLimiter    *accounts.AuthLimiter
	Log            *slog.Logger
	Accounts       *accounts.Service
	Gameplay       *gameplay.Service
	Catalog        *catalog.Service
	Board          *board.Service
	AdminOps       *adminops.Service
	Notify         *notify.Service
	Broadcaster    *notify.Broadcaster
	Anticheat      *anticheat.Service
	Files          *files.Service
	Metrics        *metrics.Metrics
	Jobs           *jobs.Inserter `optional:"true"`
	TrustedProxies []*net.IPNet   `optional:"true"`
}

//nolint:gocritic // hugeParam: fx constructs this once at wiring time; the params struct is built to be passed by value.
func newServer(p serverParams) *Server {
	opts := Options{
		Config:         p.Config,
		Auth:           p.Auth,
		Limiter:        p.Limiter,
		AuthLimiter:    p.AuthLimiter,
		Log:            p.Log,
		Accounts:       p.Accounts,
		Gameplay:       p.Gameplay,
		Catalog:        p.Catalog,
		Board:          p.Board,
		AdminOps:       p.AdminOps,
		Notify:         p.Notify,
		Broadcaster:    p.Broadcaster,
		Anticheat:      p.Anticheat,
		Files:          p.Files,
		Metrics:        p.Metrics,
		TrustedProxies: p.TrustedProxies,

		// The env says "secure cookies"; the router takes the inverted flag so that its zero
		// value — a caller that never thought about it — is the secure one.
		InsecureCookies: !p.Env.SecureCookies,
		MaxUploadBytes:  p.Env.MaxUploadBytes,
	}
	// Assigned only when wired: a nil *jobs.Inserter placed in the interface field would read as a
	// non-nil interface and defeat the nil-guard on the enqueue path.
	if p.Jobs != nil {
		opts.Jobs = p.Jobs
	}
	return New(opts)
}

func runServer(lc fx.Lifecycle, s *Server, addr ListenAddr, log *slog.Logger) {
	srv := &http.Server{
		Addr:              string(addr),
		Handler:           s.Router,
		ReadHeaderTimeout: 10 * time.Second,
		// ReadTimeout bounds a slow-body client; IdleTimeout reaps parked keep-alive
		// connections. WriteTimeout is deliberately OFF: it caps the time to write the whole
		// response, which would sever the long-lived SSE stream. The per-response bound is
		// instead a per-request deadline on the JSON surfaces (see router.go), which the
		// stream is excluded from.
		ReadTimeout: 30 * time.Second,
		IdleTimeout: 120 * time.Second,
	}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			// Bind synchronously so an "address already in use" surfaces as a failed Start
			// — the caller gets the error — instead of vanishing into a goroutine's log.
			var listenCfg net.ListenConfig
			ln, err := listenCfg.Listen(ctx, "tcp", srv.Addr)
			if err != nil {
				return fmt.Errorf("httpapi: listen %s: %w", srv.Addr, err)
			}
			log.Info("serving", "addr", srv.Addr)
			go func() {
				if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Error("http server stopped", "error", err)
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			shutdown, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdown); err != nil {
				return fmt.Errorf("httpapi: graceful shutdown failed: %w", err)
			}
			return nil
		},
	})
}
