package accounts

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/starvy/flagfish/internal/auth"
	"github.com/starvy/flagfish/internal/db"
)

// Limiter is a fixed-window counter backed by a Postgres row.
//
// It is atomic by construction, which is the whole point. A counter that is only atomic
// on Redis stops limiting silently on any other backend while the endpoint keeps
// returning 200, so the operator has no way to know the brute-force guard is off.
//
// Here the increment is INSERT … ON CONFLICT DO UPDATE SET n = n + 1 RETURNING n. Two
// concurrent bumps serialize on the primary key and both observe a correct value; there
// is no increment to lose, because there is no read-modify-write.
type Limiter struct {
	q      *db.Queries
	limit  int32
	window time.Duration
}

func NewLimiter(pool *pgxpool.Pool, limit int, window time.Duration) *Limiter {
	return &Limiter{q: db.New(pool), limit: int32(limit), window: window} //nolint:gosec // a configured limit
}

var _ auth.Limiter = (*Limiter)(nil)

// Allow bumps the caller's counter and reports whether they are still under the limit.
//
// It fails closed. If the database cannot be reached the caller is denied, not
// allowed. A rate limiter that fails open is a rate limiter that disappears exactly when
// the system is under the load it exists to shed — which is the moment an attacker is
// most likely to have caused.
func (l *Limiter) Allow(ctx context.Context, key string) (bool, error) {
	// Truncating to the window makes the window part of the primary key, so a new window
	// is a new row rather than a reset someone has to remember to perform.
	start := time.Now().Truncate(l.window)

	n, err := l.q.BumpRateLimit(ctx, db.BumpRateLimitParams{
		Bucket:      key,
		WindowStart: pgtype.Timestamptz{Time: start, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("accounts: rate limit: %w", err)
	}
	return n <= l.limit, nil
}
