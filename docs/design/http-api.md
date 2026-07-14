# The HTTP API layer

This document describes `internal/httpapi`: the transport that turns a request into a
policy decision and a handler call. It owns the router, the middleware chain, the two Huma
APIs, the seam a feature package uses to publish an endpoint, and the handler slices that
are wired through it today.

It is the implementation-level companion to
[_arch/03 — HTTP / API architecture](_arch/03-api.md), which states the wire contract, the
API surface, and the reasoning behind the middleware order. Read that one for *why*; read
this one to change the code.

## 1. Overview and layering

The process is a straight dependency line, and it only points one way:

```
cmd/flagfish ─▶ internal/app ─▶ internal/httpapi ─▶ internal/auth        (the seam)
                    │                              internal/domain/policy (pure)
                    ├─▶ internal/accounts ────────▶ internal/auth
                    ├─▶ internal/catalog   (read: challenge board)
                    ├─▶ internal/board     (read: scoreboard)
                    ├─▶ internal/gameplay  (write: the hot path)
                    ├─▶ internal/adminops  (admin CRUD + the audit read side)
                    ├─▶ internal/anticheat, internal/files, internal/notify
                    └─▶ internal/config, internal/db, internal/jobs
```

Three tiers:

- **Transport** — `internal/httpapi`. Chi router, middleware, the two `huma.API` instances,
  the `Register` seam, and the handler slices. It never opens a database connection; every
  dependency it needs arrives as an interface, a config snapshot, or a feature service
  (see `httpapi.Options`) — the services own their pools.
- **Feature services** — `internal/accounts`, `internal/catalog`, `internal/board`,
  `internal/gameplay`, `internal/adminops`, `internal/anticheat`, `internal/files`,
  `internal/notify`. These own SQL (via sqlc `db.Queries`), transactions, and the write
  hot path. They return plain Go values and domain errors; they know nothing about HTTP.
- **Domain** — `internal/domain/...`. Pure types and pure functions. `policy.Decide` is one
  such function; it takes one struct and returns one `Outcome`. It imports nothing that does
  I/O, which is what lets its decision table be tested exhaustively and fast.

**The import boundary is the load-bearing rule: nothing imports `internal/httpapi` except
`cmd/` and `internal/app`.** The transport is a leaf. A feature package that needed to
return an HTTP type would drag the whole transport into the domain, so it cannot — which is
exactly why the authentication result type lives in `internal/auth` and not here (more in
§6). `httpapi` depending on a feature service is fine; a feature service depending on
`httpapi` is the cycle the layering forbids.

## 2. Two surfaces, one reason

`Server` holds two Huma APIs, mounted on two paths in `New`:

```go
s.Public = s.newAPI(gated, "/api/v1",       "flagfish",         policy.SurfacePublic)
s.Admin  = s.newAPI(gated, "/api/v1/admin", "flagfish (admin)", policy.SurfaceAdmin)
```

The split exists because **a response whose shape varies by role cannot be typed in
OpenAPI, but one that varies by URL can.** If `GET /challenges` returned extra fields when
an admin called it, the generated TypeScript client would have one type that is sometimes
one thing and sometimes another, and every consumer would have to guess. Moving the admin
view onto its own path turns "who is calling" into "which URL" — a distinction the schema
can express and the client generator can honour. This collapses the bulk of the
role-varying field-mask views; the caller-dependent splits that remain (self vs. other,
locked vs. unlocked) are the genuinely per-request ones.

It also makes `policy.Surface` honest. A handler does not *assert* which surface it is on —
that would be a claim it could get wrong. The surface is **which mux the request arrived
on**, decided once at mount time in `newAPI` and carried into every policy decision for that
API. `SurfacePublic` vs. `SurfaceAdmin` is therefore structural, and things keyed on it (the
freeze exemption, the admin-only route classes) cannot be widened by editing a default
argument, because there is no default argument to edit. Every slice shipped today registers
on `srv.Public`; `srv.Admin` is the mounted-and-ready home for the admin views.

## 3. The middleware chain, in order

Built in `New` (`router.go`) and defined in `middleware.go`. The order is not decoration;
each step depends on the one before it having run.

Outside the wall — these run before authentication because they must also work when it
fails, and they are mounted so that health and static assets never enter the gated subtree
at all:

