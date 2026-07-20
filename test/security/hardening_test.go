//go:build integration

package security

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"regexp"
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

	t.Run("a body is bounded even on a route that declares none", func(t *testing.T) {
		// Huma bounds the body of an operation that reads one. An operation that reads none has
		// nothing to bound, so without an outer limit the bytes are ours to swallow regardless of
		// what the route says it accepts.
		r := f.do(http.MethodPost, "/api/v1/probe",
			withBody("application/json", bytes.Repeat([]byte("A"), 2<<20)))
		if r.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413 — the body limit has to sit in front of the router, "+
				"not inside the operations that happen to read a body", r.StatusCode)
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

// One id is one bucket, however the id is spelled.
//
// chi matches any segment against {id} and Huma parses it with strconv.ParseInt, which accepts
// leading zeros and a sign — so /probe/7, /probe/007 and /probe/+7 are all id 7. Key the limiter on
// the raw path and each spelling is a fresh budget against the same row, out of a padding space with
// no end. On a challenge that sets no max_attempts — the default — the limiter is the only thing
// between a script and the flag, so a bucket per spelling is a brute-force multiplier, not a
// cosmetic bug.
func TestS16_RateLimitBucketIsTheRouteNotTheRawPath(t *testing.T) {
	const limit = 5
	f := setup(t, withLimit(limit))

	// Spend the whole budget for id 7.
	for i := range limit {
		if got := f.do(http.MethodGet, "/api/v1/probe/7").StatusCode; got == http.StatusTooManyRequests {
			t.Fatalf("limited after only %d requests (limit %d)", i, limit)
		}
	}
	if got := f.do(http.MethodGet, "/api/v1/probe/7").StatusCode; got != http.StatusTooManyRequests {
		t.Fatalf("id 7: %d after %d requests, want 429 — the limiter is not limiting at all", got, limit)
	}

	// Every other spelling of 7 is the same bucket, already spent.
	for _, padded := range []string{"/api/v1/probe/007", "/api/v1/probe/0000000007", "/api/v1/probe/+7"} {
		if got := f.do(http.MethodGet, padded).StatusCode; got != http.StatusTooManyRequests {
			t.Errorf("%s: %d, want 429 — it is the same id as /probe/7, whose budget is spent. "+
				"Padding the id mints a fresh bucket, so the limit on a flag submission is "+
				"limit × however many zeros an attacker cares to type", padded, got)
		}
	}

	// And the per-id budget still exists: a different id is a different bucket, or the fix would
	// have "worked" by collapsing every challenge into one limit.
	if got := f.do(http.MethodGet, "/api/v1/probe/8").StatusCode; got == http.StatusTooManyRequests {
		t.Errorf("id 8: %d — a different id must have its own budget", got)
	}
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

// A stored secret never appears in any admin GET body.
//
// mail_server/mail_username/mail_password and webhook_url are credentials an admin hands the
// instance; the round-trip contract is set-only. The sweep is driven by the served admin OpenAPI
// document, so an admin GET added later is covered without anyone remembering this test exists —
// and the assertion runs on the raw bytes, because "the field is absent" says nothing about a
// secret embedded in a message, a problems list, or an error.
func TestS17_AdminGETsNeverEchoASetSecret(t *testing.T) {
	f := setup(t)
	f.user("secret-admin", pw, asAdmin)
	sess, err := f.acct.Login(context.Background(), "secret-admin@ctf.test", pw)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	auth := []func(*http.Request){withCookie(sess.ID)}

	secrets := []string{
		"smtp.s17-secret-host.example",
		"s17-mailer-username",
		"s17-hunter2-password",
		"https://discord.example/api/webhooks/99/s17-token-value",
	}
	patch := `{
		"mail_server":   "` + secrets[0] + `",
		"mail_username": "` + secrets[1] + `",
		"mail_password": "` + secrets[2] + `",
		"mail_port":     587,
		"mailfrom_addr": "noreply@ctf.test",
		"webhook_url":   "` + secrets[3] + `",
		"webhook_enabled": true
	}`
	r := f.do(http.MethodPatch, "/api/v1/admin/config",
		withBody("application/json", []byte(patch)), withCookie(sess.ID), withCSRF(sess.CSRFToken))
	if r.StatusCode != http.StatusOK {
		t.Fatalf("seed secrets via PATCH: %d (%s)", r.StatusCode, r.Body)
	}

	spec := f.do(http.MethodGet, "/api/v1/admin/openapi.json", auth...)
	if spec.StatusCode != http.StatusOK {
		t.Fatalf("admin openapi: %d", spec.StatusCode)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal([]byte(spec.Body), &doc); err != nil {
		t.Fatalf("decode admin openapi: %v", err)
	}

	pathParam := regexp.MustCompile(`\{[^}]+\}`)
	swept := 0
	for p, ops := range doc.Paths {
		if _, ok := ops["get"]; !ok {
			continue
		}
		swept++
		url := "/api/v1/admin" + pathParam.ReplaceAllString(p, "1")
		body := f.do(http.MethodGet, url, auth...).Body
		for _, secret := range secrets {
			if strings.Contains(body, secret) {
				t.Errorf("GET %s echoes the stored secret %q:\n%s", url, secret, snippet(body))
			}
		}
	}
	if swept == 0 {
		t.Fatal("the admin OpenAPI document lists no GET operations — the sweep swept nothing")
	}

	// The sweep must include the config endpoint itself, or a doc regression could
	// quietly reduce this test to sweeping nothing that matters.
	if _, ok := doc.Paths["/config"]; !ok {
		t.Error("/config is missing from the admin OpenAPI document")
	}
}

// Every response carries the hardening headers, whatever the route and whatever the status.
//
// The SPA serves author-supplied markdown and user-supplied team names, so it is an XSS surface;
// the headers are the standing defense, and they are only a defense if they are actually present —
// on the 200, on the 401, on the 404, not just on a handful of routes someone remembered. The
// sweep hits a spread of routes and statuses and asserts the header set on each; removing the
// middleware makes all of them go red at once.
func TestS23_SecurityHeadersOnEveryResponse(t *testing.T) {
	f := setup(t)
	f.user("header-holder", pw)

	// A deliberate spread: static health (200), an anonymous JSON route (401 at the auth wall),
	// an anonymous POST that reaches a handler, and an unrouted path (the SPA fallback).
	routes := []struct {
		method, path string
	}{
		{http.MethodGet, "/healthz"},
		{http.MethodGet, "/api/v1/probe"},
		{http.MethodPost, "/api/v1/login"},
		{http.MethodGet, "/definitely-not-a-route"},
	}

	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			r := f.do(rt.method, rt.path)

			if got := r.Header.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
			if got := r.Header.Get("X-Frame-Options"); got != "DENY" {
				t.Errorf("X-Frame-Options = %q, want DENY", got)
			}
			if got := r.Header.Get("Referrer-Policy"); got == "" {
				t.Error("Referrer-Policy is absent")
			}
			csp := r.Header.Get("Content-Security-Policy")
			if csp == "" {
				t.Fatal("Content-Security-Policy is absent")
			}
			// script-src must be exactly 'self'. A CSP whose script-src is widened to
			// 'unsafe-inline' or 'unsafe-eval' is a CSP that no longer stops the injected
			// <script>, which is the whole reason it is here.
			if v := cspDirective(csp, "script-src"); v != "'self'" {
				t.Errorf("script-src = %q, want 'self' exactly — widening it forfeits the XSS defense", v)
			}
			// And the directives the embedded SPA genuinely needs, so a future tightening that
			// would blank the page is caught here rather than in a browser: the data: favicon,
			// same-origin fetch + EventSource, and inline styles React writes.
			if v := cspDirective(csp, "img-src"); !strings.Contains(v, "data:") {
				t.Errorf("img-src = %q, want data: — the SPA favicon is a data: URI", v)
			}
			if v := cspDirective(csp, "connect-src"); !strings.Contains(v, "'self'") {
				t.Errorf("connect-src = %q, want 'self' — the API and the SSE stream are same-origin", v)
			}
			if v := cspDirective(csp, "style-src"); !strings.Contains(v, "'unsafe-inline'") {
				t.Errorf("style-src = %q, want 'unsafe-inline' — React writes inline styles", v)
			}
		})
	}
}

