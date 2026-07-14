# Contributing to flagfish

Thanks for being here. flagfish is pre-alpha, which means the highest-value contributions right now
are **tests that prove a property** and **bugs in the design**, not features.

Before you write code, read [`docs/adr/`](docs/adr/). The decisions most likely to be re-litigated
are written up there — the schema, the data layer, the job queue, the score model, the hot-path
lock, and the absence of a plugin system. If you disagree with one, open an issue arguing against
the ADR. That is a welcome conversation. What is not welcome is a PR that quietly relitigates one.

---

## The non-negotiables

These five rules are the ones that hold the project together. A PR that breaks one does not get
merged, however good it is otherwise.

### 1. `internal/domain` imports nothing

Not the database. Not HTTP. Not River. Not your logger. Pure types and pure functions: the decay
formula, the flag comparison, the policy decision, the account-mode duality.

**Why.** Two reasons, and the second one is the real one.

- It makes the invariant tests **fast and total**. Property tests over the scoring model run
  thousands of interleavings in milliseconds because there is nothing to set up. The instant
  `domain` grows a `*pgxpool.Pool`, that becomes an integration test, and an integration test you
  can only afford to run a hundred times is a different, weaker test.
- **It forces the invariants to be stated somewhere they can be checked.** A scoring rule tangled up
  with a query is a rule you can only verify by running the query. A scoring rule that is a pure
  function is a rule you can *prove*.

CI enforces the import graph: `domain` → nothing; everything else → `domain` + `db`; nothing imports
`httpapi`. Without the check, `domain` grows a database handle within a month — that is not a
hypothetical, it is what happens to every codebase that only writes the rule down.

```console
$ task check-boundaries
```

### 2. Back invariants with constraints, not checks

**Every `SELECT`-then-`INSERT` is a race.** If the database can express the rule, the database
enforces it — and the Go code handles the conflict.

```go
// NO. Two callers can both pass the check before either inserts.
if exists, _ := q.SolveExists(ctx, ...); exists {
    return ErrAlreadySolved
}
q.InsertSolve(ctx, ...)

// YES. The unique constraint is the arbiter. Zero rows returned IS the duplicate signal.
id, err := q.InsertSolve(ctx, ...)   // ON CONFLICT DO NOTHING ... RETURNING id
if errors.Is(err, pgx.ErrNoRows) {
    return ErrAlreadySolved
}
```

A mutex in Go does not fix this; it fixes it on *one process*. The constraint fixes it on all of
them, forever, including from `psql` at 3am.

### 3. Stamp the fact; do not recompute it

`solves.value` is stamped at solve time. `submissions.attributed_account_id` is stamped inside the
submit transaction. **A snapshot is a fact; a join to a mutable row is an opinion.**

Do not add code that mutates `solves` or `awards` after insert. Those tables are append-only, and
scoreboard time-travel is only correct if they are append-only *in practice*, not just in intent.

### 4. Loud beats silent

A parse error beats a silently-wrong config. A hard failure beats a quietly-degraded anti-cheat
property. When the flag pool runs out, the challenge becomes unavailable and an admin is alerted —
it does **not** fall back to a shared static flag, because that would silently destroy the
uniqueness property for exactly the late registrants you were most suspicious of.

Never swallow an error. Never return a naked `err` from a boundary — wrap it with `%w` and context.

### 5. The hot path is sacred

`internal/gameplay` submit. Read [ADR-0006](docs/adr/0006-lazy-lock-on-the-hot-path.md) before you
touch it.

The per-challenge `FOR UPDATE` lock is taken **lazily** — after the flag compare, on the
correct-flag path only. Wrong answers are ~99% of submissions and they must never take the challenge
lock. Moving the lock to the top of the transaction is a tidy-looking refactor that turns the
hottest challenge in the event into a queue, and it has **no correctness symptom**. The concurrency
suite asserts that an all-incorrect workload takes zero challenge locks. Do not delete that test,
and do not "fix" it.

Two more traps in that transaction, both easy to hit:

- **Do not use a data-modifying CTE for the solve insert.** A CTE inserts the `submissions` row even
  when the `solves` insert conflicts, orphaning a `type='correct'` submission with no solve. Two
  statements, one transaction.
- The decay recalculation must be **one statement** (`UPDATE … FROM (SELECT COUNT(*) …)`), never a
  read-modify-write. Otherwise concurrent solvers each read a stale count and the last writer wins.

---

## Development setup

You need Go (see `go.mod`), Node (for `web/`), and Docker (for Postgres). Everything else is pinned
in `tools/go.mod` and installed into `./bin`.

```console
$ git clone https://github.com/starvy/flagfish && cd flagfish
$ go -C tools install tool && export PATH="$PWD/bin:$PATH"   # bootstrap: `task` is itself a pinned tool
$ task tools                                                 # sqlc, goose, golangci-lint, air, gofumpt
$ task env                                                   # create .env from .env.example
$ task up                                                    # Postgres (dev :5432, test :5433) + MinIO
$ task migrate
$ task run                                                   # go run ./cmd/flagfish serve --with-worker
```

`task dev` gives you the same thing with live reload. `task --list` shows everything.

**Postgres 17 specifically.** Not "a Postgres", not MySQL. The schema uses features that are not
portable, deliberately: a schema that must be expressible in four dialects is a schema that cannot
express its own invariants. See [ADR-0003](docs/adr/0003-postgres-only-no-redis.md).

### The generated code

```console
$ task sqlc          # internal/db/queries/*.sql  →  internal/db/*.gen.go
$ task sqlc-diff     # fails if the generated code is stale
$ task generate      # sqlc + go:generate
```

