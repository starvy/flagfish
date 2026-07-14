# ADR-0002: sqlc + pgx/v5 + goose. No ORM, no query builder.

- **Status:** Accepted
- **Date:** 2026-07-14

## Context

The workload is not CRUD over a graph. It is:

- **one very hot transactional write path** (flag submission);
- **a handful of gnarly analytical aggregates** — standings is a `UNION ALL` of two grouped selects,
  re-grouped and inner-joined, with a freeze predicate and a bracket filter;
- ~26 read shapes driven by a per-role field mask;
- a bulk import that is inherently raw SQL;
- and a set of concurrency fixes that are all *"add a unique constraint and use `ON CONFLICT`"*.

The deep-relations story (teams → users → submissions → flags → audit) is real, but note what we
actually *do* with those relations: **we aggregate over them, we rarely traverse them.** That
distinction is decisive, because graph-traversal ORMs optimize for the thing we do least.

The candidates were sqlc, ent, GORM, Bun, sqlx, and Bob.

## Options considered

Evidence, not vibes: the five hardest queries in the product were written out in **all four**
leading candidates. The result:

| | sqlc | ent | GORM | Bun |
|---|---|---|---|---|
| Standings (`UNION ALL`) | native SQL, typed, build-verified | **builder cannot express it** — drops to raw | raw, runtime-mapped | native builder |
| Submit hot path | explicit; atomicity is visible | half-ent, half-raw | works, but implicit | explicit, clean |
| Decay recalc (`UPDATE … FROM`) | one statement | **no idiom** | raw | raw-ish |
| Attribution join | fine | fine (its best showing) | fine | fine |
| Import (bulk restore) | raw | raw | raw | raw |

**Three of the five hardest things this system does are raw SQL in every candidate.** So the
question is not "which ORM writes my queries" — none of them do — but "what do I get for the
queries the ORM *can't* write."

## Decision

**sqlc generating against pgx/v5, migrations as plain-SQL goose files, and no query builder.**

`internal/db/migrations/` **is** the schema — sqlc reads that same directory as its type source, so
there is exactly one place where the truth lives. `internal/db/*.gen.go` is generated and must
never be hand-edited.

The reliability argument outranks the ergonomics argument, and it is best made by asking what
happens **when someone renames a column**:

- **sqlc** → `sqlc vet` / the build fails. You find out in CI.
- **Bun, or ent-with-raw-SQL** → a runtime error, in production, on the scoreboard endpoint.
- **GORM** → `Scan` maps by column name and **silently yields the zero value.** Scores read `0`.
  The page renders. The scoreboard is wrong and nothing anywhere says so.

This is a scoring system. **Its failure mode is not a 500 — it is a wrong number that nobody
notices.** That last row is disqualifying.

**goose over golang-migrate and Atlas**: plain SQL files, healthy, no DSL, no service, no paywall
(Atlas moved `migrate lint` out of its free tier in v0.38), and sqlc reads the same directory.

## Consequences

### What this buys

- **Schema drift is a build failure, not a bug report.** `sqlc vet` runs every named query against
  a real migrated schema in CI.
- **Zero N+1 exposure by construction** — you write the join or you do not get the data. There is
  no `Preload` to forget.
- The hot-path transaction reads like the sentence that specifies it: `ON CONFLICT DO NOTHING …
  RETURNING id` returning zero rows *is* the duplicate-solve signal.
- No reflection, no hooks, no soft-delete, no implicit `WHERE`-clause pruning anywhere near a flag
  submission.

### ⚠️ What we gave up

**sqlc cannot build dynamic queries.** This is a hard, current limitation (sqlc #3414 / #364): no
conditional or optional `WHERE`. The admin list endpoints take `q` + `field`, and we encode the
closed field enum **in SQL**:

```sql
WHERE (@q::text = '' OR (
    (@field::text = 'name'     AND c.name     ILIKE '%'||@q||'%') OR
    (@field::text = 'category' AND c.category ILIKE '%'||@q||'%')))
```

Yes, that is index-unfriendly. It does not matter: these are admin list screens, over a few
thousand rows, behind an authenticated session. Adding a query builder as a second dependency — a
second, *unverified* way to write SQL that will inevitably leak out of the admin package — is a
permanent architectural cost to solve a performance problem we do not have. **Revisit only if a
profiler, not an aesthetic, says so.**

**Codegen friction is real.** Every query change means re-running `sqlc generate` and committing
the output.

**sqlc has a bus factor of one.** Commits are ~all one maintainer; releases are down to roughly two
a year. We accept it, and the reason is structural: **sqlc is a build-time generator and the
artifact is Go source we commit.** If sqlc were abandoned tomorrow, this repository still compiles,
still passes tests, and still ships. The blast radius is "we stop regenerating and hand-maintain
`internal/db/*.gen.go`" — a bad afternoon, not an unpatched runtime dependency in the serving path.

**Bun would have been a defensible choice**, and it is worth saying so plainly. It is healthy, it
can genuinely express the standings query in-builder (the only ORM here that can), and it composes
the freeze/bracket/admin conditionals *better* than sqlc does. What you give up is exactly one
thing — build-time SQL verification — and that one thing is the highest-value property available to
a system whose worst failure is a quietly incorrect number.

### What would change this decision

A team-level judgment that codegen friction is unacceptable. In that case: take Bun, and add a
schema-drift integration test that runs every named query against a migrated database in CI. That
test buys back most of what you gave up.

Not Bob — it is the intellectually correct answer to sqlc's one weakness (type-safe *and*
dynamically composable) and it is **0.x with rapid breaking minors**. Adopting a pre-1.0 data layer
for a system meant to run for years, in order to avoid an ugly `WHERE` clause on an admin screen,
is a bad trade. Watch it; do not build on it yet.