// HSTS is sent only on a secure connection — and this server has no idea it is behind TLS.
//
// Announcing Strict-Transport-Security on a deployment that legitimately serves plain HTTP would
// pin browsers to a scheme it does not speak. So the header is gated on the same Secure signal as
// the session cookie: on by TLS or a trusted proxy's X-Forwarded-Proto, off otherwise. Both
// directions are asserted, because a header that is always present and a header that is never
// present both defeat the point.
func TestS23b_HSTSOnlyWhenSecure(t *testing.T) {
	t.Run("absent on a plain-HTTP request", func(t *testing.T) {
		f := setup(t) // no TLS, no trusted proxy
		if got := f.do(http.MethodGet, "/healthz").Header.Get("Strict-Transport-Security"); got != "" {
			t.Errorf("HSTS = %q on a plain-HTTP server — browsers would be pinned to a scheme it cannot serve", got)
		}
	})

	t.Run("present behind a trusted TLS-terminating proxy", func(t *testing.T) {
		f := setup(t, withTrustedProxy())
		r := f.do(http.MethodGet, "/healthz", withHeader("X-Forwarded-Proto", "https"))
		if got := r.Header.Get("Strict-Transport-Security"); got == "" {
			t.Error("HSTS is absent on a request forwarded as https by a trusted proxy — the documented topology")
		}
	})
}

// cspDirective returns the value of a single CSP directive (everything after the name up to the
// next ';'), or "" if the directive is absent.
func cspDirective(csp, name string) string {
	for _, d := range strings.Split(csp, ";") {
		d = strings.TrimSpace(d)
		if rest, ok := strings.CutPrefix(d, name+" "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
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
