package httpapi

import (
	"context"

	"github.com/starvy/flagfish/internal/auth"
	"github.com/starvy/flagfish/internal/domain/policy"
)

// The seam itself lives in internal/auth, because nothing may import internal/httpapi —
// so the transport cannot own a type that internal/accounts has to return. These aliases
// keep the call sites in this package readable.
type (
	Auth          = auth.Auth
	Method        = auth.Method
	Authenticator = auth.Authenticator
	Limiter       = auth.Limiter
)

const (
	MethodAnonymous = auth.MethodAnonymous
	MethodCookie    = auth.MethodCookie
	MethodToken     = auth.MethodToken
)

// --- request-scoped values -------------------------------------------------

type ctxKey uint8

const (
	ctxAuth ctxKey = iota
	ctxClass
	ctxPolicy
	ctxClientIP
	ctxSecure
	ctxServedTLS
	ctxRequestID
)

// AuthOf returns the authenticated caller. It is always present downstream of the auth
// middleware.
func AuthOf(ctx context.Context) Auth {
	a, ok := ctx.Value(ctxAuth).(Auth)
	if !ok {
		return Auth{} // anonymous: no auth middleware ran on this path
	}
	return a
}

// ClassOf returns the route's policy class. ClassUnknown means the route forgot to
// declare one — which Decide treats as a denial, on purpose.
func ClassOf(ctx context.Context) policy.RouteClass {
	c, ok := ctx.Value(ctxClass).(policy.RouteClass)
	if !ok {
		return policy.ClassUnknown
	}
	return c
}

// PolicyOf returns the exact Policy that was evaluated for this request.
//
// Handlers must not rebuild it. The L2 resource guards, the freeze exemption and the
// field redaction all read THIS value, so the gate, the query and the serializer answer
// to one decision rather than to three reconstructions of it that can drift apart.
func PolicyOf(ctx context.Context) policy.Policy {
	p, ok := ctx.Value(ctxPolicy).(policy.Policy)
	if !ok {
		return policy.Policy{} // the gate did not run; the zero Policy denies
	}
	return p
}
