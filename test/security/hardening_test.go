//go:build integration

package security

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/starvy/flagfish/internal/accounts"
	"github.com/starvy/flagfish/internal/auth"
)

// The session cookie is issued with Secure, on a server that has no idea it is behind TLS.
//
// This is the shape of every real deployment: TLS dies at a reverse proxy and the app is spoken
// to in plaintext. A Secure attribute derived from "did this request arrive over TLS", or from
// "is the peer in the trusted-proxy list", is therefore absent exactly where it matters — the
// operator who never configured proxy trust gets session cookies a browser will happily send in
// the clear, and nothing anywhere says so. Secure is on by default and only an explicit opt-out
// turns it off.
func TestS11_SessionCookieIsSecureByDefault(t *testing.T) {
	f := setup(t) // no TLS, no trusted proxies, no cookie config — the forgetful operator
	f.user("cookie-holder", pw)

	r := f.do(http.MethodPost, "/api/v1/login",
		withBody("application/json", []byte(`{"email":"cookie-holder@ctf.test","password":"`+pw+`"}`)))
	if r.StatusCode != http.StatusOK {
		t.Fatalf("login: %d, want 200 (body %s)", r.StatusCode, r.Body)
	}

	c := r.cookie(accounts.SessionCookie)
	if c == nil {
		t.Fatalf("login set no %s cookie", accounts.SessionCookie)
	}
	if !c.Secure {
		t.Error("the session cookie has no Secure attribute. Behind a TLS-terminating proxy — the " +
			"documented topology — this cookie travels over plain HTTP the moment anything downgrades, " +
			"and the operator was never told")
	}
	if !c.HttpOnly {
		t.Error("the session cookie has no HttpOnly attribute")
	}
}

// Behind a trusted proxy the rate limiter keys on the CLIENT, not on the proxy.
//
// With the proxy's address in the trusted list, X-Forwarded-For is believed and each client gets
// its own bucket. Ignore the header — which is what an unconfigured trusted-proxy list does — and
// every request behind that proxy presents the same address: one bucket for the entire internet,
// and one stranger can spend it and lock everybody out of /login.
func TestS12_RateLimitIsPerClientBehindATrustedProxy(t *testing.T) {
	const limit = 5
	f := setup(t, withLimit(limit), withTrustedProxy())

	// One client spends its whole bucket…
	for i := range limit {
		if got := f.do(http.MethodGet, "/api/v1/probe", withForwardedFor("203.0.113.7")).StatusCode; got == http.StatusTooManyRequests {
			t.Fatalf("client A was limited after only %d requests (limit %d)", i, limit)
		}
	}
	if got := f.do(http.MethodGet, "/api/v1/probe", withForwardedFor("203.0.113.7")).StatusCode; got != http.StatusTooManyRequests {
		t.Fatalf("client A: %d after %d requests, want 429 — the limiter is not limiting at all", got, limit)
	}

	// …and the client next to it still has its own.
	for i := range limit {
		if got := f.do(http.MethodGet, "/api/v1/probe", withForwardedFor("198.51.100.4")).StatusCode; got == http.StatusTooManyRequests {
			t.Fatalf("client B was rate-limited on request %d by traffic that was not theirs. "+
				"The limiter is keyed on the proxy's address, so every player behind that proxy — "+
				"which is every player — shares ONE bucket", i)
		}
	}
}

// A body no one asked for is refused with 413, before authentication.
//
// /login is anonymous and takes a body. Without an outer bound, an unauthenticated stranger
// decides how many bytes we read into memory; the multipart upload path decides how many we spill
// to the container's disk. Both are bounded, and the upload bound is the larger one — an upload
// legitimately carries megabytes, a login does not.
func TestS13_OversizedBodiesAreRefused(t *testing.T) {
	const uploadMax = 4 << 20
	f := setup(t, withMaxUpload(uploadMax))

	t.Run("an oversized JSON body on an anonymous route is a 413", func(t *testing.T) {
		body := append([]byte(`{"email":"a@b.test","password":"`), bytes.Repeat([]byte("A"), 2<<20)...)
		body = append(body, []byte(`"}`)...)

		r := f.do(http.MethodPost, "/api/v1/login", withBody("application/json", body))
		if r.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413 — an anonymous caller sized our heap for us", r.StatusCode)
		}
	})

	t.Run("an upload over the upload cap is a 413", func(t *testing.T) {
		r := f.do(http.MethodPost, "/api/v1/admin/challenges/1/files",
			withBody(multipartContentType, multipartBody(t, uploadMax+(1<<20))))
		if r.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413 — the multipart path does not go through Huma's per-operation "+
				"body cap, so an upload streams to the container's disk with nothing bounding it", r.StatusCode)
		}
	})

	t.Run("an upload under the upload cap is not size-refused", func(t *testing.T) {
		// It is refused, of course — anonymous on an admin route — but not for its size. The
		// upload cap has to be its own, larger limit, or attaching a file becomes impossible.
		r := f.do(http.MethodPost, "/api/v1/admin/challenges/1/files",
			withBody(multipartContentType, multipartBody(t, 2<<20)))
		if r.StatusCode == http.StatusRequestEntityTooLarge {
			t.Error("a 2 MiB upload was refused as too large: the upload path is sharing the JSON cap")
		}
	})
}

