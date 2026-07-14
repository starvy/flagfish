// Package auth is the authentication seam: the result of authenticating a request, and
// the two interfaces the middleware chain needs.
//
// It exists as its own package because of the import rule: nothing may import
// internal/httpapi, so the transport cannot own a type that internal/accounts must
// return. Putting the seam here lets httpapi and accounts both depend on it and neither
// depend on the other.
package auth

import (
	"context"
	"net/http"

	"github.com/starvy/flagfish/internal/domain/policy"
)

// Method is how a caller proved who they are.
type Method uint8

const (
	MethodAnonymous Method = iota
	MethodCookie
	MethodToken
)

// Auth is the result of authenticating one request.
type Auth struct {
	Principal policy.Principal
	Method    Method
	// CSRFToken is the session's nonce, and it is empty for token auth.
	//
	// Token auth is CSRF-exempt because the identity came from a token — not because a
	// header happened to be present on the request. That distinction is what stops a CSRF
	// bypass by header-stuffing: an attacker who adds an Authorization header to a forged
	// cross-site request does not thereby become token-authenticated, they just fail to
	// authenticate at all.
	CSRFToken string
}

// Authenticator resolves a request to a Principal, from either credential.
//
// The single method is the shape of the fix. Resolving a session in one request hook and
// a token in another, with the ban wall running between them, lets a banned user holding
// a valid API token keep full API access including flag submission. Here there is one
// resolution point, it runs once, before any authorization, and it returns a
// fully-populated Principal whichever credential was used. A token cannot route around a
// wall a cookie hits, because by the time the wall runs it cannot tell them apart.
type Authenticator interface {
	Authenticate(ctx context.Context, r *http.Request) (Auth, error)
}

// Anonymous authenticates nobody.
//
// It is a safe placeholder: every caller is anonymous, so every gate that requires auth
// denies. A stub that failed open would be a hole that only shows up in production.
type Anonymous struct{}

func (Anonymous) Authenticate(context.Context, *http.Request) (Auth, error) {
	return Auth{Method: MethodAnonymous}, nil
}

// Limiter is the rate limit. The implementation is a Postgres row; the seam is here
// because the middleware chain is what consumes it.
type Limiter interface {
	// Allow bumps the counter for key and reports whether the caller is still under the
	// limit. It is expected to be atomic, and to fail closed.
	Allow(ctx context.Context, key string) (bool, error)
}

// NoLimit allows everything. Fine in a dev loop; the router warns at boot when it is in
// use, because a production deployment that quietly has no rate limiting is exactly the
// failure this package exists to prevent.
type NoLimit struct{}

func (NoLimit) Allow(context.Context, string) (bool, error) { return true, nil }
