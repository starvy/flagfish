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

// The credential routes carry three budgets, not one, because "how many credential requests may
// this source make" is the wrong question on its own: a whole venue behind one NAT is one source,
// and an attacker with a botnet is a thousand. Each of these limits a different axis; all three are
// the same Postgres-counter mechanism, and the bucket namespaces that keep them apart are applied
// by the caller.

// AuthLimiter is the strict budget on failed attempts against one credential target — the account
// named by a submitted email, or a submitted token. This is the brute-force guard proper: guessing
// one account is throttled no matter how many addresses the guesses arrive from.
type AuthLimiter struct{ *Limiter }

// AuthFailureLimiter is the strict budget on failed credential attempts from one source address.
// It catches the spray that a per-target budget cannot see — one guess each against a thousand
// accounts — while costing a shared address nothing, because a successful attempt is refunded.
type AuthFailureLimiter struct{ *Limiter }

// AuthIPLimiter is the blunt, generous budget on all credential traffic from one source address,
// successful or not. It is a resource guard, not a brute-force guard: password verification is
// deliberately expensive, so the number of times a stranger can make us do it has to be bounded.
type AuthIPLimiter struct{ *Limiter }

func NewAuthLimiter(pool *pgxpool.Pool, limit int, window time.Duration) *AuthLimiter {
	return &AuthLimiter{NewLimiter(pool, limit, window)}
}

func NewAuthFailureLimiter(pool *pgxpool.Pool, limit int, window time.Duration) *AuthFailureLimiter {
	return &AuthFailureLimiter{NewLimiter(pool, limit, window)}
}

func NewAuthIPLimiter(pool *pgxpool.Pool, limit int, window time.Duration) *AuthIPLimiter {
	return &AuthIPLimiter{NewLimiter(pool, limit, window)}
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

// Refund gives back one bump, so a budget can be spent before an outcome is known and returned
// once it is. This is how a failure counter stays atomic: there is still no read-modify-write, only
// two atomic writes in opposite directions.
//
// It never grants credit that was not taken — the UPDATE matches nothing when the row is absent,
// and the counter floors at zero — so a stray refund cannot widen anyone's budget.
func (l *Limiter) Refund(ctx context.Context, key string) error {
	start := time.Now().Truncate(l.window)

	if err := l.q.RefundRateLimit(ctx, db.RefundRateLimitParams{
		Bucket:      key,
		WindowStart: pgtype.Timestamptz{Time: start, Valid: true},
	}); err != nil {
		return fmt.Errorf("accounts: rate limit refund: %w", err)
	}
	return nil
}
