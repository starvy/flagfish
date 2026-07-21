# HTTP / API architecture

flagfish serves one JSON API and one static SPA out of a single binary. The API is the product
surface: the SPA is just its first client, and `flagfishctl`, CI jobs and third-party bots are the
others. This chapter states the wire contract, the surface, the middleware chain, and the list
semantics. The implementation-level companion — the actual router, the `Register` seam, the policy
gate, the handler slices — is [The HTTP API layer](../http-api.md).

Stack: chi for routing and middleware, [Huma](https://huma.rocks) mounted on top of it via
`humachi` for typed operations and OpenAPI generation, sqlc for the queries underneath. The OpenAPI
document is generated from the handler structs and checked into the repo, so it cannot drift from
the running code.

---

## 1. The response contract

**Every non-2xx is `application/problem+json` (RFC 7807). Every 2xx is the resource itself.**
No `{success, data}` envelope, in either direction.

### 1.1 Errors

```jsonc
// 400 / 401 / 403 / 404 / 409 / 422 / 429 / 500 — one shape, always
{
  "type":   "urn:flagfish:error:validation",  // stable, machine-readable
  "title":  "Unprocessable Entity",
  "status": 422,
  "detail": "validation failed",
  "instance": "/api/v1/challenges",
  "errors": [                                          // the only error list on the wire
    {"location": "body.value", "message": "expected number >= 0", "value": -5}
  ],
  "request_id": "01JZ…"                                // extension member; matches the log line
}
```

One shape for every failure, produced in exactly two places — the pre-Huma middleware rejections
and Huma's own typed error helpers — so there is no code path that can invent a third. A client
writes one error handler, not one per status code.

Domain outcomes a client must branch on — `rate_limited`, `already_solved`, `paused`,
`instance_pool_exhausted` — go in `type`, which is a URI: versioned, greppable, stable across locale
and copy edits. They do not go in a free-text `detail`, which is for humans and may change.

The alternative — a body-level envelope where the outcome lives in `{"success": false, …}` and the
HTTP status stays 200 — fails for three reasons. It makes the transport lie: every proxy, cache,
retry policy and load balancer between the client and the server reads the status line, and none of
them read the body. It makes the error type unbounded in practice, because nothing forces the
`errors` member to hold the same kind of value on a validation failure as on a rate-limit rejection
(the shapes drift apart endpoint by endpoint, and a generated client cannot type the union). And it
does not cover the whole surface anyway: the requests that never reach a handler — a 404 on an
unrouted path, a 429 from the limiter, a 500 from a panic — are produced by the framework, not by
the application, so they escape the envelope and the client needs the standard path *as well*.
problem+json is that standard path, every HTTP client and OpenAPI toolchain already understands it,
and using it uniformly means the framework's own failures and the handler's failures are the same
document.

### 1.2 Success

```jsonc
GET /api/v1/challenges/1     → 200  {"id":1,"name":"heap-overflow", …}          // the object
GET /api/v1/challenges       → 200  {"items":[…],
                                     "pagination":{"page":1,"per_page":50,"pages":4,"total":183,
                                                   "next":2,"prev":null}}
POST /api/v1/challenges      → 201  {"id":7, …}  + Location: /api/v1/challenges/7
DELETE /api/v1/challenges/7  → 204  (no body)
```

`{"success": true}` on a 200 is a second, redundant status channel, and a redundant channel is a
channel that can disagree with the first one. Every response is a typed Go struct, so a wrapper is
not free either: it is one extra generic level in every handler, every generated TypeScript type,
and every `flagfishctl` call site, purchased with zero information.

`items` + `pagination` is not the envelope in disguise — it is a *named list type*. It appears in
the OpenAPI document as `ChallengeList`, `openapi-typescript` gives the SPA a real type for it, and
it exists because a page of results genuinely has metadata that a bare array cannot carry.

### 1.3 Not wire-compatible with CTFd, deliberately

flagfish [imports CTFd export archives](04-import.md), so the question comes up: should the API
also speak CTFd's wire format, so existing bots keep working?

No. The payloads differ regardless of the envelope. Solve values are stamped at solve time rather
than recomputed from the challenge's current value, so scoreboard and solve payloads carry
different numbers by design. The challenge payload has no plugin template/script fields, because
there is no plugin system. Several resources do not exist here at all. Preserving an envelope over
different data buys the *appearance* of compatibility: a bot parses cleanly, branches on `success`,
and acts on the wrong answer. A hard 404 and a schema mismatch at the door are strictly better —
they fail where the fix belongs, in the client, at the moment the client is pointed at flagfish.
We publish `openapi.yaml`; a typed client regenerates, and that is the whole migration.

### 1.4 Behaviors fixed by construction

- **One error shape for every status.** Including the ones the application never sees: unrouted
  paths, limiter rejections, panics.
- **401 and 403 mean different things.** 401 = a credential was presented and did not resolve
  (unknown token, expired session, wrong password). 403 = the caller is resolved — anonymous
  included — and is not allowed. A bad credential never quietly downgrades to anonymous, because
  that downgrade is how a dead token turns into public access without anyone noticing.
- **No content negotiation, and no 3xx.** `/api/v1/*` never emits a redirect and never serves
  HTML. A denial that has a natural next step for a browser (log in, join a team, confirm your
  email) says so in a `Location` header on the problem document, and the SPA decides what to do
  with that. A gate that inspects `Accept:` and serves a redirect to browsers and a status code to
  programs is two behaviors under one route; picking one deletes an entire class of middleware.
- **`timestamptz` everywhere, RFC3339 with milliseconds on the wire.** No naive timestamps, no
  server-local clock in a payload.

---

## 2. The API surface and the packages that own it

The router is chi → `humachi` → two `huma.API` instances: the public surface at `/api/v1` and the
admin surface at `/api/v1/admin`. The split is structural — a response whose shape varies by role
cannot be typed in OpenAPI, but one that varies by URL can — and it is explained in
[The HTTP API layer](../http-api.md#2-two-surfaces-one-reason).

Transport lives in one package. `internal/httpapi` owns the router, the middleware, the policy
gate, the error model, the OpenAPI emit, and one handler slice per resource group
(`handlers_challenges.go`, `handlers_submit.go`, `handlers_admin_*.go`, …). Behind it sit the
feature services, which own SQL and transactions and know nothing about HTTP. **There is no shared
"models" package**: request/response structs live next to their handlers, and sqlc's row structs
never leave the store layer.

| Resource group | Public | Admin | Service package |
|---|---|---|---|
| Challenges (board, detail, solve list) | ✅ | ✅ | `internal/catalog` (read) · `internal/adminops` (CRUD, ordering, state) |
| Flags | — | ✅ | `internal/adminops`; types are `static` \| `regex` \| `unique`, a closed in-tree set |
| Hints | ✅ unlock | ✅ CRUD | `internal/gameplay` (unlock, it spends points) · `internal/adminops` |
| Tags | ✅ read | ✅ CRUD/merge | `internal/adminops` |
| Files / artifacts | ✅ download | ✅ upload | `internal/files`, behind the `internal/storage` seam (filesystem or S3) |
| Submissions & solves | ✅ attempt | ✅ list, mark | `internal/gameplay` — the hot path |
| Awards | — | ✅ | `internal/adminops` |
| Scoreboard | ✅ | ✅ (hidden/banned included) | `internal/board` |
| Users | ✅ profile | ✅ CRUD, ban, role | `internal/accounts` · `internal/adminops` |
| Teams | ✅ join/create/leave | ✅ CRUD | `internal/accounts` |
| Brackets | ✅ read | ✅ CRUD | `internal/adminops` |
| API tokens | ✅ own | — | `internal/accounts` |
| Config | — | ✅ atomic bulk PATCH | `internal/config` |
| Notifications | ✅ read + SSE | ✅ CRUD | `internal/notify` (`LISTEN/NOTIFY` → in-process broadcaster) |
| Audit | — | ✅ read-only | `internal/adminops` (reads the trigger-captured stream; `internal/audit` propagates the actor into the transaction) |
| Anti-cheat | — | ✅ read-only | `internal/anticheat` |
| Tasks (import / export) | — | ✅ | `internal/platform/importer`, `internal/platform/exporter`, run as River jobs from `internal/jobs` |
| Statistics | ✅ minimal | ✅ | `internal/board` aggregates; the expensive progression matrix is not in v1 |

Deferred, and every one of them is *additive* — a new table and new endpoints, no change to the hot
path and no change to the visibility rules, which is what makes deferring them safe: a CMS/pages
resource, topics, comments, solutions and challenge ratings, audiences, modules, social-share
links, and CSV import/export. OAuth / federated login is deferred for the same reason. There is no
plugin-asset route class at all, and never will be: challenge types and flag types are in-tree
interfaces.

---

## 3. The admin surface

The admin panel is a set of client routes in the SPA. The server keeps no HTML route except the SPA
fallback, and the panel talks to the same API as everything else — through `/api/v1/admin`, with
`RouteClass`-declared policy, not through a privileged back door.

That is only true if every screen the panel needs has an endpoint behind it. The list below is the
admin surface and the endpoints that back it; the point of writing it out is completeness. An SPA
migration stalls at 90% when three screens turn out to have been reading straight from the
database with no API in front, and the endpoints marked **new** below are exactly those screens.

| Admin screen | Endpoints behind it |
|---|---|
| Overview | `GET /admin/statistics/overview` — **new**: users, teams, distinct submission IPs, correct/incorrect counts, challenge count, total points, most- and least-solved. The panel must not compute this client-side over a full list fetch. |
| Challenges (list) | `GET /admin/challenges?q=&field=&sort=&page=` |
| Challenge (detail) | `GET /admin/challenges/{id}` + sub-resources: `/flags`, `/hints`, `/tags`, `/files`, `/requirements`, `/solves` |
| Challenge (new / edit) | `POST` / `PATCH /admin/challenges`, `POST /admin/challenges/order`, `PATCH /admin/challenges/{id}/state` |
| Challenge preview | client-side render of the same payload through the player field mask. The server-side signed-file-token behavior is preserved in `/files` — a preview must not hand out an unsigned artifact URL. |
| Submissions | `GET /admin/submissions?type=correct\|incorrect&page=` |
| Users (list) | `GET /admin/users?q=&field=…&page=` — `field` includes **`ip`**, which joins the address-tracking table. It is a *virtual* field: a separate named query, not a branch in the search `CASE`. |
| User (detail) | `GET /admin/users/{id}` + `/solves`, `/fails`, `/awards`, + **new** `/addresses` (recorded submission addresses) and **new** `/missing` (challenges not yet solved) |
| User (new) | `POST /admin/users` (`?notify=` sends the welcome mail) |
| Teams | `GET /admin/teams`, `POST /admin/teams`, `GET /admin/teams/{id}` + `/members`, `/solves`, `/fails`, `/awards`, + **new** `/addresses`, **new** `/missing` |
| Scoreboard | `GET /admin/scoreboard` — includes hidden and banned accounts. This is a *named* view (`ScoreboardView{Public, Admin}`), decided by which surface the request arrived on, not a default argument a handler can widen. |
| Config | `GET /admin/config` + atomic bulk `PATCH /admin/config` — one transaction, all keys or none. A half-applied config change is a half-configured event. |
| Notifications | `GET` / `POST` / `DELETE /admin/notifications` |
| Import / export | `POST /admin/imports` → `202 {task_id}`; `POST /admin/exports` → `202 {task_id}`; `GET /admin/tasks…` (§6.4) |
| Reset | **new** `POST /admin/reset` `{accounts, submissions, challenges, notifications}` → `202 {task_id}`. It is a bulk delete over the whole instance; it runs as a task and it is audited. |
| Audit | `GET /admin/audit` (§6.5) |
| Anti-cheat | `GET /admin/anticheat/*` (§6.2) |

Net-new endpoints the panel forces into existence: `users/{id}/addresses`, `teams/{id}/addresses`,
`users/{id}/missing`, `teams/{id}/missing`, `statistics/overview`, `admin/reset`, and `field=ip` on
the user list.

Not present, and worth stating: there is no update-check banner. An admin page load does not phone
home to a vendor endpoint.

---

## 4. Auth and the middleware chain

### 4.1 Two credentials

| | Session cookie | API token |
|---|---|---|
| Presented as | `Cookie: flagfish_session=…` | `Authorization: Bearer <token>` — the scheme word is validated, not ignored |
| Value format | opaque id; only its digest is stored | opaque secret; only its sha256 is stored, returned in plaintext exactly once |
| Expiry | session TTL, enforced in the lookup query | always set; "never expires" is deliberately inexpressible |
| Survives a password change? | **no** — the fingerprint stops matching, no delete needed | **no** — but only because it is deleted; a bearer secret has nothing to compare |
| CSRF | **required** on unsafe methods | **exempt** — see below |
| Used by | the SPA | `flagfishctl`, bots, CI |

Three rules that follow from having two credentials rather than one:

1. **Token auth is honored on every request, uniformly** — every method, every content type,
   including the SSE stream. Not "on JSON requests only, plus a special case for multipart
   uploads". `flagfishctl` uploading an instance pool must not depend on a `Content-Type` coincidence,
   and a token that works on `GET /challenges` but not on `GET /events` is a bug the user has to
   discover at runtime.
2. **CSRF exemption keys on the identity source, not on header presence.** A request is exempt
   because *the identity came from a token*, not because an `Authorization` header happened to be
   there. Those are different predicates, and only the first one is safe: an attacker who stuffs an
   `Authorization` header onto a forged cross-site request does not thereby become
   token-authenticated — they just fail to authenticate.
3. **A credential change evicts both, by different means.** A session dies because its password
   fingerprint stops matching; a token dies because the change deletes it. Same guarantee, two
   mechanisms — and the second is easy to forget precisely because the first is free, which is how
   "change your password" becomes a remediation that leaves the attacker holding a live bearer
   credential.

### 4.2 The chain — the order *is* the design

The rule the chain exists to enforce: **authentication completes, for all schemes, before any
authorization middleware runs.** The ban wall consumes an identity; it never looks at a session.

This is stated as architecture and not left to implementation habit because the failure mode is
silent and severe. If a ban check runs before a second credential scheme has been resolved, then
that scheme is not covered by the wall — a banned account with a valid token keeps full API access,
including flag submission, and nothing in the code *says* so. The bug is entirely in the ordering,
so the ordering is the artifact.

```
chi (outer → inner)
 1. RequestID                   # stamped first, so every log line and problem document carries it
 2. RealIP                      # submissions.ip is recorded evidence; X-Forwarded-For is
                                #   believed only from a configured trusted proxy, and an empty
                                #   trusted list means trust nobody
 3. Recoverer                   # panic → 500 problem+json (never HTML)
 4. Structured logging          # one line per request, with the final status
 5. AUTHENTICATE  ─────────────────────────────────────────────────────────────────
      if Authorization: Bearer <tok>  → look up by digest; unknown/expired → 401
                                        → Principal{account, via: Token}
                                        #   a credential change DELETED the row, so this is
                                        #   how a revoked token surfaces: as "unknown"
      else if session cookie          → load session; password-hash fingerprint mismatch
                                        → 401 (changing a password kills every other session)
                                        → Principal{account, via: Cookie}
      else                            → Principal{anonymous}
    #  ↑ ONE resolution point. Everything below reads the Principal, never the raw credential.
    #  A presented-but-bad credential is a 401 — it must not downgrade to anonymous.
 6. BAN WALL                    # principal.Banned || principal.TeamBanned → 403.
                                #   It cannot tell the two schemes apart, so it cannot fail
                                #   to cover one.
 7. CSRF                        # only if via == Cookie AND the method is unsafe
 8. Rate limit                  # per (principal | client IP, operation); 429 problem+json.
                                #   Fails closed: a limiter that errors returns 503.
── humachi ────────────────────────────────────────────────────────────────────────
 9. POLICY GATE                 # one decision, from the operation's declared RouteClass +
                                #   the config snapshot + the principal + the surface.
                                #   Auth, verification, forced password change, the time gates
                                #   and the visibility gates are all *this* decision — there is
                                #   no second place a route can be denied.
                                #   An operation with no declared class is DENIED, not ungated.
10. Handler → feature service → sqlc
11. Field mask on the way out (view = user | self | admin)
```

Steps 9 and 11 are worth naming precisely. The gate is a **declaration on the operation**, not a
stack of decorators wrapped around the handler: each operation states its `RouteClass`, the class
maps to a row of gate attributes (requires-auth, requires-verified, time-gated, admin-only,
ban-exempt, …), and that table is data. "Which gates guard this endpoint" is therefore a diffable
artifact — it shows up in `openapi.yaml` and in one golden test — rather than something you
reconstruct by reading decorators. And because a missing declaration fails closed, forgetting to
gate a new route is loud at registration time, not silent in production.

The ban wall stays ahead of the gate, as a chi middleware, because a ban is not a per-operation
question: it applies to every request that entered the authenticated subtree, whether or not that
request is a typed Huma operation. Everything that *is* per-operation is decided once, at step 9.
Health checks and static assets are mounted *outside* the authenticated subtree entirely, so their
exemption is structural rather than a list of paths someone has to remember to maintain — a banned
user still needs the stylesheet that renders the page telling them they are banned.

Anonymous access stays available where the event's visibility settings allow it: public challenge
listing, public scoreboard, notifications. That is a policy decision made by config, evaluated at
the gate, not a route that happens to have no annotation on it.

**SSE.** `GET /events` is not a JSON operation, so it is a plain chi route outside Huma — but it
sits behind the same chain, steps 1–10, and it accepts both credentials. It is backed by
`LISTEN/NOTIFY` and an in-process broadcaster, so a notification fans out to every connected client
of every replica without a polling loop.

Implementation: [The HTTP API layer §3](../http-api.md#3-the-middleware-chain-in-order) and
[§4](../http-api.md#4-the-policy-gate-and-routeclass).

---

## 5. Pagination, filtering, sorting

### 5.1 Pagination — page-based, uniform

Page-based, because the consumers are admin tables (TanStack Table wants `page` and `total`) and
because a page number is a thing a human can type into a URL. One shape everywhere: `page` (1-based,
default 1), `per_page` (default 50, max 100 — **one constant, not a per-endpoint cap**), and
`pagination{page, per_page, pages, total, next, prev}` on every list. An out-of-range page returns
an empty `items`, not an error: it is a legal question with an empty answer.

Cursor pagination is used **only** where the list is append-only and unbounded — the audit stream
and anti-cheat queries over submissions. Everything else is admin-table-sized and offsets are fine.

### 5.2 Filtering — the closed enum is a Go type *and* a SQL `CASE`

A list endpoint takes `q` (the needle) plus `field` (which column to look in). `field` is a closed
enum, per endpoint, and small: challenges `{name, description, category, type, state}`, submissions
`{challenge_id, user_id, team_id, ip, provided, type}`, users
`{name, website, country, bracket, affiliation, email}` — with `email` admin-only.

**No query builder.** The enum is encoded twice, in two places that are both checked at build time:

```go
// The enum is a Go type; Huma emits it as an OpenAPI enum, so an out-of-enum `field` is
// rejected at the edge with a 422 problem+json and never reaches SQL.
type ListInput struct {
    Q       string `query:"q"        maxLength:"128"`
    Field   string `query:"field"    enum:"name,description,category,type,state"`
    Page    int    `query:"page"     default:"1"  minimum:"1"`
    PerPage int    `query:"per_page" default:"50" minimum:"1" maximum:"100"`
}
```

```sql
-- name: ListChallenges :many
-- The closed enum, encoded. Index-unfriendly, and it does not matter: this is an admin table
-- over a few thousand rows. One named query per endpoint; the enum lives in exactly two places
-- (the Go tag above and this WHERE) and both fail the build if they drift.
SELECT sqlc.embed(c), COUNT(*) OVER () AS total
  FROM challenges c
 WHERE (@state::text = '' OR c.state = @state)
   AND (@q::text = '' OR (
         (@field::text = 'name'        AND c.name        ILIKE '%'||@q||'%') OR
         (@field::text = 'description' AND c.description ILIKE '%'||@q||'%') OR
         (@field::text = 'category'    AND c.category    ILIKE '%'||@q||'%') OR
         (@field::text = 'type'        AND c.type        ILIKE '%'||@q||'%') OR
         (@field::text = 'state'       AND c.state       ILIKE '%'||@q||'%')))
 ORDER BY c.id
 LIMIT @lim::int OFFSET @off::int;
```

The rules that fall out of this:

- **Integer columns compare with `=`, not `ILIKE`.** The coercion happens in Go: parse `q` as an
  int, and an unparseable needle against an integer field yields an *empty result*, not a 400 and
  certainly not a 500. Declaring a text column's query parameter as an integer and rejecting
  everything non-numeric is a filter that cannot find its own data.
- **Virtual fields get their own query.** `field=ip` on the user list joins the address-tracking
  table; admin submissions can filter by `challenge_name`. These are separate named queries, not
  another arm in the `CASE`.
- **Role-dependent enum members are enforced after the policy gate.** `field=email` for a
  non-admin is a **403**, not a 400: the parameter is well-formed, the caller is not allowed to use
  it, and conflating those two hides an authorization decision inside a validation error.
- `COUNT(*) OVER ()` returns `total` in the same round trip. A second `COUNT` query is a second
  chance to disagree with the first.

### 5.3 Sorting — server-side, closed enum, total order

Lists are sorted on the server. Client-side sorting of a paginated table is a lie: it sorts the 50
rows that happened to load, not the 2,000 that matched, and the user cannot tell the difference by
looking.

`sort` and `order` are closed enums per endpoint, exactly like `field`:

```sql
 ORDER BY
   CASE WHEN @order::text = 'asc' THEN
     CASE @sort::text WHEN 'name'     THEN c.name
                      WHEN 'category' THEN c.category END END ASC  NULLS LAST,
   CASE WHEN @order::text = 'desc' THEN
     CASE @sort::text WHEN 'name'     THEN c.name
                      WHEN 'category' THEN c.category END END DESC NULLS LAST,
   CASE WHEN @sort::text = 'value' AND @order::text = 'asc'  THEN c.value END ASC  NULLS LAST,
   CASE WHEN @sort::text = 'value' AND @order::text = 'desc' THEN c.value END DESC NULLS LAST,
   c.id                                        -- always: a total order, so pages are stable
```

Text and numeric sort keys must sit in **separate `CASE` arms** — a `CASE` expression has one
result type. The trailing `c.id` is not optional: without a total order, two rows with equal sort
keys can swap between page 1 and page 2 and the client will show one twice and the other never.

This does get unwieldy past roughly four sort keys. The escape is one named query per (sort, order)
pair, not a query builder: a builder is a second, unverified way to write SQL, and the whole reason
the queries are hand-written and compiled is that there is exactly one.

---

## 6. The endpoints that are not parity

### 6.1 The instance pool — unique flags, designed for `flagfishctl` first

Unique flags mean each account is issued its own flag value for a challenge, so a leaked flag
identifies its owner. The pool is uploaded by the challenge author, and the *upload* is the thing to
get right: a pool-upload flow that only makes sense from a browser is useless to the person who
keeps their challenges in git. So the canonical form is a bulk, idempotent, body-is-the-pool
upload — the browser gets it by reading a file, not the other way round.

| Method | Path | Notes |
|---|---|---|
| `PUT` | `/challenges/{id}/instances` | **Bulk, idempotent, replaces by generation.** Body: `{"flags": […], "generation": 3}` or `text/plain`, newline-delimited — which is what a `flags.txt` in a repo actually is. The server hashes each entry, inserts pool rows `ON CONFLICT DO NOTHING`, bumps the generation. Already-issued flags keep their issue rows: rotation adds, it does not orphan. → `200 {generation, added, duplicates, total, issued}`. Token auth, no CSRF, no multipart. |
| `GET` | `/challenges/{id}/instances` | Admin. Paginated, **values redacted by default**; `?reveal=true` for the author. |
| `GET` | `/challenges/{id}/instances/stats` | `{total, issued, remaining, utilization}` — the pre-event exhaustion gauge. This is the one number that must be right *before* the event starts, because it cannot be fixed during. |
| `DELETE` | `/challenges/{id}/instances/{instance_id}` | Only if unissued. |
| `GET` | `/challenges/{id}/flag-issues` | Admin: who holds which entry. Never player-visible. |

Issuance is **not** an endpoint. It happens lazily inside `GET /challenges/{id}` and artifact
download, in the same transaction, `ON CONFLICT DO NOTHING` — so two concurrent requests from the
same account converge on one issue rather than burning two pool entries. An exhausted pool is a
`409` with `type: …/errors/instance-pool-exhausted`. It fails hard and loudly: quietly falling back to a
shared static flag would silently destroy the property the whole feature exists to provide.

### 6.2 Anti-cheat — admin-only, silent

A detector that announces itself is not a detector. These endpoints are admin-gated, read-only, and
they are the **only** place `submissions.attributed_account_id` and `submissions.ip` surface at all.
Nothing a player can observe changes when a signal fires.

| Endpoint | Signal |
|---|---|
| `GET /admin/anticheat/flag-sharing` | Correct submissions where the flag was issued to a *different* account than the one that submitted it. |
| `GET /admin/anticheat/unissued-solves` | Correct submissions on a unique-flag challenge with **no flag issued to the submitter**. Provable, not statistical: that value cannot have been obtained legitimately. |
| `GET /admin/anticheat/ip-overlap` | Accounts sharing a submission IP with no team relationship. |
| `GET /admin/anticheat/timing` | `?window=<seconds>` — solves clustered abnormally tightly behind a first blood. |

All of them: cursor-paginated, with `?challenge_id=`, `?since=`, `?until=`. Review state
(reviewed / dismissed / actioned) is a later addition — v1 ships the queries, not a workflow, and
the queries are the part that is hard to add afterwards.

### 6.3 Scoreboard time-travel

`GET /scoreboard?as_of=<RFC3339>` and `GET /scoreboard/top/{count}?as_of=`. It is one extra `WHERE`
clause, and it is only that cheap because solve values are stamped at solve time — the board at any
past instant is a filter over an append-only ledger, not a reconstruction.

Two clamps, both part of the contract:

- `as_of` in the future clamps to now. It is not an error; the answer is simply the current board.
- **For a non-admin, `as_of` is additionally clamped to the freeze horizon whenever a freeze is
  set.** Without that clamp, time-travel *is* a freeze bypass: `?as_of=now` would hand the live
  board to anyone who asks, and the freeze would protect nothing. The admin scoreboard view is not
  clamped — but it is a different surface with its own policy class, not a query parameter a player
  can set.

The response is identical in shape to the live scoreboard, which makes a replay/animation client a
loop over one endpoint rather than a second API.

### 6.4 Tasks — one status source for long-running work

| Endpoint | Behavior |
|---|---|
| `POST /admin/imports` | multipart archive → `202 {task_id}`. Import is a singleton, enforced by a partial unique index — a second concurrent import is a `409`, not a corrupted database. |
| `POST /admin/exports` | → `202 {task_id}` |
| `GET /admin/tasks` | `?kind=import\|export&state=` |
| `GET /admin/tasks/{id}` | `{id, kind, state, progress, step, created_by, started_at, finished_at, error}` |
| `POST /admin/tasks/{id}/cancel` | cancels the River job; the worker's context is cancelled and the large transaction **rolls back**. A half-imported CTF is worse than no import. |
| `GET /admin/tasks/{id}/artifact` | export download, signed and expiring |
| `GET /admin/tasks/{id}/events` | SSE progress. Optional — polling `GET /tasks/{id}` is the fallback and is what the SPA ships. |

The task state lives in **our own `tasks` table**, written in the same transaction as the work it
describes — not in the job queue's internal tables, and not in a cache entry. Status that lives in a
cache is status that a cache eviction can lose, and losing the status of a database restore while it
is running is the worst possible moment to have to guess.

### 6.5 Audit

`GET /admin/audit` — admin, cursor-paginated, filtered by `actor_id`, `entity`, `entity_id`,
`action`, `since`, `until`. Gameplay events and trigger-captured admin mutations land in one stream,
because a reader asking "what happened to this challenge" should not have to know which of two
subsystems recorded it.

Read-only, permanently: there is no `POST`, `PATCH` or `DELETE` on `/audit`. An audit log that its
own admin API can edit is not an audit log.

### 6.6 First blood

Not a namespace. `challenges.first_blood` (`none | announce | bonus`) is a field on the challenge
payload, and `GET /challenges/{id}/solves` marks the first solve. Announcements go out through the
webhook job queue, and they are **suppressed during freeze** — an announcement that fires while the
board is frozen leaks exactly the information the freeze exists to withhold.

---

## 7. Still open

- **Flag-pool wire format.** Newline-delimited `text/plain` and a JSON array both work for a
  flags-only pool. A pool that also carries a per-flag artifact mapping (challenge-as-code, where
  each account gets its own binary) needs a richer body, and that shape follows the
  challenge-as-code manifest schema rather than leading it.
- **Breaking-change policy.** `openapi.yaml` is checked in and CI-gated, so drift is caught. What is
  not yet settled is the rule for an intentional break once third-party bots exist: a `/api/v2`
  prefix, or additive-only forever. It should be decided before the first bot ships, not after.
- **Rate-limit scope for token auth.** Limits key on the principal when there is one, which is the
  right shape — a shared-IP venue and a CI runner behind one NAT are not the same tenant. The
  numbers themselves, and whether a `flagfishctl` sync of a few hundred challenges trips them, are
  not fixed yet.