// A database failure during authentication is a 503, and it says nothing.
//
// The failure mode is two bugs in one line: the caller is told 401 ("your credential is bad")
// when the truth is "we could not check it", and the detail field carries the wrapped pgx error
// — our schema, our driver, our internals — to an unauthenticated stranger.
func TestS14_AuthFailureDoesNotLeakTheDatabase(t *testing.T) {
	f := setup(t, withAuthenticator(brokenAuth{}))

	r := f.do(http.MethodGet, "/api/v1/probe", withCookie("whatever"))

	if r.StatusCode == http.StatusUnauthorized {
		t.Error("a database failure during authentication answered 401. The credential was never " +
			"checked — reporting it as invalid is a lie that hides an outage")
	}
	if r.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", r.StatusCode)
	}
	for _, leak := range []string{dbErrorText, "pgx", "sessions", "SQLSTATE"} {
		if strings.Contains(r.Body, leak) {
			t.Errorf("the response body leaks %q to an unauthenticated caller:\n%s", leak, r.Body)
		}
	}
}

// The admin OpenAPI document and its reference UI are not anonymously readable.
//
// Huma registers the docs and schema through the adapter, which bypasses the middleware the
// operations themselves run — so the map of every admin endpoint answers 200 to anyone who asks,
// and asking is free reconnaissance. They belong behind the same gate as the routes they describe.
func TestS15_AdminOpenAPIIsNotAnonymous(t *testing.T) {
	f := setup(t)

	f.user("spec-admin", pw, asAdmin)
	f.user("player", pw)

	adminSess, err := f.acct.Login(context.Background(), "spec-admin@ctf.test", pw)
	if err != nil {
		t.Fatalf("login admin: %v", err)
	}
	playerSess, err := f.acct.Login(context.Background(), "player@ctf.test", pw)
	if err != nil {
		t.Fatalf("login player: %v", err)
	}

	paths := []string{
		"/api/v1/admin/openapi.json",
		"/api/v1/admin/openapi.yaml",
		"/api/v1/admin/docs",
		// Huma builds its doc routes from the configured path, and a mounted sub-router made that
		// path a prefixed duplicate. It was the reachable one, so it is pinned here too.
		"/api/v1/admin/api/v1/admin/openapi.json",
	}

	for _, p := range paths {
		t.Run("anonymous "+p, func(t *testing.T) {
			r := f.do(http.MethodGet, p)
			if r.StatusCode == http.StatusOK {
				t.Errorf("%s served 200 to an anonymous caller — the entire admin API, "+
					"endpoint by endpoint, for free", p)
			}
			if strings.Contains(r.Body, "openapi") || strings.Contains(r.Body, "admin-upload-file") {
				t.Errorf("%s leaked spec content to an anonymous caller:\n%s", p, snippet(r.Body))
			}
		})

		t.Run("player "+p, func(t *testing.T) {
			if got := f.do(http.MethodGet, p, withCookie(playerSess.ID)).StatusCode; got == http.StatusOK {
				t.Errorf("%s served 200 to a non-admin player", p)
			}
		})
	}

	// And it is still served — to an admin. A guard that works by deleting the feature is not a
	// guard, and the next person to "fix" the 404 would put it back the way it was.
	r := f.do(http.MethodGet, "/api/v1/admin/openapi.json", withCookie(adminSess.ID))
	if r.StatusCode != http.StatusOK {
		t.Fatalf("admin reading the admin spec: %d, want 200", r.StatusCode)
	}
	if !strings.Contains(r.Body, "openapi") {
		t.Errorf("the admin spec does not look like an OpenAPI document:\n%s", snippet(r.Body))
	}
}

// dbErrorText stands in for the wrapped driver error an outage produces. It is deliberately
// distinctive: the assertion is that no part of it reaches the wire.
const dbErrorText = "connection refused: dial tcp 10.0.0.5:5432 (db=flagfish table=sessions)"

// brokenAuth is the database being down underneath authentication: not "this credential is
// invalid", but "we could not find out".
type brokenAuth struct{}

func (brokenAuth) Authenticate(context.Context, *http.Request) (auth.Auth, error) {
	return auth.Auth{}, fmt.Errorf("accounts: session lookup: %w", errors.New(dbErrorText))
}

const multipartContentType = "multipart/form-data; boundary=" + multipartBoundary

const multipartBoundary = "flagfishtestboundary"

// multipartBody builds a multipart upload of roughly n bytes.
func multipartBody(t *testing.T, n int) []byte {
	t.Helper()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.SetBoundary(multipartBoundary); err != nil {
		t.Fatalf("boundary: %v", err)
	}
	part, err := w.CreateFormFile("file", "payload.bin")
	if err != nil {
		t.Fatalf("form file: %v", err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("A"), n)); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return buf.Bytes()
}

func snippet(s string) string {
	const max = 300
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