- **Never edit `internal/db/*.gen.go`.** It is generated. Edit the SQL in `internal/db/queries/` and
  regenerate.
- **The goose migrations in `internal/db/migrations/` are the schema.** There is no other definition
  of it; sqlc reads that directory. A schema change that breaks a query **fails the build**, which
  is the entire reason sqlc is in the stack ([ADR-0002](docs/adr/0002-sqlc-not-an-orm.md)).
- The emitted `openapi.yaml` is committed, and CI runs `git diff --exit-code` on it. An API contract
  change shows up as a **diff in code review**, in the PR that caused it.

## Testing

```console
$ task test                # unit + invariant tests over internal/domain
$ task test-integration    # the integration suite, real Postgres on :5433
$ task test-concurrency    # the concurrency suite. Start here.
$ task test-security       # the authorization suite
$ task ci                  # everything CI runs. If this is green, the PR is green.
```

### Real Postgres. Never a mock.

This is not a style preference, and it is not negotiable.

**The invariants live in the constraints — and a mock cannot fail the way a database can.** A mocked
repository will happily accept two solves for the same `(challenge, account)`, because a mock does
not have a unique index. It will accept a `NULL` where the column is `NOT NULL`. It will let the
hint-unlock double-charge through. A test suite built on mocks would pass on every race in the
concurrency suite, and every one of those races would still be in the product.

So: `test/concurrency` runs N goroutines against a real Postgres 17 and asserts on the rows that came
out. `test/integration` drives the HTTP API. `e2e/` drives a *running* binary over HTTP with a
generated OpenAPI client.

The layers, and what each one is allowed to assume:

| Layer | Where | Oracle |
|---|---|---|
| **Invariant / property tests** | `internal/domain/**` | Mathematics. Zero database. |
| **Concurrency suite** | `test/concurrency` | The database's own constraints. **Start here.** |
| **Authorization suite** | `test/security` | Each test fails if its guard is removed. |
| **Importer goldens** | `internal/platform/importer` | Real archives, and a checked-in golden report. |
| **Integration / E2E** | `test/integration`, `e2e/` | The documented behavior — because we *decided* it is. |

⚠️ **Every behavior test for a surprising rule needs a comment that says "this looks like a bug and
is not."** A handful of behaviors are deliberately counterintuitive: pause does not block hint
purchases; unverified logged-in users are *more* restricted than anonymous ones; admins see
**frozen** data on the public scoreboard and live data on the admin one (the bypass is
call-site-driven, not role-driven). These tests exist so that someone in year two does not "fix"
them. **The test is the comment that cannot rot.**

### Writing a test in the concurrency suite

This is the most valuable thing you can contribute. The shape:

```go
// <the race, in one line>
// Assertion: <the invariant, stated as a row count or a bound>
```

Spawn N goroutines, `sync.WaitGroup`, hit the real code path against real Postgres, then assert on
the rows. If the assertion can be satisfied by a Go-side lock, you are testing the wrong thing — the
point is that the *constraint* holds even when two processes race.

## Conventions

- **`context.Context` is the first argument. Always.** No `context.Background()` outside `main`.
- **No `interface{}` / `any` in domain code.** If you need a discriminated union, write one.
- Errors wrap with `%w` and carry context.
- Table-driven tests. Golden files for the importer.
- Comments explain the **why**, not the what. Sparse. If a comment restates the code, delete it.
- `task fmt`, `task lint`, and the import-graph check all run in CI.

### Commits

**Commit messages explain *why*, not *what*.** The diff already says what changed.

```
gameplay: take the challenge lock after the flag compare

A top-of-transaction lock serializes every *wrong* guess — ~99% of
submissions — through one row. Contention should scale with solves,
not submissions. Pinned by the lazy-lock test in test/concurrency.
```

Conventional-ish prefixes by package (`gameplay:`, `db:`, `httpapi:`, `docs:`) are preferred but not
enforced. What is enforced is that the message answers "why".

### Pull requests

- Scoped and reviewable. One concern per PR.
- If it touches the hot path, the schema, or the scoring model, say which ADR it lives under — or
  propose a new one.
- **If it changes behavior that a test does not cover, add the test first.** In this codebase a test
  is not evidence that the code works; it is the *specification* of what "works" means.

## Adding an ADR

Copy [`docs/adr/0000-template.md`](docs/adr/0000-template.md), take the next number, and add it to
the index in [`docs/adr/README.md`](docs/adr/README.md). Be honest about what was **given up** — an
ADR that lists only benefits is marketing, and the next person will not trust it.

## Where things live

```
cmd/flagfish/        the single binary: serve | worker | migrate | import | export | restore | admin
cmd/flagfishctl/     the CLI — a first-party consumer of the same public API bots use
internal/domain/     imports NOTHING. scoring, flags, policy, account mode.
internal/db/         sqlc-generated + goose migrations + hand-written queries
internal/gameplay/   the submit hot path
internal/scoreboard/ standings, freeze, brackets, time travel
internal/anticheat/  detector queries. reads only.
internal/audit/      read API. the writes are Postgres triggers, not Go.
internal/platform/   importer, exporter, tasks
internal/httpapi/    Huma + chi. nothing imports this.
test/concurrency/    the product pitch
e2e/                 black-box suite: a generated client drives a running server over HTTP
web/                 React + Vite. built to dist/, go:embed'd.
```

## Code of conduct

Be decent. Argue with the design, not with the person. Disagreement backed by a citation always
wins; disagreement backed by a preference usually loses.
