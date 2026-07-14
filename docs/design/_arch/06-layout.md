# _arch/06 — Package layout, deployment, observability

## Design rules

1. **No god-package.** There is no `internal/models` holding every entity. Packages are split by
   *domain*, not by *layer*: a single module that owns every table is the shortest path to a schema
   where entities that share nothing end up sharing a row.
2. **`domain` imports nothing.** Not the database, not HTTP, not the job queue. Pure types and pure
   functions — the scoring formula, the flag comparison, the policy decision. This is what makes the
   invariant tests fast and total, and CI enforces it.
3. **The router is not load-bearing.** `http.Handler` all the way down; the framework is replaceable.
4. **Nothing is loaded at runtime.** No plugin scan, no external templates, no assets read from disk.
   What compiles is what ships.

---

## Layout

```
cmd/
  flagfish/                 # the single binary: serve | worker | migrate | import | export |
                            #   restore | admin | env | openapi | healthcheck
  flagfishctl/              # challenge-as-code CLI — a first-party consumer of the public API

internal/
  domain/                   # ← imports NOTHING but stdlib. pure.
    scoring/                #   decay formula, standings shape, tiebreak
    flags/                  #   compare: static (constant-time) | regex | unique (hash); FlagIssuer
    policy/                 #   the authorization decision. pure: inputs → allow/deny/redact
    account/                #   the user/team duality, expressed once
    prereq/                 #   challenge prerequisite resolution

  db/                       # sqlc-generated. DO NOT EDIT.
    migrations/             #   goose, plain SQL. go:embed'd. THE schema source of truth
    queries/                #   *.sql — the hand-written SQL sqlc verifies at build time

  gameplay/                 # the submit hot path, hint unlocks, instance issuance
  catalog/                  # the read side of the challenge board: list, detail, solve list
  board/                    # standings, freeze, brackets, time-travel. reads the score ledger.
  accounts/                 # users, teams, registration, login, tokens, email flows
  auth/                     # session and token identity, shared by the middleware
  adminops/                 # admin write surface: challenges, users, brackets, tags, audit reads
  anticheat/                # the detector queries. reads only.
  audit/                    # the actor stamp the triggers read. writes are Postgres triggers.
  files/                    # challenge artifacts: upload, delete, authorized download
  storage/                  # ArtifactStore: local disk today, S3 the second implementation
  config/                   # typed config: snapshot in memory, LISTEN/NOTIFY invalidation
  notify/                   # LISTEN/NOTIFY → SSE fan-out
  jobs/                     # River workers: email, webhooks, first-blood announcements
  mail/                     # SMTP transport
  httpapi/                  # Huma + chi: handlers, middleware chain, the error envelope
  app/                      # composition root — wiring, lifecycle, OpenAPI assembly
  migrate/                  # goose runner, behind an advisory lock
  platform/
    importer/               # CTFd archive → our schema (one-way)
    exporter/               # our own format, field-masked, plus restore
  web/                      # go:embed of the built SPA + the static handler

web/                        # React + Vite + TanStack. built to dist/, staged into internal/web/dist
```

**The dependency rule, enforced in CI:** `domain` → nothing but stdlib. Everything else → `domain`
plus `db`. `httpapi` → the feature packages; nothing imports `httpapi`. The check
(`task check-boundaries`) walks the **transitive** import set from `go list -deps` — a direct-import
check is not enough, because `domain → helper → pgx` is exactly as fatal as `domain → pgx` — and it
runs outside the linter, so it cannot be silenced by an inline `//nolint` nobody reviews. Without it,
`domain` grows a `*pgxpool.Pool` within a month and the invariant suite quietly stops being pure.

---

## Config — the whole table lives in memory

Config is ~100 rows that change a few times per event. It is read on nearly every request and written
almost never, which is the shape that a snapshot fits exactly.

```go
type Snapshot struct { /* typed fields, no string coercion */ }

// atomic.Pointer[Snapshot], swapped wholesale.
// boot:   SELECT * FROM config → parse + validate ONCE → store
// write:  UPDATE row; NOTIFY config_changed
// listen: LISTEN config_changed → re-SELECT → atomic swap
```

Reads are a **pointer dereference**. There is no cache, no TTL, and no invalidation graph, so there
is nothing to get wrong: a memoized config with no invalidation path is *unbounded* staleness, and a
memoized config with one is a distributed-cache problem you did not mean to sign up for.

Parsing and validation happen **once, at load**. A malformed value **fails at boot**, with a clear
error naming the key — rather than being coerced by guesswork at some call site three months later.
A store that infers types on read ("looks like digits ⇒ int") will eventually hand an `int` to a
caller that stored the string `"12345"` on purpose, and the failure lands nowhere near the cause.

This reuses the `LISTEN/NOTIFY` machinery already wired for SSE. Second payoff from one decision.

---

## Deployment — one binary, two roles

```
./flagfish serve                 # API. River insert-only client — enqueues, never works.
./flagfish worker                # River client with the workers registered.
./flagfish serve --with-worker   # both, in-process. THE DEFAULT. compose runs this.
./flagfish migrate               # goose, behind pg_advisory_lock.
./flagfish import <archive.zip>  # CTFd archive → our schema (one-way).
```

**Self-hosters get one container.** Large events split the roles and get three things: an import
cannot touch API p99, deploying the API does not kill a running import, and the process that restores
the database is not the one serving traffic. Don't force the topology — make it a flag.

`go:embed` takes the built SPA (staged into `internal/web/dist`) **and** the goose migrations.
Nothing is read from disk at runtime. If `./flagfish serve` cannot run from an empty directory
against a fresh Postgres, the single-binary property is broken.

**Migrate-on-boot with N replicas is a schema-corrupting race.** Take `pg_advisory_lock` around the
migration step, or run `flagfish migrate` as a separate pre-deploy job. One line, trivially
forgotten, and the failure is a half-applied schema.

---

## Middleware chain (order matters)

```
request-id → real-IP → recover → structured-log → OTel span
  → auth (session cookie | API token)
  → ban wall      ← must cover BOTH auth paths. A ban enforced only on the cookie path is not a ban.
  → CSRF          ← cookie auth ONLY; token auth is CSRF-exempt (there is no ambient credential)
  → rate limit    ← INSERT … ON CONFLICT DO UPDATE SET n = n+1 — atomic in Postgres, no cache tier
  → policy gate   ← the pure authorization decision (see _arch/02-policy.md)
  → handler
```

**Real client IP is not optional.** `submissions.ip` is a recorded field and it is load-bearing for
the anti-cheat detectors. Every real deployment sits behind a proxy, so `X-Forwarded-For` handling
must be right, with a trusted-proxy list. Getting it wrong does not break the app — it silently
poisons the anti-cheat data, which is worse, because nothing tells you.

---

## Observability

OTel traces, structured logs, Prometheus metrics. Cheap now, expensive to retrofit.

**The two spans that matter** are the two things the architecture is organised around:

| Span | Why |
|---|---|
| `submit` | lock-wait, flag-compare, solve-insert, first-blood count. **Lock-wait time is the canary:** if it climbs, a refactor has moved the challenge lock off the correct-flag path and every wrong guess is now serializing. |
| `standings` | the `UNION ALL` over the score ledger. The heaviest read in the product. |

**The metric that is load-bearing for the product, not just for ops:** **instance-pool utilisation** —
`issued / total` per challenge. Pool exhaustion mid-CTF is a hard failure by design (see
[Unique flags](../TARGET-FEATURES.md#unique-flags)); this gauge is what makes that failure preventable
rather than merely loud, and it is the one number that must be right *before* an event starts.
