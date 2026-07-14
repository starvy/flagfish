// Package audit carries the acting admin into the database, where the capture triggers turn every
// row change into an audit_log entry. The trigger knows what changed but not who; this package is
// the who. The read API over audit_log will live here too.
package audit

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// Actor is the authenticated admin performing a mutation.
type Actor struct {
	ID int64
	IP *netip.Addr
}

type ctxKey struct{}

// WithActor attaches the actor for writers that cannot take one as a parameter (the config store's
// upsert runs behind an interface that predates auditing).
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, ctxKey{}, a)
}

// ActorFrom returns the attached actor, if any.
func ActorFrom(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(ctxKey{}).(Actor)
	return a, ok
}

// Stamp records the actor on the transaction. set_config(..., true) is SET LOCAL: it dies with the
// transaction, so a pooled connection cannot leak one admin's identity into the next request.
func Stamp(ctx context.Context, tx pgx.Tx, a Actor) error {
	ip := ""
	if a.IP != nil {
		ip = a.IP.String()
	}
	_, err := tx.Exec(ctx,
		`SELECT set_config('app.actor_id', $1, true), set_config('app.ip', $2, true)`,
		strconv.FormatInt(a.ID, 10), ip)
	if err != nil {
		return fmt.Errorf("audit: stamp actor %d: %w", a.ID, err)
	}
	return nil
}
