package accounts

import (
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/starvy/flagfish/internal/auth"
	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/jobs"
)

// LimiterConfig is the process-level rate-limit setting, injected so the Limiter is not
// wired to hardcoded numbers. It comes from the environment, not the config table.
type LimiterConfig struct {
	Limit  int
	Window time.Duration
}

// Module wires credentials into the graph: the Service as the auth.Authenticator, and the
// Limiter as the auth.Limiter. Both are also exposed under their concrete types, because
// the login/token HTTP handlers need methods the interfaces do not carry. NewService's
// account.Mode argument is supplied by the graph from the config snapshot.
var Module = fx.Module(
	"accounts",
	fx.Provide(
		func(pool *pgxpool.Pool, mode account.Mode, log *slog.Logger, i *jobs.Inserter) *Service {
			return NewService(pool, mode, log, WithJobs(i))
		},
		func(s *Service) auth.Authenticator { return s },
		newLimiter,
		func(l *Limiter) auth.Limiter { return l },
	),
)

func newLimiter(pool *pgxpool.Pool, c LimiterConfig) *Limiter {
	return NewLimiter(pool, c.Limit, c.Window)
}