1. **`chimw.RequestID`** — stamps a request id first, so every later log line and error can
   carry it.
2. **`realIP(trustedProxies)`** — resolves the client address, honouring `X-Forwarded-For`
   *only* from a configured trusted proxy and otherwise believing the socket peer. It also
   stashes the parsed address (`ctxClientIP`) and whether the request arrived over TLS
   (`ctxSecure`, from `r.TLS` or a trusted-proxy `X-Forwarded-Proto`) for handlers to read.
   This is not cosmetic: `submissions.ip` is recorded anti-cheat evidence, and chi's stock
   `RealIP` trusts the header from anyone, letting a client forge its own recorded address.
   Empty trusted list means trust nobody — a wrong-but-honest IP beats a forged one.
3. **`recoverer`** — turns a panic into a 500 problem document and one log line, keeping the
   process up. It captures the context explicitly because a handler may have replaced the
   request.
4. **`logging`** — one structured line per request (method, path, status, duration, ip),
   wrapping the response writer to capture the final status.

Behind the wall (the `r.Group` in `New`):

5. **`authenticate`** — resolves the `Principal` from *either* credential, once, and stashes
   the `Auth` in the context. Everything downstream reads that one resolution.
6. **`banWall`** — reads `Principal.Banned` / `Principal.TeamBanned` and rejects. It sits
   *immediately after* authentication and *before* anything else, so it covers both cookie
   and token auth — it cannot tell them apart, so it cannot fail to cover one. (This is the
   fix for the classic bypass where a banned user's API token still submits flags because the
   ban check ran before the token was resolved.)
7. **`csrf`** — enforced for cookie auth on unsafe methods only. Token auth is exempt, and
   safe methods (`GET/HEAD/OPTIONS/TRACE`) are exempt. Reasoning in §6.
8. **`rateLimit`** — keys on the account when authenticated, on the client IP otherwise, so
   one team behind a NAT is limited per-account rather than per-source. The limiter must fail
   **closed**: a limiter that errors returns 503, never an open door.
9. **`policyGate`** — the Huma-level route policy gate, §4. It is last because it needs the
   fully-resolved principal and the operation's declared class, both of which are only
   available here.

Health (`/healthz`) and static assets are mounted *outside* the `r.Group`, so their
ban-exemption is structural: a banned user must still load the CSS that renders the page
telling them they are banned, and "exempt" is "not behind the wall" rather than a list of
paths someone has to remember to maintain.

## 4. The policy gate and RouteClass

`policyGate` (in `router.go`) is a **Huma** middleware, not a chi one, because only Huma
knows which operation is being dispatched, and the operation is what carries the declared
`RouteClass`. Putting the gate at the chi level would mean a second place a route could be
denied, and two gates that must agree eventually disagree — so there is exactly one.

For each request the gate builds a `policy.Policy` from three sources it already has: the
config snapshot (`s.opts.Config.Current().Event(now)`), the authenticated caller
(`AuthOf(ctx)`), and the request shape — the operation's class, the surface this API was
mounted with, and the `?view=admin` / `?preview` query flags. It calls `policy.Decide`. On a
denial it writes the status (defaulting to 403) as an RFC 7807 problem via `huma.WriteErr`,
carrying the `Location` header when the decision wants a redirect. On allow it threads the
evaluated `Policy` and `RouteClass` back into the context (`ctxPolicy`, `ctxClass`) so the
handler's own guards and the serializer read the *same* decision instead of rebuilding it.

**`RouteClass` is the whole L1 surface.** `internal/domain/policy/routeclass.go` enumerates
every policy-relevant endpoint identity — `ClassChallengeAttempt`, `ClassScoreboard`,
`ClassTokens`, `ClassAdmin`, and the rest — and maps each to a `classAttrs` row of gate
attributes (requires-auth, requires-verified, time-gated, admin-only, ban-exempt, and so on).
The table is data, not code, so adding a route is a reviewable one-line diff, and `AllClasses`
lets a golden test walk every class and keep the table honest.

**A missing declaration is a denial.** An operation carries its class in
`op.Metadata[metadataClass]`; `classOfOperation` reads it back and returns `ClassUnknown` if
it is absent or the wrong type. `Decide`'s first check is `if r.Class == ClassUnknown` →
fail closed. So a route that forgot to declare itself is not "ungated" — it is denied. This
is the deliberate inverse of the decorator-per-route model, where a forgotten annotation
silently publishes an endpoint. Here forgetting is loud, and it is loud at the gate rather
than in production.

## 5. The Register seam and the handler slices

A feature package publishes an operation through `httpapi.Register`, not `huma.Register`:

```go
func Register[I, O any](
    api huma.API,
    class policy.RouteClass,
    op huma.Operation,
    handler func(context.Context, *I) (*O, error),
)
```

The `RouteClass` is a **required positional argument**, not an option with a zero value. An
endpoint that has not said what it is cannot be gated, and an ungated endpoint is "a
vulnerability with an OpenAPI schema" — so the type system makes you state the class to
register at all. `Register` stamps the class into a copy of the operation's `Metadata`
(the operation is taken by value on purpose, so it never mutates the caller's struct) and
then delegates to `huma.Register`. That stamped metadata is what `policyGate` reads back.

**The slices are wired through `registerRoutes`.** `New` calls `s.registerRoutes()` after
building both APIs. That method (`handlers.go`) fans out to one `registerX` method per
feature slice, each guarded by a nil-check on the service it drives:

```go
func (s *Server) registerRoutes() {
    s.registerPublic()                                    // branding/instance: always present
    if s.opts.Accounts != nil { s.registerAuth(); s.registerTokens(); s.registerTeams() }
    if s.opts.Catalog  != nil { s.registerChallenges() }
    if s.opts.Gameplay != nil { s.registerSubmit() }
    if s.opts.Board    != nil { s.registerScoreboard() }
    if s.opts.Files    != nil { s.registerFiles() }
    if s.opts.AdminOps != nil { s.registerAdminChallenges(); s.registerAdminUsers(); … }
    // … and so on, one guarded slice per service; each else-branch logs a warning at boot.
}
```

The nil-guard is the same fail-safe the router applies to a missing `Auth` or `Limiter`: a
slice whose service was not wired is left unregistered (with a warning) rather than registered
against a nil pointer that would panic on first call. That is also what makes the OpenAPI emit
work against nil pools — see §8. The services themselves arrive on `Options` (`Accounts`,
`Catalog`, `Board`, `Gameplay`, `AdminOps`, `Anticheat`, `Files`, `Notify` + `Broadcaster`) and
are supplied by the composition root through fx (`serverParams` in `fx.go`).

Each `registerX` method makes a series of `httpapi.Register` calls, one per operation, with
typed input/output structs. `handlers_auth.go` registers `POST /register`, `/login`,
`/logout`, `GET /me`, `POST /me/password`; `handlers_tokens.go` the token CRUD;
`handlers_teams.go` enrollment; `handlers_challenges.go` the board reads; `handlers_submit.go`
the writes; `handlers_scoreboard.go` the standings; `handlers_files.go` artifact download;
`handlers_notifications.go` the notification list and the SSE stream; and the
`handlers_admin_*.go` files the admin surface (challenges, users, brackets, tags, config,
files, notifications, audit, anti-cheat). Huma derives the OpenAPI schema and the request
validation from those structs, so a handler body receives an already-validated `*I` and the
wire contract is generated, never hand-maintained.

**Request-scoped helpers** keep the handlers thin (`auth.go`, `handlers.go`): `AuthOf` for
the caller, `PolicyOf` for the evaluated decision, `ClassOf` for the route class, plus
`clientIPOf` and `secureOf` reading the values `realIP` stashed. `s.actor(ctx)` assembles a
`gameplay.Actor` from the principal, the instance mode (team vs. user attribution), and the
client IP — the one place the write handlers translate a request into a play actor.

**The read/write split lives at this seam.** `internal/catalog` and `internal/board` are the
read services: catalog does challenge listing, detail, and the per-challenge solve list;
board reads the append-only score ledger for the standings. Both hold no locks and mutate
nothing. `internal/gameplay` owns the write hot path: `Submit` runs the flag compare, solve
insert, first-blood detection and announcement enqueue in one transaction, and takes the
challenge row lock **lazily** — only after the flag has compared correct — because ~99% of
submissions are wrong and must never serialize on the hottest challenge's lock. The read
handlers register on `SurfacePublic` and are plain queries; the write handlers
(`ClassChallengeAttempt`, `ClassHintUnlock`) go through gameplay.

## 6. Authentication

Authentication converges on one point. `internal/accounts.Service` implements
`auth.Authenticator`, whose single `Authenticate(ctx, r)` resolves *either* credential to a
fully-populated `policy.Principal` before any authorization middleware runs. The
`authenticate` middleware calls it once and stashes the result; `banWall`, `csrf`,
`rateLimit`, and `policyGate` all read that one `Auth`. Because the ban wall sees only the
resolved principal and not how it was proven, a token cannot route around a wall a cookie
hits. The classic form of that bug — a ban check that runs before the second credential scheme
has been resolved, so a banned account keeps submitting flags with an API token — is not
representable when there is a single resolution point ahead of every gate.

The seam types (`Auth`, `Method`, `Authenticator`, `Limiter`) live in **`internal/auth`,
not here**, precisely because of the import rule in §1: `internal/accounts` must return the
authentication result, and it may not import `internal/httpapi`, so the shared type sits in a
package both can depend on. `httpapi/auth.go` re-exports them as aliases so call sites in
this package stay readable.

**Sessions** are opaque and DB-backed. `Login` → `mintSession` generates a random session id,
stores only its digest in `sessions`, and sets a `SessionTTL` expiry the query enforces on
read. The login/register/change-password handlers return a `sessionOutput`
(`handlers_auth.go`): a `Set-Cookie` header carrying the session id plus a JSON body with the
`csrf_token` the SPA echoes on writes. It is assembled by the `s.session(ctx, sess)` helper,
which calls `accounts.SessionCookieFor(sess, secureOf(ctx))` — so the `Secure` attribute is
set on a real TLS deployment and left off on plain-HTTP localhost. The cookie
(`flagfish_session`) is `HttpOnly` (an XSS cannot read it) and `SameSite=Lax` (not attached to
cross-site POSTs — defence in depth behind CSRF). Session lookup (`authenticateSession`) also
runs the **password-change kill switch**: each session stores a fingerprint of the password
hash it was minted against, and `authenticateSession` constant-time-compares it to the
current hash. When `ChangePassword` writes a new hash, every session minted before it stops
matching and is dead — no revocation list, no fan-out delete, no window. The caller re-mints
its own session as part of the change.

**API tokens** (`handlers_tokens.go`, `accounts/tokens.go`) are bearer credentials in
`Authorization`. `POST /tokens` returns the plaintext exactly once in `createTokenOutput`;
only its sha256 is stored, and an expiry is always set — "never expires" is deliberately
inexpressible. `authenticateToken` looks up by digest and cannot distinguish unknown from
expired (both are `ErrUnauthorized`). `DELETE /tokens/{id}` enforces ownership in the WHERE
clause, and "not yours or not there" collapses to one 404. Both credential paths end in the
same `loadPrincipal`, the one place a `Principal` is built. A presented-but-bad credential is
a 401 and must **not** silently downgrade to anonymous — that downgrade is how a dead token
quietly becomes public access.

**CSRF** protects cookie-authenticated writes only. `csrf` short-circuits unless the method is
cookie auth *and* the method is unsafe, then constant-time-compares the `CSRF-Token` header
against the session's stored nonce. Token auth is exempt because CSRF exists to counter the
browser attaching *cookies* to cross-site requests on its own; it does not attach an
`Authorization` header on its own, so there is nothing to defend. The exemption keys on the
*identity source* (`Method == MethodCookie`), not on header presence — an attacker stuffing an
`Authorization` header onto a forged cross-site request does not become token-authenticated,
they just fail to authenticate. The CSRF nonce is a second independent secret minted alongside
the session (not derived from the session id) and returned in the session body, so the browser
holds the cookie it cannot read and JavaScript holds the token to echo back.

## 7. Error model

Every error on the wire is an RFC 7807 problem document (`application/problem+json`), and every
success is the bare resource — no `{success, data}` envelope in either direction. The argument for
that contract is in [the API architecture chapter](_arch/03-api.md#1-the-response-contract); what
follows is how it is produced. Two producers, one shape:

- **Middleware-level** denials use `problem(w, status, kind, detail)` (`middleware.go`),
  writing `{type, title, status, detail}` with `type` a stable
  `urn:flagfish:error:<kind>` URI. This covers the pre-Huma rejections: invalid
  credentials (401 `invalid-credentials`), ban (403 `banned` / `team-banned`), CSRF (403
  `csrf`), rate limit (429 `rate-limited`), limiter unavailable (503), and panic (500).
- **Handler- and gate-level** failures use Huma's typed helpers — `huma.Error404NotFound`,
  `huma.Error409Conflict`, `huma.Error402PaymentRequired`, and so on — which emit the same
  RFC 7807 envelope. The handler bodies map domain errors to status: `catalog.ErrChallengeNotFound`
  → 404, `accounts.ErrEmailTaken` → 409, `gameplay.ErrInsufficientScore` → 402,
  `account.ErrTeamless` → 403, and any unexpected error → a logged 500 with a flat message.

Using one envelope on both sides means a policy denial, a validation failure, and a handler
error are indistinguishable in shape to a client — a consumer writes one error handler, not
three. A failed body write after the status line is already committed is swallowed by design:
the client hung up, and there is no second response to send.

One rule the submit handler makes explicit: a corrupt flag (for example a stored regex that
no longer compiles) surfaces as a 500, never as a laundered "incorrect" — telling a player
their correct flag is wrong would be worse than an honest failure.

## 8. OpenAPI as a checked-in contract

Each API serves its own OpenAPI document and docs UI, configured in `newAPI`:
`/api/v1/openapi` + `/api/v1/docs`, and the admin equivalents. Because Huma derives the schema
from the handlers' input/output structs, the served document always matches the code that is
running — it is the product surface the TypeScript client is generated from and that
third-party bots and `flagfishctl` are written against.

That contract is checked into the repo and drift-gated:

- **`flagfish openapi`** (`cmd/flagfish/main.go` → `app.OpenAPIYAML`) prints the public document
  to stdout. It builds the real `Server` with the feature services constructed against **nil
  pools**: route registration only records operation schemas and no handler ever runs, so the
  emitted YAML is exactly what the binary serves, with no database in the loop. This is also
  why `openapi` is reachable before `LoadEnv` — a CI runner with no database can still emit
  the contract.
- **`openapi.yaml`** is the checked-in copy at the repo root.
- **CI fails on drift.** `task check-generated` regenerates the document (`go run ./cmd/flagfish
  openapi > openapi.yaml`) and `git diff --exit-code`s it alongside the sqlc-generated code.
  A handler change that silently alters the API surface therefore shows up as a diff a human
  has to approve, rather than shipping unnoticed.

## 9. Adding an endpoint — checklist

`handlers_auth.go` is the worked example: `registerAuth` registers five operations, and
`session`/`register`/`login`/`logout`/`me`/`changePassword` are the thin handlers behind
them. To add one:

1. **Pick or add a `RouteClass`** in `internal/domain/policy/routeclass.go` and give it a
   `classAttrs` row. Add it to `AllClasses`/`classNames`. If it is a new class, the golden
   decision-table test will force you to state its gates.
2. **Choose the surface.** Public read/write → register on `srv.Public`. Admin view → a
   separate operation on `srv.Admin` with an admin class. Do not branch a response on role;
   split the URL.
3. **Define typed input/output structs** next to the handler (like `sessionOutput` /
   `loginInput` in `handlers_auth.go`). Huma generates validation and schema from them; the
   handler body receives an already-validated `*I`. A cookie or CSRF response goes in a
   `Set-Cookie` header field on the output struct.
4. **Add a `registerX` method** (or extend an existing slice's), calling
   `httpapi.Register(api, class, op, handler)` — never `huma.Register` directly, so the class
   is stamped and the gate can see it. Wire it into `registerRoutes` behind the nil-guard for
   its service, and add the service to `Options` and the fx `serverParams` if it is new.
5. **Keep the handler thin.** It calls one feature-service method, maps domain errors to Huma
   status helpers, and uses `AuthOf` / `s.actor(ctx)` for the caller. It must not rebuild the
   `Policy` — read `PolicyOf(ctx)` if it needs the decision; the gate, the query, and the
   serializer answer to one decision, not three.
6. **Do not touch the hot path casually.** A write under `ClassChallengeAttempt` goes through
   `gameplay.Submit`, whose lazy lock is the reason wrong answers stay cheap. Read the hot-path
   design before changing it.
7. **Regenerate and commit the contract.** Run `task generate` (or `flagfish openapi >
   openapi.yaml`) and commit the updated `openapi.yaml` with the code, or CI's drift gate will
   reject the change.
