# Design decisions — the long form

This is the evidence behind flagfish's architecture: the options that were on the table, the
properties of the domain that discriminated between them, and what each choice cost.

The eight load-bearing decisions are also recorded as short [ADRs](../adr/README.md). Those are
the "what and why in one page" version, meant for a contributor about to relitigate one. **This
document is the long form** — the comparison tables, the maintenance evidence, and
[Appendix A](#appendix-a--the-five-hardest-queries-in-all-four-candidates), which is the actual
argument for the data layer: the five hardest queries in the product, written out in every
candidate. Where an ADR exists, the entry here links to it and does not repeat it.

**The stack:**

> **Postgres 17 (only)** · sqlc + pgx/v5 · goose · River · Huma + chi · Go, one static binary
> **React + Vite** · TanStack Table + TanStack Query · TS types generated from the emitted OpenAPI
> Clean schema · per-solve score snapshots · a lazily-locked hot path

**Two principles run underneath almost everything below.** Each of them independently settles
three unrelated questions, which is a decent smell test that they are real principles and not
slogans:

1. **Stamp the fact; don't recompute it.** A snapshot is a fact; a join to a mutable row is an
   opinion. This settles the score model (`solves.value`), flag attribution
   (`submissions.attributed_account_id`), and the team-score question.
2. **Back the invariant with a constraint, not a check.** Every `SELECT`-then-`INSERT` is a race.
   If the database can express the rule, it must. This settles flag issuance
   (`UNIQUE(instance_id)`), duplicate solves (`ON CONFLICT` is the arbiter), config
   (`UNIQUE(key)`), and the singleton import task (a partial unique index).

Sections are numbered so other chapters can cite them; the numbers mean nothing outside this file.

---

## The forces — what the domain demands

Generic "sqlc vs ent vs GORM" advice is worthless here. What follows are the properties of the
CTF-scoring domain, and of this system's own workload, that actually discriminate between the
options. Everything downstream is scored against them.

**Concepts come in kinds, and the kind decides which invariant holds.**
Flags are static, regex, or per-account unique. Submissions are correct, incorrect, partial,
discarded, or rate-limited. A file is attached to a challenge, or to a page, or to a solution, or
to nothing. Awards are manual grants, hint spends (negative), or first-blood bonuses. The naive
model — one wide table per family, one nullable FK column per sibling, a `type` discriminator to
say which one is live — puts the real rule ("exactly one owner FK is set, and *which* one depends
on `type`") somewhere the database cannot see it. The domain has discriminated unions in it; the
schema has to either enforce the discrimination (`CHECK (num_nonnulls(...) <= 1)`, per-kind tables,
typed columns) or hand the invariant to application code forever. This is the first thing a schema
decision has to get right, and it is why the schema question gates the data-layer question.

**The duplicate-solve rule is a uniqueness constraint, and it is the template for everything else.**
"An account solves a challenge at most once" is `UNIQUE (challenge_id, user_id)` and
`UNIQUE (challenge_id, team_id)`. It is not a `SELECT` before an `INSERT`. Under concurrency the
`SELECT` proves nothing; the constraint proves everything. `INSERT … ON CONFLICT DO NOTHING
RETURNING id` returning zero rows *is* the `already_solved` signal, and it is the only correct one.
Every other check-then-insert in the product is measured against this pattern.

**Account-mode duality touches every gameplay query.**
A deployment runs in **user mode** or **team mode**. Both `user_id` and `team_id` are written on
every submission, solve, and award row; the mode only selects which column you **read**. Roughly
fifteen query families are affected: standings, per-challenge solve counts, decay input, rate-limit
keys, prerequisite checks, the duplicate-solve check, submission listings, hint unlocks, statistics.
This is the most invasive cross-cutting concern in the codebase, and it has to be resolved
explicitly — in the query set, at the type level, or at the schema level — because a mode that can
silently re-point every scoring query at a different column is a scoreboard that can silently change.

**Standings is a `UNION ALL` of two grouped selects, re-grouped, and joined back to accounts.**
Score is `SUM` over two different kinds of scoring event — solves and awards — which live in
different tables with different shapes. So the query is: select the scoring events from each, union
them, group by account, join to the account table, order. Three consequences the design has to
decide *consciously*:

- The join from the account table to the score subquery is an **inner join**: an account with no
  non-zero scoring events is absent from the scoreboard entirely, not shown at zero. That is a
  visible product behavior, chosen deliberately.
- The tiebreak needs a key that is comparable **across both event kinds**. Comparing
  `MAX(solves.id)` with `MAX(awards.id)` compares two unrelated sequences: deterministic, arbitrary,
  and not stable across an import. The tiebreak key has to be time, plus a stable id for the
  microsecond collision.
- Freeze, brackets, and hidden/banned accounts are all predicates on this one query. It carries
  most of the product's read complexity, and no ORM will write it for you.

**Decay makes "the current value" a moving target, so a score is either stamped or joined.**
Dynamic scoring lowers a challenge's value as more accounts solve it. That means "what is a solve
worth?" has two possible answers, and they are not close:

- **Join the live challenge row.** Standings `SUM(challenges.value)`. Then a *later* solve by a
  *different* account changes *your* score. Every score in the system becomes a function of every
  future event, and history is destroyed as it runs — nothing anywhere records what a solve was
  worth when it happened.
- **Stamp the value at solve time.** Standings `SUM(solves.value)`. The scoreboard becomes
  append-only, the first solver keeps what they earned, and the audit trail can quote a number that
  is a fact rather than a recomputation.

There is no third option, and the choice reaches into the schema, the hot path, the job set, and the
audit trail. It is settled in §10.

**Freeze is a SQL predicate; the freeze bypass belongs to the *view*, not the *role*.**
When the board is frozen, scoring events after the freeze time are excluded: `date < freeze`,
strictly. The bypass is not "admins see everything" — it is "the *admin scoreboard* sees live data,
the *public scoreboard* is the public scoreboard, including for admins." That distinction has to be
a named view in the code (`ScoreboardView{Public, Admin}`), not a defaulted function parameter that
one call site happens not to pass.

**Visibility decomposes into three layers, and the SQL-expressible layer is small and closed.**
This is the best news in the domain. The complete set of predicates that must live in a `WHERE`
clause is:

1. `account.banned = false AND account.hidden = false`
2. `solves.date < freeze AND awards.date < freeze` (when the view is frozen and non-admin)
3. `solves.value <> 0`, `awards.value <> 0`
4. `account.bracket_id = ?` (optional)
5. `<solves|submissions|awards>.<user_id|team_id> = ?` (the account-mode column)
6. `challenges.state <> 'hidden'` on the non-admin detail query

Everything else is a **route policy gate** computable from
`(config, authed, is_admin, verified, banned, team_banned, teamless, ctftime, paused, mode)` with
**zero row-level database input**, plus a **field-redaction** layer that nulls out `score`/`place`
on rows already fetched. One policy layer, six SQL predicates, a field mask — see the
[policy chapter](_arch/02-policy.md). A closed set that small is exactly what makes a non-ORM data
layer viable: there is no combinatorial explosion of dynamic `WHERE` clauses to build.

**The hot path is ~99% wrong answers.**
That is what a CTF *is*: teams guess, mostly wrong, at high frequency, and occasionally one is
right. Every design choice on the submit path has to be scored against that ratio. A guard taken at
the top of the submit transaction is a guard taken on every wrong guess. Contention must scale with
**solves**, not with **submissions** — see the [hot path chapter](_arch/05-hotpath.md).

**Config is ~100 rows, read on nearly every request, changed a few times per event.**
It wants: a unique key (a read by key against a table that permits duplicate keys is
nondeterministic), a declared type per key (a stringly-typed store that guesses on read —
`isdigit()` → int, `"true"` → bool — defers every type error to read time, far from the code that
caused it, and misparses a value that is legitimately the string `"12345"`), and an invalidation
story that cannot go stale. At 100 rows the correct cache is *no cache*: hold all of it in memory
and swap the pointer.

**Caching is an invalidation-graph problem, not a TTL problem.**
The values worth caching — standings, an account's score and place, config — are not
time-expiring; they are event-expiring. They go stale the instant a scoring event lands, and they
are correct indefinitely otherwise. A TTL is therefore both too slow (stale for the length of the
TTL) and too fast (re-computing an unchanged answer). What the system actually needs is the graph:
*this write invalidates these reads*. Note the sharpest edge of that graph: **standings are
affected by every submission that scores, and by nothing else** — a naive "invalidate the
scoreboard on every submit" makes a brute-forcing team thrash every connected client.

**Whole-instance import is bulk SQL, it is destructive, and it is the only long-running operation.**
Restoring an archive is: recreate the schema, disable FK enforcement, bulk-load every table in
dependency order with IDs preserved, resync sequences, re-enable FKs. No ORM helps with any of that
in any language. It runs for minutes, it erases the instance it runs against, it must never be
retried automatically, and exactly one may be in flight at a time. Its status is something an admin
stares at in a progress bar — which makes that status **domain state**, not queue state.

**Every check-then-insert in a scoring engine is a race, and they cluster in a small, known set of
places.** This is the list. Each one is a real, reproducible interleaving, each is worth a test, and
each is fixed by a constraint rather than by a more careful `SELECT`:

| Race | The interleaving | The fix |
|---|---|---|
| **Duplicate solve** | Two concurrent correct submissions for the same account and challenge both see "not solved yet". | `UNIQUE (challenge_id, user_id)` / `(challenge_id, team_id)`; `ON CONFLICT DO NOTHING` is the arbiter. |
| **Double-unlock / negative score** | Concurrent hint purchases each double-insert the unlock **and** its negative award. Worse, if the affordability check reads a *cached* score, N concurrent unlocks all see the pre-spend balance — **the score can go negative**. | `UNIQUE (hint_id, account)`, `award_id UNIQUE NOT NULL` (exactly one charge), and the balance read taken `FOR UPDATE` on the account row, from `SUM`, never from a cache. |
| **Cap bypass under concurrent registration** | `count-then-insert` on max users / max teams / max team size: N concurrent registrations all read the pre-insert count and blow past the cap. | The count must be taken under a lock on the row that owns the cap, or expressed as a constraint. |
| **Flag-issuance double-assignment** *(ours)* | Two concurrent first-views of a unique-flag challenge consume two pool entries for one account — or hand one pool entry to two accounts, which destroys the anti-cheat property outright. | `PRIMARY KEY (challenge_id, account_id)` + `UNIQUE (instance_id)`. |
| **First-blood double-award** | Two concurrent first solvers both count zero prior solves and both believe they were first. | Decided under the challenge lock, and backed by a partial unique index: at most one first-blood award per challenge. |
| **Decay last-writer-wins** | Concurrent solvers each read a stale solve count and recompute the value from it; the last writer wins and the value is wrong. | One statement — `UPDATE … FROM (SELECT COUNT(*) …)` — never a read-modify-write. |
| **File-location double-insert** | Concurrent uploads of the same path both check "not present" and both insert. | `UNIQUE (location)`. |
| **Rate-limit counter loss** | A counter incremented as read-modify-write loses increments under concurrency — the limiter under-counts exactly when it matters. | `INSERT … ON CONFLICT DO UPDATE SET n = n + 1` on a window-keyed row: one atomic statement. |
| **Upsert that 500s** | A unique constraint exists, but the integrity error is uncaught, so the *correct* schema still produces a 500 on the race. | The conflict is *handled* (`ON CONFLICT … DO UPDATE`), not merely prevented. |
| **Session/tracking insert race** | A check-then-insert on a per-(user, IP) tracking row hits an integrity error, whose rollback path can take the user's session with it. | `UNIQUE (user_id, ip)` and a real upsert. |
| **Two imports in flight** | Two admins both start an import; both are destructive. | `CREATE UNIQUE INDEX … ON tasks (kind) WHERE state IN ('queued','running')`. |

Fixing these is nearly free *if the schema is yours to constrain*. That is most of the argument in
§1. The [concurrency suite](_arch/07-testing.md) hammers every row of this table with N goroutines
against a real Postgres, and it is the first thing built.

**The three headline features all land on the hot path.**
**Unique flags**, the **audit trail**, and **first blood** (see
[TARGET-FEATURES](TARGET-FEATURES.md)) each add a read or a write to the flag-submission
transaction. They are the reason the hot path is designed rather than assembled, and they are the
main new load on the data layer. Retrofitting any of them into a shipped schema is the expensive
path; they are designed in from day one.

---

## Part 1 — The stack

### 1. A clean schema, with a one-way import adapter

Recorded as [ADR-0001](../adr/0001-clean-schema-with-import-adapter.md).

**The question.** Should the schema be *ours* — modeled on the domain, with the invariants above
carried by constraints — or should it be shaped to be bit-compatible with the archive format we
want to import, so that import is free?

**The options.**

- **(a) Adopt the foreign schema.** Same tables, same columns, same discriminator-plus-nullable-FK
  families, same untyped key/value config. An archive imports byte-for-byte, and an in-place
  migration of a live instance becomes conceivable.
- **(b) A clean schema plus a translating one-way importer.** Model the domain properly: one table
  per concept, typed config, `solves` self-sufficient, real `audit_log` / `challenge_instances` /
  `flag_issues` tables. Read foreign archives at the edge and map them in.
- **(c) A clean schema and no import at all.** Greenfield.

**The tradeoffs.**

| | (a) foreign schema | (b) clean + adapter | (c) clean, no import |
|---|---|---|---|
| Archive import | free | a bounded, one-time adapter | not supported |
| ORM viability (ent / GORM / Bun) | **poor** — none of them model single-table inheritance; thirteen concept families become one wide nullable struct plus a type enum, and subtype typing is lost | good — one Go type per table is exactly what they want | good |
| sqlc viability | good (SQL is indifferent to table shape) | good | good |
| Constraint fixes | **constrained** — adding `UNIQUE` to a table diverges from the compatibility contract | free | free |
| Query clarity | every scoring query carries a `submissions ⋈ solves` join | `solves` is one table | same |
| Config | untyped key/value, coerced on read, duplicate keys possible | typed, `UNIQUE(key)` | same |
| The three headline features | awkward siblings inside inherited tables | natural | natural |

**The decision: (b).** One table per concept. No `type` discriminator with sibling-nullable FKs.
`solves` is self-sufficient and carries its own stamped `value`. `config` has `UNIQUE(key)`.
`audit_log`, `challenge_instances`, and `flag_issues` are real tables. A one-directional importer
reads CTFd export archives and maps them onto that schema (§17, and the
[import chapter](_arch/04-import.md)).

**What drives it.** The invariants in "The forces" are only *free* if the schema is ours to
constrain. Under (a), every one of them — `UNIQUE(location)`, `UNIQUE(config.key)`, a unique
constraint on hint unlocks — is a **divergence from the compatibility contract you just signed**.
You would be preserving byte-compatibility with a shape whose defining property is that it cannot
express its own rules. Nobody is asking for that.

The second half of the argument is about Go. Single-table inheritance is a serialization of Python
class inheritance into DDL. Go has no inheritance; adopting that shape means hand-writing a
discriminated union over a wide nullable struct for **thirteen** families, and every one of them is
a place where a subtype's invariant lives in a code comment instead of in the database. That is the
exact failure mode this project exists to eliminate.

**What we gave up.** The adapter is the honest price, and it is real work: polymorphic `type`
columns to unfold, historical variants of the `requirements` field (JSON in some versions, a string
in others), a double-encoded custom-field value from MariaDB deployments, and an ID-preservation
and sequence-resync contract. But it is **bounded, one-directional, and testable against real
archives** — a batch job with a golden-file suite, at the edge. Compare (a), where the foreign
shape is in the type of *every query you will ever write*. Pay once, at the door; not forever, in
the core.

**Why not (c).** (c) saves the adapter and costs the entire migration story — the single strongest
adoption argument the product has. The adapter is a couple of weeks. Don't trade the go-to-market
for two weeks.

**What would reopen it.** A hard requirement to migrate a *live* foreign instance in place — not
"import an archive" but "point the new binary at the old database." Nothing in the product calls for
that, and it would be a one-time operation dictating the permanent shape of the schema.

---

### 2. sqlc + pgx/v5. No ORM, no query builder

Recorded as [ADR-0002](../adr/0002-sqlc-not-an-orm.md). The evidence for it is
[Appendix A](#appendix-a--the-five-hardest-queries-in-all-four-candidates), which is the part of
this decision worth reading.

**The workload, stated precisely.** It is not CRUD over a graph. It is: one very hot transactional
write path (flag submission); a handful of gnarly analytical aggregates (standings, decay,
statistics, the progression matrix); ~26 read shapes driven by a per-role field mask; a small closed
set of SQL-expressible visibility predicates; a bulk restore that is inherently raw SQL; and a set
of concurrency fixes that are all *"add a unique constraint and use `ON CONFLICT`."*

The deep-relations story (teams → users → submissions → flags → audit) is real, but note **what we
actually do with those relations: we aggregate over them, we rarely traverse them.** That
distinction is decisive. A graph-traversal ORM optimizes for the thing this workload does least.

**Scoring the candidates against the real schema.**

| Criterion | sqlc | ent | GORM | Bun |
|---|---|---|---|---|
| **Standings (`UNION ALL`)** | Plain SQL; params typed; **verified against the real schema at build time**. | **The builder cannot express `UNION ALL`.** Drops to raw `QueryContext` + a hand-written scan — ent contributes *nothing* to the most important query in the product. | Raw SQL + `Scan` into a hand-written struct; mapping by column name; silent zero-value on mismatch. | **Can** express it — real `UnionAll` and CTE support in the builder. Typed-ish, but nothing checks it against the schema. |
| **Submit hot path** | `q.WithTx(tx)` is generated; `ON CONFLICT … RETURNING` is natural; the unit of atomicity is visible in the code. | Workable (`client.Tx`), but the interesting statements are raw anyway. | Workable; implicit hooks and soft-delete are a liability inside a scoring transaction. | Workable; clean and explicit. |
| **Decay recalc (`UPDATE … FROM`)** | One statement, typed. | **No idiom** — raw. | Raw. | Raw-ish. |
| **Raw-SQL escape hatch** | Not an escape hatch — it *is* the model. | Poor: raw drops you out of ent's typing entirely. | Good (`db.Raw`), untyped. | Good; the builder degrades gracefully. |
| **Codegen friction** | Real: any query change means re-running codegen. Dynamic queries need care (below). | High: the schema is Go code, and ent owns migrations via Atlas. Fighting it is painful. | None (runtime reflection) — which is also the problem. | None. |
| **N+1 exposure** | **Zero by construction** — you write the join or you don't get the data. | Explicit `.WithX()` eager-loading; easy to forget. | `Preload` — a classic N+1 factory. | `Relation()` — same. |
| **Compile-time drift detection** | **Yes** — codegen reads the migrations; a schema change that breaks a query **fails the build**. | Yes, but drift is defined against *ent's own* schema, not against SQL you wrote. | **No.** | No. |
| **Bulk import** | Raw SQL / `COPY` — identical in all four. | Same. | Same. | Same. |
| **Maintenance health** | Active; bus-factor 1 — but it is a *build-time* generator, so abandonment ≠ an unpatched runtime dependency. | **Declining** (see below). | Healthy. | Healthy. |

**The reliability argument, which outranks the ergonomics argument.** This is a scoring system. Its
failure mode is not "500" — it is **a wrong scoreboard that nobody notices.** So judge each
candidate on what happens when someone renames a column:

- **sqlc** → `go generate` fails, or the build fails. You find out in CI.
- **Bun / raw-through-ent** → a runtime error, in production, on the scoreboard endpoint.
- **GORM** → `Scan` maps by column name and **silently yields the zero value.** Scores read `0`.
  The page renders. The scoreboard is wrong and nothing anywhere says so.

That last row is disqualifying, and it is not close. GORM's implicit behaviors — zero-value
conditions silently dropped from a `WHERE`, hooks firing inside your hot path, soft-delete — are
exactly the class of magic you do not want between a flag submission and a `solves` row. Its
productivity story is real; it is aimed at CRUD apps where a silently-dropped predicate is a visible
bug rather than an invisible one.

**Rejecting ent, plainly.** Its builder cannot express `UNION ALL`, so standings — the single most
important read in the product — drops to `QueryContext` and a hand-written scan. It has no
`UPDATE … FROM` idiom, so the decay recalculation is raw too. It wants to **own the schema and the
migrations** via Atlas, which collides with a hand-tuned schema. And its headline feature — graph
traversal with eager-loading — is precisely the thing this workload does least.

**sqlc's one real weakness: dynamic queries.** The admin list endpoints take a `q` + `field` pair
where `field` is a small **closed enum** per endpoint and the filter is `col = q` (int) or
`col ILIKE '%q%'` (text). sqlc builds only static queries. Three ways out:

1. Encode the closed enum in SQL:
   ```sql
   WHERE (@q::text = '' OR (
       (@field::text = 'name'     AND c.name     ILIKE '%'||@q||'%') OR
       (@field::text = 'category' AND c.category ILIKE '%'||@q||'%')))
   ```
2. Hybrid: sqlc for ~95% of queries, plus a query builder for the handful of dynamic admin lists.
3. Generate one query per (endpoint, field). Ugly; don't.

**We take option 1, and explicitly do not add a query builder.** Yes, it is index-unfriendly. It
does not matter: these are *admin list screens*, over tables with a few thousand rows, behind an
authenticated admin session. You would be optimizing a sequential scan over 2,000 challenges that
runs when one admin types into a search box. Adding a second, unverified way to write SQL to the
dependency tree — one that will inevitably leak out of the admin package — is a permanent
architectural cost to solve a performance problem we do not have. Revisit if a profiler, not an
aesthetic, says so.

(Confirmed as a hard, current limitation, 2026-07-13: sqlc supports only static queries, with no
conditional or optional `WHERE`. `sqlc.narg` and `sqlc.embed` work and are useful; `sqlc.slice` is
the `IN` escape hatch but cannot be used with prepared queries — on Postgres you would write
`= ANY($1::int[])` natively anyway. pgx/v5 is a first-class target; Postgres arrays and JSONB map
cleanly through pgx types.)

**Maintenance health of the data-layer candidates** *(verified 2026-07-13 via the GitHub API and
release feeds; several of these contradict the conventional wisdom, and the evidence ages — treat
the date as part of the claim)*:

| Library | Latest release | Last commit | Signal |
|---|---|---|---|
| **pgx v5** | v5.10.0 (2026-06-03) | active | **Healthy.** The foundation under sqlc, Bob, and River. |
| **sqlc** | v1.31.1 (2026-04-22) | 2026-07-05 | **Adopt with eyes open.** Commits are almost all one maintainer; releases down to ~2/year. Bus-factor 1 — but the artifact is *generated code you own*. |
| **ent** | v0.14.6 (2026-03-23) | 2026-05-31 | **Measurably declining.** ~15 commits Nov 2025 → May 2026; 496 open issues, 123 open PRs. The maintainer's product is now Atlas. |
| **GORM** | active | active | Healthy by activity. The objections above are design, not maintenance. |
| **Bun** | v1.2.18 (2026-06-17) | active | **Healthy.** |
| **sqlx** | — | **2024-05-30** (~26 months stale) | **Effectively dead**, with no archive banner, so it still *looks* alive on pkg.go.dev. Do not score "Bun/sqlx" as one option — they are not in the same health class. |
| **Bob** (`stephenafamo/bob`) | v0.48.0 | very active | Type-safe *and* dynamically composable — the intellectually correct answer to sqlc's weakness. Cost: 0.x, with rapid breaking minors. |
| **go-jet** | active | active | Type-safe builder; effectively solo-maintained. |
| **SQLBoiler** | — | — | Self-declared maintenance mode; its own README points at Bob and sqlc. Ruled out. |

**On sqlc's bus-factor-1, which is the strongest objection.** It is real, and it is unusually cheap,
for a structural reason: **sqlc is a build-time code generator, and its artifact is Go source you
commit.** If it were abandoned tomorrow, the repository still compiles, still passes tests, still
ships. The blast radius of abandonment is "we stop regenerating and hand-maintain the generated
package" — a bad afternoon. Contrast a *runtime* dependency going unmaintained: that is an unpatched
library in the serving path. These are not the same risk and should not be priced the same. Vendor
the generated code, pin the sqlc version in CI, and the exposure is close to zero.

**Why Bun is a legitimate runner-up and not a booby prize.** It is healthy, it is the only ORM here
that can genuinely express the standings query in-builder, it composes the freeze/bracket/admin
conditionals *better* than sqlc does, and it has zero codegen friction. What you give up is exactly
one thing — **build-time SQL verification** — and that one thing is the highest-value property
available to a system whose worst failure is a quietly incorrect number. If codegen friction ever
becomes intolerable, Bun plus a schema-drift integration test (run every named query against a
migrated database in CI) buys back most of what sqlc provides.

**Why not Bob.** It is the correct answer to sqlc's weakness, and it is 0.x with rapid breaking
minors. Adopting a pre-1.0 data layer for a system meant to run for years, to avoid one ugly `WHERE`
clause on an admin screen, is a bad trade. Watch it; don't build on it yet.

**What we gave up.** Codegen friction on every query change, and an ugly closed-enum `WHERE` on the
admin list endpoints.

---

### 3. goose for migrations. Plain SQL files

**The options.** goose · golang-migrate · Atlas.

**Maintenance health** *(verified 2026-07-13)*:

| Tool | Status |
|---|---|
| **goose** | v3.27.2 (2026-06-30), active. **Healthy.** |
| **golang-migrate** | v4.19.1 (2025-11-29), commits to 2026-07-05. **Recovered** from a maintainer gap, but carries a 477-item backlog and its releases lag its commits. Its `dirty`-state failure mode is a well-known 3am ops paper-cut. |
| **Atlas** | Active, and **moving features behind a paywall**: `atlas migrate lint` left the free tier in v0.38 (2025-10-30). Relevant mainly because ent's migration story *is* Atlas. |

**The decision: goose.** Plain SQL files, no DSL, no service, no vendor relationship — and **sqlc
reads that same migration directory as its schema source**, so the migrations *are* the schema
definition and there is exactly one place where the truth lives. That coupling is the whole
argument: it is what makes "rename a column and the build fails" true.

Migrations run behind a Postgres advisory lock — see §23.

---

### 4. Huma for the API. The emitted OpenAPI document is a product surface

**The constraint.** Compile-time drift detection **on both ends** (Go and TypeScript),
CI-enforceable, *plus* a clean public REST/OpenAPI surface. The public API is a **first-class
product surface** — bots, CLIs, third-party integrations — not a courtesy to our own SPA. The
surface is roughly 90 paths / 155 operations / 24 namespaces, with ~26 distinct role-masked entity
shapes, and the same mask governing writes. See the [API chapter](_arch/03-api.md).

**The options.**

- **(a) Huma (Go-first).** Handlers declare typed input/output structs; OpenAPI 3.1 falls out. Feed
  it to `openapi-typescript` for the SPA.
- **(b) oapi-codegen (spec-first).** Hand-author the OpenAPI document; generate Go server interfaces
  and a TS client from the same artifact.
- **(c) Connect/Buf (protobuf contract).** `.proto` is the source of truth; generate Go server and TS
  client; emit REST/OpenAPI via a gateway or plugin.

**The tradeoffs.**

| | Huma | oapi-codegen | Connect/Buf |
|---|---|---|---|
| Go-side drift | Compile-time: the handler *is* the schema. | Compile-time against the spec — but the **spec** can drift from intent; you must remember to edit it. | Compile-time from `.proto`. |
| TS-side drift | Generated OpenAPI → `openapi-typescript`; CI regenerates and diffs. | Same. | Strongest — a generated TS client from the same IDL. |
| Public REST/OpenAPI surface | **Native.** OpenAPI is the output, not an afterthought. | **Native.** The spec is the artifact you publish. | **Weakest.** REST is a *derived projection*; the idiomatic surface is Connect/gRPC. Third-party bot authors expect plain REST and an OpenAPI document. |
| Who owns the contract | Go code | the spec document | the `.proto` |
| Ecosystem weight | light | light | heavy (buf toolchain; protobuf types leak into Go) |

**Maintenance health** *(verified 2026-07-13 — this materially changes the comparison)*:

| Component | Status |
|---|---|
| **Huma** | v2.38.0, active — **but the founder has disengaged** (8 commits in 365 days); a second maintainer now merges everything. **Best-in-class OpenAPI 3.1 support**, plus an in-tree `Downgrade()` that also serves `/openapi-3.0.yaml` — which matters, because much of the Go/TS codegen ecosystem is still 3.0-only. |
| **oapi-codegen** | v2.7.2 (2026-07-07), **two active maintainers** — the healthiest Go-side option. But: **OpenAPI 3.1 support merged to `main` 2026-06-20 and is not in any tagged release** — v2.7.2 is 3.0-only. And v2.7.1/v2.7.2 are both fixes for **Go code injection from a malicious spec** (GHSA-rjwr-m7qx-3fjr) — review generated diffs; never generate from an untrusted spec. |
| **Connect** | connect-go v1.20.0; **CNCF-governed since 2024**, which de-risks abandonment. protobuf-es v2.12.1 (15.3M weekly downloads). The healthiest *governance* of the three. |
| **Connect → OpenAPI** | **There is no first-party OpenAPI generator.** The only credible option is `sudorandom/protoc-gen-connect-openapi` (OpenAPI 3.1, near-zero backlog) — **solo-maintained, pre-1.0**. (`protoc-gen-openapiv2` belongs to grpc-gateway and emits Swagger 2.0.) |
| **openapi-typescript / openapi-fetch** | **The weak link, and it sits under both (a) and (b).** 6M weekly downloads on `openapi-fetch`, 4M on `openapi-typescript` — and the last 100 commits on `main` are all from a bot. Last human commit 2026-02-27. **`openapi-fetch` is in maintenance mode by maintainer decision**: no new features, ever. It works and won't break, but nobody is home. |
| **Hey API** (`@hey-api/openapi-ts`) | The main alternative TS generator (2.9M weekly downloads), actively developed — by one person, at 0.99.0, with a hosted registry and no public pricing page. Read that as a likely monetization path. |

So the honest shape of the decision is a straight trade between the two ends: **(a)/(b)** give a
first-class REST/OpenAPI surface on a maintenance-mode TS client; **(c)** gives a genuinely healthy,
well-governed TS client on a REST surface that is a second-class projection built by one person's
pre-1.0 plugin. There is no option that is healthy on both ends. The decision is *which end to take
the risk on*.

**The decision: (a) Huma, emitting a committed `openapi.yaml`, with CI gated on
`git diff --exit-code`.** TypeScript types are generated with `openapi-typescript`, consuming Huma's
in-tree `Downgrade()` 3.0 output. No protobuf.

**Connect is eliminated by our own constraint.** If the public REST API is the product surface, then
under Connect that surface is a derived projection served through the least healthy component in the
entire stack. The option whose selling point is a healthy TS client would have us serving the
*contract* — the thing bots and integrators depend on, the thing that cannot be changed once it is
out there — through a solo pre-1.0 plugin. **Take the risk at the leaf, not at the contract.** Buf
also drags protobuf types into the Go domain layer and a whole toolchain into the build, for a
90-path REST API.

**Huma over oapi-codegen: the number that decides it is 155.** Hand-authoring and hand-maintaining an
OpenAPI document with ~155 operations, 24 namespaces, and ~26 role-masked entity shapes is not
"designing your contract" — it is maintaining a second, unexecutable copy of your type system in
YAML, and it will drift. Not through carelessness: a 155-operation YAML file is a place where
mistakes go to hide, and the compiler cannot see it. Spec-first shines at ~20 operations designed by
committee across teams; at this size it becomes a large, quiet liability.

And the "designed by accident" objection to code-first has a cheap, complete fix: **commit the
generated document and fail CI on drift.** Every schema change then shows up as a diff, in code
review, in the PR that caused it. That is deliberate design where it matters, without maintaining
the artifact by hand — and it is ~10 lines of CI. On health, oapi-codegen's two active maintainers
are a genuine point in its favor; they do not outweigh being a spec-first tool that cannot yet
release the current spec version.

**The risk we accept, named.** The TypeScript half of "drift detection on both ends" rests on a
feature-frozen library. We accept that deliberately, because **a type generator is a bounded, solved
problem**: "no new features, ever" is near-harmless for a tool whose entire job is
`OpenAPI schema → TypeScript types`. It is not a database driver; it does not need to evolve. If it
ever does break, swapping to another generator — or writing one, since it is a schema walk — is a
contained problem *at the leaf of the dependency graph*.

**The error envelope is designed once, up front.** Huma defaults to RFC 7807
`application/problem+json`; we take it. One envelope for every status code, including 404/500/429.
Generators understand it, and it kills an entire class of inconsistency for free.

**What we gave up.** A hand-designed spec document, and any protobuf-shaped future. Note the escape
hatch is unusually good: **the emitted OpenAPI document is a portable artifact.** Migrating to a
different generator means feeding our own emitted spec to it. That is precisely why committing the
document to the repo is worth doing on day one.

---

### 5. chi, under Huma's chi adapter

**The decision, and the finding: it does not matter, and it must stay that way.** `humachi` on top of
chi v5. Ten minutes of work, then never think about it again. **The failure mode here is not picking
wrong; it is letting the router become load-bearing.**

Our middleware needs are modest and known: request ID, panic recovery, **real client IP**
(non-optional — the submission IP is a recorded field, and there is a proxy in every real
deployment), CSRF for cookie auth only (token auth is CSRF-exempt), the policy gate, rate limiting,
structured logging.

**Health check** *(verified 2026-07-13)*: all three candidates are healthy; this decision carries no
maintenance risk. chi v5.3.1 (2026-07-06). echo v5.3.0 (2026-07-12; v5 is GA). Stdlib `net/http`
since Go 1.22 gives method matching, single-segment wildcards, `{path...}`, exact-match `{$}`,
most-specific-pattern-wins precedence, and automatic 405 — but **no** route groups, sub-routers,
per-route middleware chains, or built-in middleware.

- **Why chi over stdlib.** Stdlib can *route* this fine, but we would hand-roll a smaller, worse,
  untested version of `chi/middleware`. chi is `http.Handler` all the way down and effectively
  dependency-free. **You cannot get locked in by chi**, which is the entire point: the day stdlib is
  enough, the handlers already have the right signatures.
- **Why not echo.** Not a quality objection — echo v5 is healthy. It is that echo's own `Context`
  type **leaks into every handler signature in the codebase**. The correct amount of lock-in to
  accept from a mux is zero.

---

### 6. River for background jobs. Not Kafka, not RabbitMQ, not a hand-rolled outbox

Recorded as [ADR-0004](../adr/0004-river-for-background-jobs.md).

**What actually wants to be a job.** Three things, and it is worth being precise, because the volume
argument for a queue is nonexistent at ~5 writes/sec:

| Work | Why it wants a queue |
|---|---|
| **Email** (registration, verification, password reset, …) | It blocks the HTTP response on a 1–3 second network call, and a failed send needs a retry rather than a swallowed exception. |
| **Webhooks** (first blood → chat) | Outbound HTTP to a flaky third party is the canonical retry / backoff / poison-pill workload. |
| **Import / export** | Long-running; needs durable status, progress, and a failure surface. See §7. |

**And here is the property that decides the choice.** A side effect triggered from inside a database
transaction is a **dual write**. Send inline, and a rolled-back transaction has still sent the email,
still hit the webhook, **still announced a first blood for a solve that did not happen.** Avoiding
that bug class is the *entire* reason to have a queue — so a queue that reintroduces it is worse than
no queue at all.

**The options.** (a) River — Postgres-backed, jobs in *our* database. (b) asynq — Redis-backed.
(c) A hand-rolled `SELECT … FOR UPDATE SKIP LOCKED` outbox, ~200 lines, zero dependencies.
(d) Kafka / RabbitMQ. (e) No queue; a goroutine with a retry, for email only.

| | River | asynq | none |
|---|---|---|---|
| New infra | **none** (uses Postgres) | **requires Redis** (couples to §8) | none |
| Transactional enqueue | **Yes** — enqueue in the same transaction as the domain write. | No — a second system; the dual write is back. | n/a |
| Durability / retry / dead-letter | yes | yes | **no** — a restart loses in-flight work |
| Throughput ceiling | lower (Postgres-bound) — irrelevant at our volume | higher | n/a |
| Ops surface | one datastore | two | zero |

**Kafka and RabbitMQ are eliminated first, and it is not about scale.** Both live *outside* Postgres,
so enqueueing becomes a dual write — precisely the bug the queue exists to prevent. You would pay a
large ops cost to *reintroduce* it, and then need a Postgres outbox anyway, at which point the broker
is doing nothing the outbox wasn't. The rule: **a broker earns its keep when the producer and the
consumer are different services owned by different people.** Ours are the same binary. A broker
between two functions in one process is not architecture; it is a network hop with a YAML file. (If
an external consumer ever appears — a public event stream, an analytics pipeline — the move is
unchanged: write to Postgres transactionally, publish to the bus *from the outbox*. That path stays
open and costs nothing to defer.)

**The real contest is River vs. the hand-rolled outbox**, and it very nearly wins: no license
question, no open-core, no v0.x, no vendor. Two things decide it.

1. **The "200 lines" figure is true right up until the job rescuer.** It covers the table, the
   poller, backoff, and max-attempts. It does *not* cover: a worker takes a `SIGKILL` mid-send, its
   job is stuck in `running` forever, and nothing ever picks it up again. Fixing that means leases,
   heartbeats, or a reaper that can distinguish "the worker died" from "the job is legitimately
   slow." That is fiddly, that is where the bugs live, and **that is the line item that turns 200
   lines into 800.** It is near-worthless for email (SMTP fails fast) and **load-bearing for
   webhooks**, which is to say: the moment webhooks land, you need the expensive part — which is
   exactly when you least want to be building it.
2. **The asymmetry of being wrong.** Take River and webhooks never ship: you carried one dependency
   to send email. Zero harm. Hand-roll it and webhooks ship: you now maintain a job system as a side
   project, and you find that out at 3am, during a live event, while the chat integration is
   flapping. Under genuine uncertainty you don't pick the better expected value — **you pick the
   option whose bad branch you can live with.**

**River over asynq — the technical reason, not the health reason.** River's job table lives in *your*
Postgres, so `INSERT INTO users …; river.Insert(tx, SendWelcomeEmail{…})` is **one transaction**:
either the user exists and the email is queued, or neither happened. asynq enqueues into Redis — a
second system — which is the same dual write as a broker, in a smaller package.

**Maintenance health** *(verified 2026-07-13 — both candidates have a catch)*:

- **River** — v0.40.0 (2026-07-02), pushed daily, 5.4k stars, 58 open issues. Very active. Still
  **v0.x** (API stability by convention, not promise). **MPL-2.0**, not MIT — file-level copyleft;
  see §22. **Open-core**: "River Pro" is a paid module.
- **asynq** — the original author has not committed since **2023-07-08**. A single volunteer now
  merges everything. Latest release v0.26.0 (2026-02-03); the release gaps tell the story —
  v0.24.1 (2023-05) → v0.25.0 (2024-11) → v0.26.0 (2026-02). Backlog: 216 open issues, 67 open PRs.
  A one-volunteer rescue of an abandoned project with a three-year backlog. Not dead; not something
  to put a scoring system's durability on.

**The queue configuration.**

| Queue | `MaxWorkers` | `MaxAttempts` | Why |
|---|---|---|---|
| `email` | ~5 | ~15 | Standard exponential backoff. Retries are cheap; nobody is watching. |
| `webhooks` | ~5 | ~10, **plus a deadline** | See below. |
| `maintenance` *(import/export)* | **1** | **1** | **Never auto-retry a destructive operation.** Isolated so a 20-minute import cannot starve email. See §7. |

**Webhooks need a TTL, not just a retry cap.** River's default policy retries for roughly three
weeks. A first-blood announcement that lands in chat three weeks after the event ended is worse than
one that never lands — it is noise.

```go
if time.Since(job.CreatedAt) > time.Hour {
    return river.JobCancel(errors.New("announcement too stale to be useful"))
}
```

**Retries exist to survive a blip, not to resurrect a moment that has passed.** The default policy is
tuned for *eventually-must-happen* work (email), not *now-or-never* work (announcements).

**Do not wrap River in our own interface.** Applying the seam rule from §15 to ourselves: build a
seam only when it has two real implementations today, or when retrofitting it later needs a schema
migration. Neither applies. It is `river.Insert(tx, args)` at ~10 call sites; migrating away is a
find-and-replace, not an architecture.

**The two catches, and the real one is not the one you would expect.**

- **MPL-2.0.** File-level copyleft. For a server-side application, a non-event — we do not modify
  River's files, so nothing propagates into ours. It *is* a different license class from everything
  else in the stack, so it is stated on purpose rather than discovered later. flagfish ships as a
  self-hostable binary, so what we distribute alongside it is River's source and notice; there is no
  obligation beyond that.
- **The dead-letter queue is not the paid feature.** OSS River gives `state = 'discarded'` for jobs
  that exhaust their retries: durable, inspectable with a `SELECT`, re-insertable, reaped by the job
  cleaner after a configurable window. That is a dead-letter queue in every sense that matters. Also
  OSS: unique jobs, `JobCancel`, `MaxAttempts`, per-queue `MaxWorkers`, and insert-only clients
  (which §7 leans on). **The real Pro gotcha is cross-process concurrency limits** — per-queue
  `MaxWorkers` is a limit *within one worker process*; a *global* cap across a fleet is the paid
  feature. At our scale, run one worker replica and the problem does not exist. But that is the wall
  to remember if workers ever scale horizontally.

**The decay recalculation stays synchronous, and that is the interesting part.** The per-solve
snapshot (§10) means the scoreboard sums `solves.value` and stops depending on `challenges.value`
entirely — so deferring the recalc to a job would no longer make the scoreboard stale, and it
*becomes* freely deferrable. We still don't defer it. Inside the submit transaction we are already
holding the challenge lock (§12); the recalc is a **single `UPDATE … FROM (SELECT COUNT(*))`**
against a row we have already locked — tens of microseconds, on a lock we hold anyway. Deferring it
buys nothing and costs real things: a window where the *displayed* price is wrong, an extra failure
mode, and a job to monitor. **Async is not free; it is complexity paid for with eventual
consistency.** Spend it on the 3-second SMTP call, not on a 50µs `UPDATE` under a lock you are
already holding.

That is why the job set is small — three queues, not eight job types. Resist the urge to queue things
that are fast and want to be transactional.

**What we gave up.** A dependency on a v0.x library with a commercial tier, and an MPL file in the
dependency tree.

---

### 7. Long-running tasks: a `tasks` table owns the state; River owns the execution

**The trap is conflating this with §6.** Import and export are not "just another River job," and the
distinction is not technical — it is **who owns the row the user is looking at.**

| | Email / webhooks | Import / export |
|---|---|---|
| Who is watching | nobody | **an admin, staring at a progress bar** |
| Retry | yes, aggressively | **never** — retrying a half-done database restore 25× with exponential backoff is a catastrophe |
| Idempotent | roughly | **no** — an import erases the instance |
| Concurrency | many | **exactly one, ever** |
| Result | fire-and-forget | durable status, progress, error text, cancel |

Task status is **domain state**, not queue state. The admin panel shows *"Import started 3 min ago by
alice · 47% · restoring `submissions` · [Cancel]"*. That is product data, in the typed API contract,
rendered by the admin UI.

**The decision. They layer; they do not compete.** `tasks` owns the *state*. River answers exactly one
question: *after a process crash, does something reliably pick this up?*

```go
// POST /admin/import — the transactional outbox again, and the third place it pays off
BEGIN
  INSERT INTO tasks (kind, state, created_by, …) VALUES ('import','queued',…) RETURNING id
  river.Insert(tx, RunImportArgs{TaskID: id})     // same tx: no orphan task, no orphan job
COMMIT                                            // → 202 { task_id }

// GET /admin/tasks/{id} → reads OUR tasks table. Never touches river_job.
```

**The rule this enforces: never expose another library's schema as your own API.** Reading `river_job`
and digging progress out of its JSONB metadata would make River's internal table part of our public
contract, and a River migration could then break the admin panel. The `tasks` table costs one
migration and buys a stable, typed, product-shaped surface.

**Enforce the singleton in the database, not in the worker:**

```sql
CREATE UNIQUE INDEX tasks_one_in_flight ON tasks (kind) WHERE state IN ('queued','running');
```

`MaxWorkers: 1` is *per client* — run three replicas and you get three concurrent imports. A partial
unique index makes "at most one import in flight" true **regardless of how many workers exist**, and
true even for a task inserted by some future code path that never goes through River. Constraint, not
check.

**The trap to design for now, because discovering it late produces a bad fix.** The restore is **one
transaction** (real rollback). That means **progress `UPDATE`s inside that transaction are invisible
until it commits** — the progress bar sits at 0% for eight minutes and then jumps to 100%. So progress
is written on a **second connection**, outside the import transaction:

```go
importTx     := pool.Begin(ctx)    // the big one: COPY, FK disable, setval — rolls back as a unit
progressConn := pool.Acquire(ctx)  // separate, autocommit — writes tasks.progress as it goes
```

Discovering this late tends to trigger the fix "just commit per table," which throws away the rollback
guarantee — the single most valuable property of the importer.

**Cancel then falls out for free, and cleanly:** `river.JobCancel` → the worker's `ctx` is cancelled →
pgx cancels the query → the big transaction **rolls back**. Cancel is safe *because* it is one
transaction.

**Is River the best library for this? Honestly, no — it is just the one we already have.** Import and
export are the workload *least* in need of a job library: one row, one worker, run a handful of times
in an instance's entire life, no retry, no backoff, no throughput. Everything valuable River brings is
aimed at email and webhooks. The argument for using it here is purely that **two background-work
mechanisms in one codebase is worse than one slightly-oversized mechanism** — two failure modes, two
observability stories, two things a new contributor has to learn.

**Worker topology: a role, not a mandatory deployment.** A long import inside the API process competes
with request latency. But `docker compose up` is a real product property, and forcing a self-hoster to
run a second deployment just to import an event is a regression. River's insert-only client is built
for exactly this:

```
flagfish serve                # API. river.Client with Workers: nil → inserts jobs, never works them
flagfish worker               # river.Client with workers registered
flagfish serve --with-worker  # both in-process — the docker-compose default
```

Self-hosters get one container. Large events split the roles and get: imports cannot touch API p99,
deploying the API does not kill a running import, and **the process that erases and restores the
database is not the one serving traffic.** One binary, two roles, flag-selected. Don't force the
topology.

---

### 8. Postgres only. No Redis

Recorded as [ADR-0003](../adr/0003-postgres-only-no-redis.md).

**The four jobs a Redis would conventionally be doing here**, and what Postgres does instead:

| Job | The Redis answer | The Postgres answer |
|---|---|---|
| **SSE fan-out** across processes | pub/sub | `LISTEN/NOTIFY` — a **drop-in semantic match**. The contract is broadcast-only: no per-user routing, no replay. The 8 kB `NOTIFY` payload cap is a *feature*: publish an id, let clients re-read. |
| **Sessions** | a keyed blob with a TTL | a `sessions` table with an index on `expires_at` and a nightly `DELETE`. Redis's TTL is a convenience, not a capability. |
| **Standings / score / config memoization** | a read-through cache | a table refreshed on the same events, or an in-process snapshot. The hard part is the **invalidation graph**, and that hard part is *identical* in both options. |
| **Rate-limit counters** | `INCR` + `EXPIRE` | `INSERT … ON CONFLICT DO UPDATE SET n = n + 1` on a window-keyed row. |

**The decision: one stateful dependency.** SSE over `LISTEN/NOTIFY`, sessions in a table, rate-limit
counters as an upsert, config as an in-process snapshot swapped on a `NOTIFY` (§14).

**Run the numbers before running the Redis.** Peak load for a large event is a few hundred flag
submissions per minute — roughly **five writes per second**. Postgres on a laptop does five orders of
magnitude better than that. There is no scaling argument for Redis here; there is a *habit* of
reaching for Redis, and it should be resisted, because **a second stateful system is the single most
expensive thing you can add to an ops story.** It must be deployed, monitored, secured, backed up,
failed over, version-upgraded, and reasoned about during every incident. **You do not add a datastore
because it is the right tool for a workload; you add it because the workload will not fit in the one
you already have.** Ours fits with enormous room to spare.

**Meeting the strongest counter-argument head-on: rate-limit counters.** This is the one genuinely
Redis-shaped workload — atomic increment with expiry. As an upsert it is one more statement on a path
that **already writes a `submissions` row for every single submission**. The marginal cost is one
upsert on a path that already writes — not a new order of magnitude, not a new hot path. And it is
*more* correct than a cache-backed counter, which is atomic only on some backends and silently
lossy on the rest.

**The failure mode we are deleting.** A cache is a **second source of truth with unbounded staleness**
whenever an invalidation is missed. Postgres-only does not merely avoid an ops dependency — it
collapses the class of bug where the scoreboard and the database disagree *and the scoreboard wins*.
For a scoring system, that is the whole ballgame.

**Coupling.** This decision and §6 are one decision in two halves. Postgres-only + River is coherent
(transactional enqueue is *only* possible because the job table lives in the same database as the
domain write). Redis + asynq is coherent. Postgres-only + asynq is not.

**What we gave up.** Redis is genuinely the better primitive for counters and pub/sub taken in
isolation, and we are not using it. If the day ever comes, both are behind an interface and can be
moved *for those two things alone* — having avoided the dual-write problems in the meantime.
**Postgres-first is not a bet against Redis; it is a decision not to pay for it until it earns its
keep.**

---

### 9. React + Vite, with TanStack Table and TanStack Query

**The workload.** The SPA consumes the generated client. It needs a scoreboard with live updates over
SSE, a challenge board, an admin panel, and the ~26 role-masked entity shapes as typed responses. See
the [frontend notes](frontend.md).

| | SvelteKit | React + Vite |
|---|---|---|
| Generated-client fit | identical — both consume the generated types | identical |
| Admin panel (tables, forms, filters) | fewer off-the-shelf table/form libraries | **the deepest ecosystem** — TanStack Table/Query, form libraries, component kits |
| Contributor pool | smaller | larger |
| Bundle size | smaller | heavier, and irrelevant at our scale |
| SSR | built-in (unneeded — this is an authed SPA) | opt-in |

**The decision: React + Vite, for exactly one reason.** The **admin panel is the bulk of the UI work**,
and it is almost entirely tables, filters, pagination, and forms over ~26 role-masked entity shapes.
That is the *single* workload where React's ecosystem advantage is not a talking point but a
measurable pile of code we don't write: **TanStack Table** (sorting/filtering/pagination/column
visibility — which maps nearly 1:1 onto the `q` + `field` admin list endpoints from §2) and
**TanStack Query** (caching, invalidation, optimistic updates). Everything else was a wash: the API
decision (§4) makes the framework choice nearly orthogonal, which is itself a point in favor of it.

**Two implementation notes.**

- **TanStack Query's invalidation keys should mirror the server-side invalidation graph.** One mental
  model, not two. And mind the sharp edge from "The forces": do **not** naively invalidate the
  scoreboard query on every submit response, or a brute-forcing team thrashes every connected client's
  cache.
- **SSE is a cache-invalidation signal, not a data channel.** The payload is an id; the client
  re-reads through TanStack Query. One fetch path instead of two divergent copies of the data — which
  is why the 8 kB `NOTIFY` cap is a feature, not a constraint.

---

## Part 2 — Semantics

### 10. Per-solve score snapshot. Decay never rewrites history

Recorded as [ADR-0005](../adr/0005-per-solve-score-snapshot.md). **The most consequential decision in
the project.**

**The fork.** Dynamic scoring decays a challenge's value as more accounts solve it, so "what is a
solve worth" has two answers:

- **(a) Retroactive revaluation.** No `value` column on `solves`; standings `SUM(challenges.value)` by
  joining the live challenge row. Simple, and it is what an imported archive encodes.
- **(b) Per-solve snapshot.** `solves.value NOT NULL`, stamped inside the submit transaction;
  standings `SUM(solves.value)`.

**The decision: (b).** `challenges.value` remains, demoted to what it actually is — **the current
asking price**, shown on the challenge board. Decay updates the price; it no longer rewrites history.

**The engineering case, stated plainly.** Storing a computed value is normally an anti-pattern — but
that rule assumes the inputs are stable. Here **the input is deliberately mutated**, so "recompute
from source" does not mean *recompute*; it means **retroactively rewrite settled facts.** Four things
fall out of fixing that:

1. **The scoreboard becomes append-only.** Under (a), another team solving a challenge changes *your*
   score. You did nothing; your number moved. Every score in the system is a function of every future
   event — that is not a scoreboard, it is a live recomputation that happens to be displayed on one.
2. **The hot-row `UPDATE challenges.value` leaves the scoreboard's critical read path.** Under (a),
   every solve of a popular challenge contends on the one row that every standings query joins to.
3. **The audit trail becomes meaningful.** An audit record that says "awarded 347 points" is worthless
   if 347 is recomputed on read. **A snapshot is a fact; a join to a mutable row is an opinion.** You
   cannot build a defensible audit trail on top of retroactive revaluation — the two features are in
   direct tension, and the tension is invisible until someone asks "why did this team's score change
   overnight?"
4. **Decay becomes freely deferrable** (we decline to defer it anyway — §6), and admin manual grading
   cannot silently leave a stale value behind.

**And the first solver keeps what they earned.** Solve at 500, watch the challenge decay to 100, and
under (a) your 500 quietly becomes 100. Players overwhelmingly believe the opposite is happening, and
the gap between what a scoreboard does and what everyone thinks it does is where accusations of
rigging live.

**The honest case against.** Uniform revaluation is a legitimate design philosophy, not a bug: it means
the board reflects current challenge difficulty uniformly, rather than rewarding whoever was awake at
3am. We are choosing the other philosophy, deliberately and visibly.

**What we gave up.** Importing an archive that never recorded per-solve values is **lossy here**:
`solves.value` can only be reconstructed as "the challenge's value at import time," which is wrong for
every past solve of a decayed challenge. The information was never recorded and cannot be recovered.
The importer states this, loudly, in its report. It is also the reason a differential oracle is off the
table (§21).

---

### 11. Unique flags: a pool of instances, lazily assigned, behind a `FlagIssuer` seam

The design lives in [TARGET-FEATURES](TARGET-FEATURES.md); this is the decision behind it.

**The problem.** Per-account unique flags exist to make flag sharing detectable: if the flag Alice
submits was issued to Bob, that is a fact, not an inference. The question is where the per-account
flag comes from.

**The two mechanisms.**

- **(a) An author-supplied pool.** The author generates N flags — and, in the general case, N
  *artifacts* with those flags baked in — and uploads them. The platform stores them and records who
  got which.
- **(b) Platform-templated HMAC.** The flag is `flag{<prefix>_<tag>}` where
  `tag = HMAC(server_key, challenge_id ‖ account_id)`, truncated. Nothing per-account is stored;
  issuance is a pure function.

| | (a) pool | (b) templated HMAC |
|---|---|---|
| Storage | one row per (challenge, account) | **zero rows** |
| Attribution lookup | indexed lookup on a stored hash → O(1), exact | verify the tag, extract the account → O(1), exact, **no table at all** |
| Arbitrary flag formats | **yes** — the author controls the flag entirely, which is required if the flag is embedded in a binary, a VM image, or a PDF the author built | **no** — the flag must follow the platform's template |
| Author workflow | the author generates and uploads the pool | the author does nothing |
| Key rotation | not applicable | rotating `server_key` **invalidates every outstanding flag** — during a live event, a catastrophe with no undo |
| Revocation / regeneration | replace rows, bump a generation counter | version the key or the template |
| Dynamic per-team instances | the flag is baked into the artifact | the instance must fetch its flag, or the platform must template the artifact |

**The load-bearing question is whether challenge artifacts must *contain* the flag at build time.**
They must. A per-account unique flag and a *shared static artifact* are fundamentally in conflict: if
the artifact is the same bytes for everyone, the flag inside it is the same for everyone, and it is not
unique. So per-account flags require either the author baking N flags into N artifacts, or per-team
dynamic instancing that templates the flag in at runtime. **We do not orchestrate containers** (§15) —
so there is nothing to template a flag *into*. (b) is the right answer to a question we are not yet
asking.

**The decision, in three parts.**

1. **A pool of pre-built challenge instances** (`challenge_instances`), each carrying one flag hash —
   never the plaintext — and optionally an artifact and a set of template variables. **The pool is of
   *instances*, not of flags**, because in the general case the flag is not the only thing that differs
   per account: the artifact differs, the connection string differs, the description differs. A flag is
   one field of an instance, and modeling it as the whole thing would force a schema migration the day
   any of the others is needed.
2. **Lazy assignment on first access.** The pool is populated at import; assignment happens when an
   account first views the challenge or downloads its artifact, in a transaction, `ON CONFLICT DO
   NOTHING`. Assignment cannot be done at import time for the simple reason that **the accounts do not
   exist yet** — teams register after setup. This is the one place in the product where *reading*
   mutates state, so both constraints are load-bearing:
   `PRIMARY KEY (challenge_id, account_id)` (an account is never issued two instances, even under
   concurrent first-views) and `UNIQUE (instance_id)` (**an instance is never issued to two accounts —
   this is the constraint that makes the whole anti-cheat property true**).
3. **Stamp `submissions.attributed_account_id` inside the submit transaction**, whichever
   implementation is live. **This is the part that matters.**

**Point 3 takes (b)'s best property without taking (b).** (b)'s real prize was never zero storage — it
is that **attribution needs no table**, because the account id is *inside* the flag. But even under
(b), sharing detection only works if the attribution was **captured at submit time**. So capture it
under (a) too. The submit path already loads the challenge's flags to compare against; the matching
instance says who it was issued to; write that id onto the submission. Then:

- Sharing detection is a **predicate on `submissions` alone** — `attributed_account_id IS NOT NULL AND
  attributed_account_id <> team_id` — with no join to the issue table at all.
- Attribution **survives** the flag being rotated, regenerated, or deleted. A join-based audit trail is
  only as durable as the rows it joins to; a stamped one is a fact. (Same principle as §10.)
- Swapping the `FlagIssuer` implementation later changes *one method* and leaves every query, report,
  and audit view untouched.

That is the whole trick: **(a)'s storage model with (b)'s query model.** We give up nothing.

**Kill the storage objection, because it is the only real one.** 100 challenges × 500 teams = 50,000
rows. A few megabytes of the most indexable data imaginable. Do not choose a cryptographic scheme to
avoid 50k rows.

**Regeneration is the thing (a) must get right and (b) gets free.** Store the hash, never the plaintext,
and version the pool (`generation`): re-uploading a pool bumps the generation, and old issues keep
pointing at their original instance, so submissions attributed under the previous generation stay
attributable.

**What we gave up.** An author workflow step: for a unique-flag challenge, the author must produce the
pool. And a real operational edge — if the pool is exhausted, the challenge becomes **unavailable** to
further accounts (a clear error, and an alert), because the alternative is silently handing two accounts
the same flag, which destroys the property the feature exists for.

---

### 12. The challenge lock is taken lazily, on the correct-flag path only

Recorded as [ADR-0006](../adr/0006-lazy-lock-on-the-hot-path.md). The full transaction is written out in
the [hot path chapter](_arch/05-hotpath.md).

**The atomic unit.** Timing-safe flag check + solve insert + audit write + first-blood detection, one
transaction. First blood is the hard part: it is a **race**, not a lookup. Under `READ COMMITTED`, two
concurrent first solvers both see zero prior solves and both believe they were first. The same race
destroys the decay recalculation.

**The options.**

- **(a) Serialize per challenge.** `SELECT … FROM challenges WHERE id = $1 FOR UPDATE`. Everything
  downstream — the solve count, first blood, the decay recalc — is then race-free by construction, in
  one transaction.
- **(b) Optimistic, with a derived first blood.** Never store a first-blood flag; define it as
  `MIN(solves.id)` for the challenge. A derived fact can never be wrong or double-announced.
- **(c) `SERIALIZABLE` isolation**, with retry.

**The decision: (a) — but the lock is acquired *after* the flag comparison, not before it.**

```
BEGIN
  read challenge + flags                       -- no lock
  match := FlagIssuer.Check(challenge, provided)
  if INCORRECT:
      INSERT submission(type='incorrect'); COMMIT       -- ← never touches the lock

  SELECT … FROM challenges WHERE id = $1 FOR UPDATE     -- ← lock only now
  INSERT submission(type='correct', attributed_account_id=…)
  INSERT solve(value=<current asking price>) ON CONFLICT DO NOTHING
      → zero rows ⇒ already_solved; COMMIT              -- the unique constraint is the arbiter
  count prior solves → first_blood                      -- exact, under the lock
  INSERT audit_log
  UPDATE challenges.value                               -- one statement, under the lock
COMMIT
```

**Why the reordering is the point.** **The overwhelming majority of submissions are wrong answers** —
that is what a CTF *is* — and a top-of-transaction lock serializes **every wrong guess** on the hottest
challenge through one row. Under a brute-force attempt, or just a popular challenge at 3am, you have
built a queue where you meant to build a guard. Wrong answers need no serialization: they insert an
independent row and race with nothing. **Lock the correct path only.** Contention then scales with
*solves* — a few per second at absolute peak — instead of with *submissions*.

It also keeps the lock's scope honest: it protects **solve ordering**, and nothing else.

**Why not (b).** It is genuinely the more elegant idea, and `first_blood := MIN(solves.id)` can never be
wrong, never double-announced, never raced. But first blood is then no longer decided *inside* the
submitting transaction, so the HTTP response cannot say "first blood!" without a second read — and that
second read reintroduces exactly the race that was removed. (a) gives literally the atomic unit we
specified, and the code reads like the sentence.

**Why not (c).** `SERIALIZABLE` is the theoretically clean answer and the operationally worst one: every
caller must handle serialization failures and retry, forever, on a hot path, to solve a problem that one
row lock solves exactly. Reserve it for many interacting invariants across many rows; here there is one
invariant scoped to one challenge.

**Do the math on the "solves serialize" objection, because it is the only one.** Peak on the single most
popular challenge in a large event: a handful of solves per second. The critical section is ~4 indexed
statements against rows already in cache — low single-digit milliseconds. That is two to three orders of
magnitude of headroom. The lock is free at this scale, and it is *also* what makes the decay recalc exact
rather than last-writer-wins.

**Two implementation notes that are easy to get wrong.**

- **Do not use a data-modifying CTE for the solve insert.** It would insert the `submissions` row **even
  when the `solves` insert conflicts**, orphaning a `type='correct'` submission with no solve. Two
  statements, one transaction.
- **`ON CONFLICT DO NOTHING … RETURNING id` returns zero rows on conflict** — that is the `already_solved`
  signal, and it is the *only* correct duplicate check. Do not `SELECT` first.

**What we gave up.** Solves of a single challenge serialize, and first blood cannot be derived (it is
decided and recorded). Note the migration to (b) stays cheap if scale ever demands it: stop locking,
define first blood as `MIN(solves.id)`, announce from a job. The schema does not change.

---

### 13. Account mode is fixed at setup

**The property.** A deployment is user-mode or team-mode. Both `user_id` and `team_id` are written on
every submission, solve, and award; the mode selects which column is **read**.

**The options.** (a) A runtime config row, changeable at any time. (b) Fixed at instance setup,
immutable thereafter. (c) One mode only, collapsing the duality entirely.

**The decision: (b), and enforce it in the database, not just in the admin UI.**

**The reframe that decides it.** Option (a) is not "preserve flexibility" — it is **preserve a footgun**.
Because both columns are always written, flipping the mode does not *migrate* anything; it **silently
re-points every scoring query at a different column**, instantly and retroactively rewriting every score
on the board, with no migration, no warning, and no audit record. Then ask the product question: *who
needs to switch a running event from individual to team mode?* Nobody. It is not a workflow; it is a way
to destroy an event.

(b) costs zero flexibility that any real user wants, and buys: the invariant is validated **once at
startup** instead of on every query; the mode becomes a value you can put in a struct field and trust;
and an entire class of "why did all the scores change?" incidents becomes impossible by construction.

**Why not (c).** Tempting — it is the only option that *deletes* the duality — but it forecloses half the
market. Team events and individual events are both large, real segments. That is a product amputation to
save query surface, and the query surface is cheap.

**The duality is cheap under sqlc, and that is not a coincidence — it is the sqlc thesis in miniature.**
Two named queries, `GetUserStandings` and `GetTeamStandings`, differing in `team_id`↔`user_id` and
`teams`↔`users`. Both compile-time checked. Both readable. Neither hiding. The ~15 affected query
families become ~15 pairs of named queries: more lines, and **strictly less to reason about** — the right
trade every time in a scoring path.

**One thing to actually build:** a startup assertion. If the mode is `users` but the `teams` table is
non-empty, or vice versa, **refuse to boot**, loudly. Under (a) that inconsistency is a silently wrong
scoreboard; under (b) it is a crash on line one, which is where you want it.

---

### 14. Config: one keyed table, one typed accessor, held in memory

**The four parts of the decision.**

1. **`UNIQUE(key)`.** Config is read *by key*; a duplicate row would make that read nondeterministic.
   Non-negotiable.
2. **Keep the key/value storage shape.** It is what the importer needs — it must ingest arbitrary config
   keys from foreign archives, including keys it has never heard of. Under a one-column-per-setting table,
   an unknown key is a migration or a data-loss event; under a keyed table it is a row. The key/value
   shape is doing real work *at the boundary*; the mistake to avoid is letting that shape leak into every
   call site rather than confining it to storage.
3. **One typed Go accessor in front of it.** A single `Config` struct, typed getters, validation on write.
   Nothing outside that package touches the table. Values are parsed and validated **once, at load**,
   against a declared schema — and a malformed value **fails at boot with a clear error**, rather than
   silently becoming the wrong type at some random call site three months later. That is the alternative to
   guessing the type on every read, which misparses a value that is legitimately the string `"12345"` and
   defers every type error to the point of use.
4. **Load the whole table into an in-process snapshot at boot; refresh on `LISTEN/NOTIFY`.**

```
Boot:            SELECT * FROM config → build the typed struct → atomic.Pointer[Config]
Write:           UPDATE the row, then NOTIFY config_changed
Every replica:   LISTEN config_changed → re-SELECT → atomic swap
```

**Config is ~100 rows that change a few times per event.** The correct cache for that is *no cache*: hold
all of it in memory. Reads become a pointer dereference — no TTL, no invalidation graph, no round-trip,
nothing to get wrong, and no way for a missed invalidation to leave a stale value in place indefinitely.
`LISTEN/NOTIFY` is already wired up for SSE (§8), so this is nearly free — the second time that one
decision pays for itself.

**A bonus that falls out:** an atomic, cache-coherent bulk config `PATCH` becomes trivial — one
transaction over the rows, one `NOTIFY`, one swap. Atomic by construction, rather than as a feature
someone has to engineer.

---

## Part 3 — Scope

### 15. Two seams: `FlagIssuer` and `ArtifactStore`. Nothing else

**The rule.** *A seam is worth building when it has two real implementations today, or when retrofitting
it later requires a schema migration.* Everything else is a YAGNI interface that adds indirection, gets
designed against imagined requirements, and turns out to be **the wrong shape** the moment a real second
implementation appears. A speculative interface is not cheap insurance — it is **a guess, frozen into your
type system, that you will feel obligated to honor.**

Applying the rule:

- **`ArtifactStore` — build it, and it is not speculative at all.** Two real implementations on day one:
  local disk and S3. That is not a seam, it is an interface with two implementations, which is what
  interfaces are for. It also passes the "you would write it anyway" test — the export path has to care,
  and an implementation that downloads an entire bucket before exporting is exactly the bug the
  abstraction exists to make visible.
- **`FlagIssuer` — build it. It is the one genuinely speculative thing we endorse**, and it earns its keep
  on the migration clause: **retrofitting per-instance flag issuance into a static-flag schema is a data
  migration, not a refactor.** Two methods (`IssueFor`, `Attribute`). That is the whole cost, and because
  attribution is stamped at submit time (§11), even the *queries* survive an implementation swap.
- **A challenge-instance *provider* (an orchestrator) — do not build it.** This is the trap: the seam that
  *sounds* most valuable and is the most dangerous to guess at. Per-team container orchestration is a large,
  opinionated subsystem — lifecycle, quotas, TTLs, networking, health, cleanup, cost — whose interface you
  cannot get right without knowing the orchestrator. An interface designed in ignorance of
  Kubernetes-vs-Nomad-vs-Docker will be wrong in ways that are *worse* than no interface, because code will
  be built around it. Note that the instance **pool** (§11) is already a static instance provider: it makes
  the schema, the templating, and every query orchestrator-shaped. If real instancing ever arrives, it swaps
  the *source* of an instance and leaves everything else untouched. That is the whole reason to model the
  pool as instances rather than as flags.

`connection_info` stays a free-text string on the challenge. Email is not a seam, just an interface — it
sits behind River anyway, and SMTP vs. a provider API is two implementations of one method.

**What we gave up.** If per-team instancing lands next quarter, the orchestration interface is designed
then, against a real orchestrator — which is the point, but it does mean it is not designed now.

---

### 16. Behaviors an importing organizer will notice

flagfish is not a bit-compatible reimplementation of the platform it imports from. Most behavior is the
same; a handful of things deliberately are not. This is the list, so that nobody discovers it during a
live event. Each row states **the flagfish rule first**, and the migration note second.

| Behavior | The flagfish rule | What changes for an organizer migrating an archive |
|---|---|---|
| **Scoreboard values after decay** | A solve is worth what it was worth **when it happened** (§10). Standings `SUM(solves.value)`. | Scores no longer change retroactively when a challenge decays, and the first solver keeps the higher value. Imported solves have no recorded historical value, so they are stamped with the challenge's value **at import time** — and the importer says so, per challenge, in its report. This is the largest deliberate divergence. |
| **Tiebreak** | `ORDER BY score DESC, last_scoring_event ASC, account_id ASC`. | The first two clauses mean what they meant. The third exists to keep the board *stable*, not *fair*: it fires only on an exact-microsecond tie at an identical score. A tiebreak that compares ids across two unrelated sequences (submissions and awards) is not reproducible across an import, so it could not be carried over even in principle. |
| **Accounts with no non-zero score** | Absent from the scoreboard entirely, not shown at zero. | Unchanged. It is a deliberate inner join. |
| **Freeze** | Strictly `date < freeze`. The **admin scoreboard** shows live data; the **public scoreboard** shows frozen data, including to admins. | Unchanged in effect, but it is now a named view (`ScoreboardView{Public, Admin}`) rather than a defaulted argument. Nobody can change it by editing a default. |
| **Pause** | Blocks submissions. Does **not** exempt admins, and does **not** block hint purchases. | Unchanged, and now test-pinned and written down — because it looks exactly like a bug, and someone will otherwise "fix" it in year two. |
| **Team score after membership churn** | Points stay with the team that scored them: the score is `SUM` over the rows *stamped* with that team. | This is a real adjudication, not a preservation. Computing a team's score from its *current members'* rows and computing it from the rows stamped with the team **disagree after churn**, and both cannot be true. The stamped semantic is the only one stable under churn, the only one consistent with an audit trail, and the only one that does not let a team farm points by rotating members. (Third place the same principle decides a question: stamp the fact.) |
| **Import erases the instance** | Yes. An import is destructive, single-flight, and never auto-retried (§7). | Unchanged — but it now runs in **one transaction** with a real rollback, so a failed import leaves the instance as it was. |
| **Manual grading** | Creates a solve, stamps its value, and does not rewrite the submission's history. | The value is stamped like any other solve, so a manually-graded solve can no longer go stale. |
| **Member removal vs. team deletion** | A member cannot leave a team that has scored: the departure is refused outright (`ErrTeamHasScored`). Teams are never deleted — ban or hide covers moderation. | Diverged: the old rule deleted the departing member's scoring rows. flagfish refuses the departure instead, so roster churn can never rewrite the ledger — the same append-only rule every scoring table follows. |

**Correctness rules the engine enforces**, each of which is a place a scoring engine can quietly go wrong.
They are listed here because an organizer *should* notice the difference, and because each one is a test:

- A challenge with **no flags configured cannot be solved.** An empty flag set grades nothing correct;
  "all of the flags matched" over an empty set is vacuously true and would award points to anyone who typed
  anything.
- **Case-insensitive comparison means case-insensitive, not prefix-insensitive.** A Unicode fold must never
  turn a prefix match into a full match.
- **A ban applies to every authentication path**, including API tokens. A ban that only covers the session
  cookie is an authz bypass.
- **Required profile fields gate every client**, not just the web UI. A required field bypassed by choosing
  a different client is not a required field.
- **The session identifier is regenerated on every privilege change.**
- **`decay = 0` is rejected at write time**, not silently coerced into something else.
- **Timestamps are `timestamptz`, at microsecond precision, end to end.**
- **One error envelope** — RFC 7807 — for every status code, including 404, 429, and 500 (§4).

---

### 17. Import CTFd archives; export our own format

**The asymmetry is the whole decision, and treating import and export as one feature is the mistake.**

- **Import is a one-time, best-effort, adoption-driving operation.** It runs once per organizer, ever. If
  it is 95% right and reports the 5%, it has delivered the entire value: *I switched platforms and kept my
  event.* Imperfection is acceptable and expected.
- **Export in a foreign format is a permanent, exact, bidirectional compatibility promise.** It means our
  schema can never drift from that format — **forever** — because the moment it drifts, the export is a lie.
  You would be letting a foreign class hierarchy govern your DDL in perpetuity, in exchange for a migration
  path *away* from your own product.

**The decision.** Read CTFd export archives — versioned, best-effort, loudly imperfect. Export **our own
format**, designed by us, with field masking. Do not promise to emit a foreign archive, ever.

Note that promising a foreign-format export would *retroactively force* the schema decision in §1. Do not
let an export format decide your schema.

**And the security argument closes it.** A whole-database dump with no field masking contains password
hashes, API tokens, and secrets. Committing to *emit* that format means committing to ship a
data-exfiltration primitive with a "Download Export" button on it, permanently, by contract. Our export
masks secrets by default — which is strictly better, and is *incompatible* with a bit-compatible export by
construction. A clean way to see that the option was never really available.

**What "import-only, done right" looks like.** The archive declares the schema revision it was taken at;
branch the translation logic on it. That is a `switch` over a handful of known shapes, not a migration
engine — we do not need to *migrate to* the revision, only to *read* it. Golden-file tests against real
archives from each supported version. When something cannot be mapped, **log it loudly and continue**: a
partial import with a clear report beats a failed one, and it beats a *silently* partial one by an infinite
margin. The details are in the [import chapter](_arch/04-import.md).

---

### 18. v1 scope, and what is deferred. No plugin system

Recorded (for the plugin half) as [ADR-0007](../adr/0007-no-plugin-system.md).

**The one part of this that is an engineering decision rather than a product one: do not build a plugin
system.** "Plugin system" means something very different in Go than it does in a dynamic language, where
the story is free: import a module, register into a registry, done. Go's options are:

- **`plugin.so`** — requires *exact* toolchain and dependency-version matching between host and plugin,
  does not work everywhere, and is a support nightmare that ships as a feature.
- **A subprocess/RPC boundary** — real, but now every extension point is an IPC call with serialization,
  lifecycle, versioning, and failure semantics. It puts a **distributed system** inside the flag comparison,
  which lives inside a locked database transaction.
- **WASM** — increasingly viable, still a large surface, and a lot of machinery for what is really "a
  function that compares a string."
- **Build-time registration** — which is not a plugin system; it is a fork with extra ceremony.

Every one of those is a major subsystem. Meanwhile, look at what the extension surface actually needs to
be: **challenge types** and **flag types**. Both are interfaces with two or three methods. So **make them
in-tree interfaces with a registry**, and ship `static`/`dynamic` challenge types and `static`/`regex`/
`unique` flag types as implementations. That is 100% of the extension *value* for ~50 lines and zero plugin
machinery. And the second-order effect is the real one: **a plugin system is a permanent API surface** —
the moment plugins exist, every internal type they touch is frozen.

**The v1 cut.**

| | Item | Why |
|---|---|---|
| **v1** | challenges, flags, submissions/solves, scoring & decay, scoreboard + freeze + tiebreak, users/teams + account mode, the visibility matrix, hints + unlocks, awards, notifications/SSE, auth (session + API tokens), files, config | The core. Nothing here is separable. |
| **v1** | **unique flags, the audit trail, first blood** | They are the reason the project exists, and they are the only things adding load to the submit path — design them in from day one, because retrofitting them into a shipped schema is the expensive path. |
| **v1** | **brackets** | Nearly free: one column and one `WHERE` predicate that the standings query already accounts for. Cutting it saves nothing. |
| **v1, minimal** | the statistics namespace | Ship the cheap aggregates; **defer the progression matrix** (the heavy one, and the least used). Don't cut the namespace wholesale — admins notice immediately. |
| **defer** | **a plugin system** | See above. Replaced by in-tree interfaces. |
| **defer** | themes, the CMS/Pages system | A whole templating subsystem for a product whose UI is an SPA. The reason a platform grows themes is that it is server-rendered; that reason is gone. |
| **defer** | OAuth / external login | Additive; no schema impact. |
| **defer** | ratings & reviews, comments, topics, solutions, audiences | All additive; none touch the core schema. |
| **defer** | CSV import/export | Distinct from archive import (§17). Nice-to-have. |

**The rule behind the cut list, and the only part of it worth holding us to:** defer anything **additive**
(a new table, a new endpoint, no change to the core schema or the submit path); keep anything **structural**
(touches `solves`, `submissions`, scoring, or the visibility matrix). By that rule the three headline
features are v1 no matter how aggressive the cut gets, and everything in the defer column can be added later
without a migration. **That is what makes it safe to cut them — not that they are unimportant, but that they
are cheap to add back.** Beyond-v1 work is tracked in the [roadmap](ROADMAP.md).

---

## Part 4 — Settled details

### 19. Argon2id, verifying bcrypt, rehashing on login

New passwords are hashed with **Argon2id**. Imported bcrypt hashes still **verify**, and are
**transparently rehashed to Argon2id on the next successful login** — the plaintext is in hand exactly once,
at login, so this is free. The bcrypt population drains to zero on its own: no migration script, no mass
password-reset blast.

This is not optional. Archives carry bcrypt hashes; an Argon2id-only build would **lock out every imported
user**, and would discover that on the day someone migrates a live event. Detect the algorithm by hash
prefix (`$2b$` vs `$argon2id$`) — both formats carry it in the hash string.

### 20. Flag types: static, regex, unique

Regex flags are genuinely used (variable whitespace or case around `flag{…}`), so dropping them would break
real challenges and real imports. **Regex matching cannot be made timing-safe.** We accept that and
**document it**: constant-time comparison applies to `static` and `unique` flags. It is not a leak worth
breaking author workflows over.

### 21. Verification: invariants, a concurrency suite, importer goldens

**The concurrency suite is the oracle we build first**, because the interesting failures in a scoring engine
are *races*, and a race does not show up in a functional test. Every row of the race table in "The forces"
gets hammered with N goroutines against a real Postgres. See the [testing chapter](_arch/07-testing.md).

| Layer | What it proves |
|---|---|
| **Invariant / property tests** | `score ≡ SUM(solves.value) + SUM(awards.value)` under **any** interleaving; a solve is never double-counted; standings are deterministic; the score never goes negative. |
| **Concurrency suite** | *Exactly one* solve under 100 concurrent correct submissions. No double-charge under concurrent unlocks. One instance to one account, under concurrent first-views. One first blood. One import in flight. |
| **Importer golden tests** against real archives | The **only** place foreign-format compatibility is tested. Bounded, at the edge. |
| **Table-driven behavior tests** | The visibility matrix (26 shapes × roles), freeze semantics, the tiebreak. |

**A differential oracle is ruled out, and the reason is §10.** The per-solve snapshot means our scoreboard
**deliberately differs** from a retroactively-revalued one, on exactly the numbers that matter most. A
differential test against another implementation would therefore fail on the design and pass on the bugs,
which is the wrong way round. Differential testing is a *clone* strategy; this is not a clone.

**Real Postgres, never a mock.** The invariants live in the constraints, and a mock cannot fail the way a
database can.

### 22. Apache-2.0

Maximum adoption, an explicit patent grant — the one reason to prefer it over MIT for anything a company will
deploy — and zero friction for organizers migrating from an Apache-2.0 platform. AGPL was considered and
rejected: many companies ban it outright, which would measurably cost contributors and corporate adoption,
and adoption is the goal.

**River's MPL-2.0 is compatible.** MPL copyleft is **file-level**: we do not modify River's files, so nothing
propagates into ours. Distribute its source and notice; no obligation beyond that.

### 23. Migrations run behind a Postgres advisory lock

Running migrations on boot with N replicas is a race that can corrupt the schema. Either take a
`pg_advisory_lock` around the migration step, or run it as a separate pre-deploy job. One line; trivially
forgotten; catastrophic.

---

## Coherence check: do these actually fit together?

A pile of locally-optimal choices is not an architecture. Here is the stack, and the couplings that have to
hold:

> **Postgres 17 (only) · sqlc + pgx/v5 · goose · River · Huma + chi · React + Vite**
> Clean schema · per-solve score snapshots · a lazily-locked hot path

**The couplings, checked.**

1. **Clean schema → data layer.** A clean schema puts all four data-layer candidates genuinely back in play,
   so sqlc is chosen **on its merits** rather than by elimination. Worth stating: sqlc is the one choice here
   that is *robust to the schema decision flipping* — SQL is indifferent to table shape, while every ORM
   breaks on single-table inheritance. The data-layer decision does not depend on the schema decision going
   our way.
2. **Postgres-only ↔ River.** These are one decision in two halves. River's transactional enqueue is *only*
   possible because the job table lives in the same Postgres as the domain write. Choosing Redis would make
   asynq the natural partner and hand back the dual-write bug that was the entire reason to adopt a queue.
3. **No orchestrator → an instance pool → the stamped attribution → the locked transaction.** No orchestration
   means static artifacts, which means author-baked flags, which means an instance pool, which means
   attribution is a row you *stamp* in the submit transaction — which is why the audit write lives *inside* the
   locked transaction rather than in a job. Pull on the first and all four move.
4. **The score snapshot unlocks a job we then decline to take.** The snapshot makes the decay recalculation
   freely deferrable; the lock makes it free to keep inline. So the job set stays small. **A decision that
   unlocks an option you then decline is still a good decision** — it is the difference between "we can't" and
   "we won't."

**Two principles, each settling three unrelated questions.**

- **Stamp the fact; don't recompute it.** Settles the score model (`solves.value`), flag attribution
  (`submissions.attributed_account_id`), and the team score after membership churn.
- **Back the invariant with a constraint, not a check.** Settles flag issuance (`UNIQUE(instance_id)`),
  duplicate solves (`ON CONFLICT` is the arbiter), config (`UNIQUE(key)`), and the singleton import task (a
  partial unique index) — and it is what every race in "The forces" is missing.

**Three places one decision pays for itself twice.**

- **`LISTEN/NOTIFY`** is the SSE bus *and* the config-snapshot invalidation channel.
- **The `FOR UPDATE` lock** decides first blood *and* makes the decay recalculation exact rather than
  last-writer-wins.
- **Transactional enqueue** is the outbox for email and webhooks *and* the trigger that creates a task row —
  which is why a broker would be a downgrade, not an upgrade: it breaks the one property both uses depend on.

**The two to defend hardest** are the schema (§1) and the lazy lock (§12): the first because the schema is the
one thing you cannot cheaply change later, the second because the lock ordering is a correctness-and-performance
win that costs nothing and is easy to get wrong if it is not written down.

---

## Summary index

| § | Decision | What we chose | The one line that drove it |
|---|---|---|---|
| **1** | **Schema** *(gates §2)* | **A clean schema + a one-way import adapter** — [ADR-0001](../adr/0001-clean-schema-with-import-adapter.md) | The invariants are only free if the schema is ours to constrain; a foreign shape makes every constraint a breach of the compatibility contract. |
| **2** | **Data layer** | **sqlc + pgx/v5. No ORM, no builder** *(runner-up: Bun)* — [ADR-0002](../adr/0002-sqlc-not-an-orm.md) | Three of the five hardest queries are raw SQL in *every* candidate; only sqlc build-verifies them. A renamed column must fail the build, not silently zero a score. |
| **3** | Migrations | **goose**, plain SQL | sqlc reads the same directory, so the migrations *are* the schema — one place where the truth lives. |
| **4** | API pipeline | **Huma**, with a committed `openapi.yaml` and a CI drift gate | Take the maintenance risk at the **leaf** (a frozen TS type generator), not at the **contract** (a pre-1.0 plugin serving the public API). |
| **5** | HTTP layer | **chi**, via `humachi` | Zero lock-in by construction. A router must never become load-bearing. |
| **6** | Background jobs | **River** — queues `email`, `webhooks`, `maintenance` — [ADR-0004](../adr/0004-river-for-background-jobs.md) | Transactional enqueue makes "the announcement fired, the solve rolled back" impossible. A broker reintroduces it. |
| **7** | Long-running tasks | **A `tasks` table owns the state; River owns the execution.** A partial unique index for the singleton | Task status is *domain state*, not queue state. Never expose another library's schema as your API. |
| **8** | Datastores | **Postgres only. No Redis** — [ADR-0003](../adr/0003-postgres-only-no-redis.md) | Peak is ~5 writes/sec. Redis is a scaling answer to a problem we do not have, and a second source of truth with unbounded staleness. |
| **9** | Frontend | **React + Vite**, TanStack Table + Query | The admin panel is the bulk of the UI work, and it is tables and forms. That is the only real delta. |
| **10** | Score model | **Per-solve snapshot** (`solves.value`) — [ADR-0005](../adr/0005-per-solve-score-snapshot.md) | An audit trail on top of retroactive revaluation is a contradiction. A snapshot is a fact; a join to a mutable row is an opinion. |
| **11** | Unique flags | **An instance pool, lazily assigned, behind `FlagIssuer`** + `attributed_account_id` stamped at submit | Without an orchestrator there is nothing to template a flag *into*. Take the pool's storage model with the HMAC scheme's query model — you give up nothing. |
| **12** | Hot-path concurrency | **Per-challenge `FOR UPDATE`, taken lazily** — [ADR-0006](../adr/0006-lazy-lock-on-the-hot-path.md) | Most submissions are *wrong*. A top-of-transaction lock serializes every wrong guess; contention must scale with solves, not submissions. |
| **13** | Account mode | **Fixed at setup, immutable** | It is not flexibility, it is a footgun: flipping the mode silently rewrites every score with no migration. |
| **14** | Config | **A keyed table + `UNIQUE(key)` + a typed accessor + an in-memory snapshot** | It is 100 rows. Hold it in memory: no cache, no TTL, no invalidation graph, nothing to get wrong. |
| **15** | Infra seams | **`FlagIssuer` + `ArtifactStore`. Nothing else** | Build a seam only if it has two real implementations today, or if retrofitting it needs a schema migration. Exactly two qualify. |
| **16** | Migration-visible behavior | **Documented divergences**, stated rule-first | Scoreboard values after decay, the tiebreak, and the team-score semantic are deliberate. Nobody should discover them during an event. |
| **17** | Archive compatibility | **Import CTFd archives; export our own format** | Import is one-time and best-effort. Emitting a foreign format would be a forever promise that re-decides our schema, and it has no field mask — our own export does. |
| **18** | v1 scope | **Core + the three headline features. No plugin system** — [ADR-0007](../adr/0007-no-plugin-system.md) | "Plugin system" in Go is a distributed system or a recompile. The extension surface is two interfaces — ship them in-tree. |
| **19** | Password hashing | **Argon2id, verifying bcrypt, rehashing on login** | An Argon2id-only build locks out every imported user, and finds out during a live migration. |
| **20** | Flag types | **static + regex + unique** | Regex is genuinely used; it cannot be made timing-safe; say so rather than break real challenges. |
| **21** | Verification | **Invariants + a concurrency suite + importer goldens** | The interesting failures are races, so the race suite is the oracle. A differential oracle is impossible: we deliberately score differently. |
| **22** | License | **Apache-2.0** | Maximum adoption, explicit patent grant. River's MPL is file-level and does not propagate. |
| **23** | Migration safety | **goose behind `pg_advisory_lock`** | Migrating on boot with N replicas is a race. One line; trivially forgotten; catastrophic. |

---

## Appendix A — the five hardest queries, in all four candidates

This is the evidence behind §2. Five queries, chosen because they are where the candidates actually differ,
written against flagfish's schema (see the [schema chapter](_arch/01-schema.md)).

### Query 1 — Scoreboard standings: freeze, tiebreak, bracket filter, hidden/banned accounts

The hardest read in the product: a `UNION ALL` of two grouped selects over two different scoring-event
tables, re-grouped, inner-joined back to accounts.

#### sqlc

```sql
-- name: GetTeamStandings :many
WITH events AS (
    SELECT s.team_id  AS account_id,
           s.value    AS value,          -- the stamped snapshot, not a join to challenges
           s.date     AS event_date
      FROM solves s
     WHERE s.value <> 0
       AND (@freeze::timestamptz IS NULL OR s.date < @freeze::timestamptz)
    UNION ALL
    SELECT a.team_id, a.value, a.date
      FROM awards a
     WHERE a.value <> 0
       AND (@freeze::timestamptz IS NULL OR a.date < @freeze::timestamptz)
),
sums AS (
    SELECT account_id,
           SUM(value)      AS score,
           MAX(event_date) AS last_event_at
      FROM events
     WHERE account_id IS NOT NULL
     GROUP BY account_id
)
SELECT t.id AS account_id, t.name, t.bracket_id,
       b.name AS bracket_name,
       sums.score::bigint AS score,
       t.hidden, t.banned
  FROM teams t
  JOIN sums ON sums.account_id = t.id            -- INNER: scoreless accounts are absent, by design
  LEFT JOIN brackets b ON b.id = t.bracket_id
 WHERE (@admin::bool OR (t.banned = false AND t.hidden = false))
   AND (@bracket_id::bigint IS NULL OR t.bracket_id = @bracket_id)
 ORDER BY sums.score DESC, sums.last_event_at ASC, t.id ASC   -- stable, single id space
 LIMIT NULLIF(@lim::int, 0);
```

```go
// generated — no hand-written struct, verified against the migrated schema at build time
rows, err := q.GetTeamStandings(ctx, db.GetTeamStandingsParams{
    Freeze:    freeze,      // pgtype.Timestamptz, NULL when unset
    Admin:     view == ScoreboardAdmin,   // the view decides the freeze bypass, not the role
    BracketID: bracketID,
    Lim:       limit,
})
```

Account-mode duality is handled by a **second named query**, `GetUserStandings`, differing only in
`team_id`→`user_id` and `teams`→`users`. Both are compile-time checked.

Note what the stamped `solves.value` buys the query itself: no join to `challenges` at all, and no
`submissions ⋈ solves` join either. The hottest read in the product touches two tables and one lookup table.

#### ent

The builder **cannot express `UNION ALL`**. You drop out of ent entirely:

```go
// ent contributes nothing to the most important query in the product.
rows, err := client.QueryContext(ctx, `WITH events AS ( ... )`, freeze, admin, bracketID, lim)
if err != nil { return nil, err }
defer rows.Close()

var out []Standing
for rows.Next() {
    var s Standing
    // hand-written scan, hand-written struct, no schema verification
    if err := rows.Scan(&s.AccountID, &s.Name, &s.BracketID,
        &s.BracketName, &s.Score, &s.Hidden, &s.Banned); err != nil { return nil, err }
    out = append(out, s)
}
```

Same SQL as sqlc, minus the generated types, minus the build-time verification. You pay ent's codegen and
schema-ownership costs and get none of its benefits here.

#### GORM

```go
type Standing struct {
    AccountID   uint    `gorm:"column:account_id"`
    Name        string
    Score       int64
    BracketName *string `gorm:"column:bracket_name"`
    Hidden      bool
    Banned      bool
}
var rows []Standing
err := db.Raw(`WITH events AS ( ... )`, freeze, admin, bracketID, lim).Scan(&rows).Error
```

Also raw. Mapping is by column name **at runtime**: rename a column and you get a silent zero value, not an
error. A silently-wrong scoreboard is the one failure this product cannot tolerate.

#### Bun

Bun is the only ORM here that can actually build it:

```go
solves := db.NewSelect().
    TableExpr("solves AS s").
    ColumnExpr("s.team_id AS account_id").
    ColumnExpr("s.value AS value").
    ColumnExpr("s.date AS event_date").
    Where("s.value <> 0").
    ApplyIf(freeze.Valid, func(q *bun.SelectQuery) *bun.SelectQuery {
        return q.Where("s.date < ?", freeze)
    })

awards := db.NewSelect().
    TableExpr("awards AS a").
    ColumnExpr("a.team_id, a.value, a.date").
    Where("a.value <> 0").
    ApplyIf(freeze.Valid, func(q *bun.SelectQuery) *bun.SelectQuery {
        return q.Where("a.date < ?", freeze)
    })

var out []Standing
err := db.NewSelect().
    With("events", solves.UnionAll(awards)).
    // … sums CTE, join to teams, order by …
    Scan(ctx, &out)
```

Genuinely expressible, reads acceptably, and composes the freeze/bracket/admin conditionals *better* than
sqlc does. What you give up: nothing checks it against the schema until it runs.

**Verdict.** sqlc and Bun both produce good code. ent and GORM both degrade to raw SQL — so for the query
that matters most, choosing them buys nothing.

---

### Query 2 — The submit hot path: flag check + solve + audit + first blood, one atomic unit

Shown with the lazy lock (§12): the lock is taken **after** the comparison, so wrong answers — ~99% of
submissions — never touch it.

#### sqlc + pgx

```go
func (s *Service) Submit(ctx context.Context, in SubmitInput) (Result, error) {
    tx, err := s.pool.Begin(ctx)
    if err != nil { return Result{}, err }
    defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
    q := s.q.WithTx(tx)

    ch, err := q.GetChallengeForSubmit(ctx, in.ChallengeID)   // no lock yet
    if err != nil { return Result{}, err }

    match, ok, err := s.issuer.Check(ctx, q, ch, in.Provided) // constant-time for static/unique
    if err != nil { return Result{}, err }
    if !ok {
        if _, err := q.InsertSubmission(ctx, db.InsertSubmissionParams{
            Type: "incorrect", ChallengeID: in.ChallengeID,
            UserID: in.UserID, TeamID: in.TeamID, IP: in.IP, Provided: in.Provided,
        }); err != nil { return Result{}, err }
        return Result{Status: Incorrect}, tx.Commit(ctx)      // ← the lock was never taken
    }

    // Correct. Serialize solves for this challenge: solve count, first blood and the decay
    // recalc are all race-free under it, in one transaction.
    locked, err := q.LockChallengeForSolve(ctx, in.ChallengeID)  // SELECT … FOR UPDATE
    if err != nil { return Result{}, err }

    sub, err := q.InsertSubmission(ctx, db.InsertSubmissionParams{
        Type: "correct", ChallengeID: in.ChallengeID,
        UserID: in.UserID, TeamID: in.TeamID, IP: in.IP, Provided: in.Provided,
        AttributedAccountID: match.IssuedTo,        // stamped, never joined later
    })
    if err != nil { return Result{}, err }

    // The unique constraint is the arbiter, not a SELECT.
    solve, err := q.InsertSolve(ctx, db.InsertSolveParams{
        SubmissionID: sub.ID, ChallengeID: in.ChallengeID,
        UserID: in.UserID, TeamID: in.TeamID,
        Value: locked.Value,                        // the per-solve snapshot (§10)
    })
    if errors.Is(err, pgx.ErrNoRows) {              // ON CONFLICT DO NOTHING → zero rows
        return Result{Status: AlreadySolved}, tx.Commit(ctx)
    } else if err != nil { return Result{}, err }

    priorSolves, err := q.CountSolvesExcluding(ctx, db.CountSolvesExcludingParams{
        ChallengeID: in.ChallengeID, ExcludeSolveID: solve.ID,
    })                                              // exact under the lock, not a guess
    if err != nil { return Result{}, err }
    firstBlood := priorSolves == 0

    if err := q.InsertAuditEntry(ctx, db.InsertAuditEntryParams{ /* … */ }); err != nil {
        return Result{}, err
    }
    if ch.Function != "static" {
        if err := q.RecalcChallengeValue(ctx, in.ChallengeID); err != nil { return Result{}, err }
    }
    return Result{Status: Correct, FirstBlood: firstBlood}, tx.Commit(ctx)
}
```

```sql
-- name: InsertSolve :one
-- Deliberately NOT a data-modifying CTE with the submission insert: a CTE would insert the
-- submissions row even when this insert conflicts, orphaning a type='correct' submission.
INSERT INTO solves (submission_id, challenge_id, user_id, team_id, value)
VALUES (@submission_id, @challenge_id, @user_id, @team_id, @value)
ON CONFLICT DO NOTHING
RETURNING id;

-- name: RecalcChallengeValue :exec
-- ONE statement. A read-modify-write would let concurrent solvers each read a stale count.
UPDATE challenges c
   SET value = GREATEST(c.minimum, CEIL(
         ((c.minimum - c.initial)::float8 / POWER(NULLIF(c.decay, 0), 2))
         * POWER(GREATEST(cnt.n - 1, 0), 2) + c.initial))
  FROM (
      SELECT COUNT(*) AS n
        FROM solves s
        JOIN teams t ON t.id = s.team_id        -- the account table per mode (§13)
       WHERE s.challenge_id = @challenge_id
         AND t.hidden = false AND t.banned = false
  ) AS cnt
 WHERE c.id = @challenge_id;
```

The four writes and the two reads are unambiguously one transaction, and the atomicity is visible in the code.

#### ent / GORM / Bun

All three can express the transaction shape (`client.Tx` / `db.Transaction` / `db.RunInTx`). The differences
are narrow but real:

- **ent** — `ON CONFLICT DO NOTHING … RETURNING` is awkward through the builder, and the recalc
  `UPDATE … FROM` is raw. The transaction ends up half-ent, half-raw.
- **GORM** — `clause.OnConflict{DoNothing: true}` works, but hooks and implicit behaviors run inside the hot
  path, and `RowsAffected`-based conflict detection is easy to get subtly wrong. In a path where "did I
  actually insert?" decides whether a solve counts, implicit is the wrong default.
- **Bun** — clean: `On("CONFLICT DO NOTHING")`, explicit, composes well. The closest to the sqlc version in
  readability.

**Verdict.** The hot path differentiates only on how *explicit* the conflict-and-commit semantics are.
sqlc and Bun make them obvious; GORM hides them.

---

### Query 3 — Dynamic-score recalculation

Shown as `RecalcChallengeValue` above. The point for the comparison is narrow: it must be **one statement**
(`UPDATE … FROM (SELECT COUNT(*) …)`), never a read-modify-write, or concurrent solvers each read a stale
count and the last writer wins. sqlc, Bun, and GORM express it as raw-ish SQL; **ent has no `UPDATE … FROM`
idiom at all.** Note also that the formula needs `float8` division, a `CEIL`, a `GREATEST(minimum, …)` clamp,
and a guard on `decay = 0` (`NULLIF`) — which is rejected at write time rather than coerced (§16).

---

### Query 4 — Attribution and flag-sharing detection

*Given a submitted flag, who was it **issued** to, and who **submitted** it?* The shape depends entirely on
the flag mechanism (§11).

#### The instance pool, with attribution stamped at submit time

Because `submissions.attributed_account_id` is written inside the submit transaction, sharing detection is a
predicate on **one table**:

```sql
-- name: FindFlagSharing :many
-- Correct submissions whose flag was issued to a DIFFERENT account than the one that submitted it.
SELECT sub.id, sub.date, sub.user_id, sub.team_id, sub.challenge_id,
       sub.attributed_account_id
  FROM submissions sub
 WHERE sub.type = 'correct'
   AND sub.attributed_account_id IS NOT NULL
   AND sub.attributed_account_id <> sub.team_id      -- the account column per mode (§13)
 ORDER BY sub.date DESC
 LIMIT @lim;
```

No join to the issue table at all — so attribution survives a pool being regenerated, an instance being
deleted, or a flag being rotated. **A join-based audit trail is only as durable as the rows it joins to.**

The join *is* needed once, on the submit path, to work out who the flag belonged to — one indexed lookup:

```sql
-- name: AttributeUniqueFlag :one
SELECT ci.id AS instance_id, fi.account_id
  FROM challenge_instances ci
  LEFT JOIN flag_issues fi ON fi.instance_id = ci.id
 WHERE ci.challenge_id = @challenge_id
   AND ci.value_hash   = @value_hash;              -- sha256(provided); never the plaintext
```

Exact-match only, by construction, and indexed on `(challenge_id, value_hash)`.

#### The HMAC alternative, for comparison

```go
// No pool table; attribution is a pure function.
// flag = flag{<prefix>_<base32(accountID)>_<hex(HMAC(key, challengeID‖accountID)[:8])>}
func Attribute(serverKey []byte, challengeID int64, provided string) (accountID int64, ok bool) {
    id, tag, ok := parse(provided)
    if !ok { return 0, false }
    want := hmacTag(serverKey, challengeID, id)
    return id, subtle.ConstantTimeCompare(tag, want) == 1
}
```

O(1), zero storage, zero queries — and note that **the sharing-detection query above is unchanged**, because
it reads the stamped column either way. That is the point of stamping: the query model does not depend on
the issuance model, so `FlagIssuer` can be swapped without touching a single report.

**Verdict.** A straightforward indexed lookup; all four candidates handle it. ent's edge traversal comes
closest to being useful here — but it is a two-table join, which is not where an ORM earns its keep.

---

### Query 5 — Whole-instance import

**All four candidates are identical here, and none of them help.** The operation is: recreate the schema →
`SET session_replication_role = replica` → bulk-load every table in FK-dependency order with **IDs
preserved** → `setval` per table → restore FK enforcement.

```go
// Same in sqlc / ent / GORM / Bun — this is raw SQL and a COPY stream, by nature.
tx, err := pool.Begin(ctx)
if err != nil { return err }
defer tx.Rollback(ctx) //nolint:errcheck
if _, err := tx.Exec(ctx, `SET session_replication_role = replica`); err != nil { return err }

for _, table := range tablesInDependencyOrder {   // teams, users, challenges, flags, …
    src := archive.Reader(table)
    // ONE transaction for the whole restore: a failure anywhere leaves the instance untouched.
    if _, err := tx.CopyFrom(ctx, pgx.Identifier{table}, columnsOf(table), src); err != nil {
        return fmt.Errorf("restore %s: %w", table, err)
    }
    if _, err := tx.Exec(ctx, fmt.Sprintf(
        `SELECT setval(pg_get_serial_sequence('%s','id'), COALESCE(MAX(id),0)+1, false) FROM %q`,
        table, table)); err != nil { return err }
}
if _, err := tx.Exec(ctx, `SET session_replication_role = DEFAULT`); err != nil { return err }
return tx.Commit(ctx)
```

What this argues for is not a data layer but §7 (this is a job, with a durable status row and a progress
connection outside the transaction) and §17 (the dependency order and the ID-preservation contract are the
adapter's problem, at the edge, and nowhere else).

---

### Appendix A — what the code actually showed

| | sqlc | ent | GORM | Bun |
|---|---|---|---|---|
| Standings | native SQL, typed, build-verified | **degrades to raw** | **degrades to raw**, runtime mapping | native builder, runtime-checked |
| Submit hot path | explicit; atomicity is visible | half-ent, half-raw | works, but implicit | explicit, clean |
| Decay recalc | one statement | **no `UPDATE … FROM` idiom** | raw | raw-ish |
| Attribution | fine | fine (its best showing) | fine | fine |
| Whole-instance import | raw (unavoidable) | raw | raw | raw |

**The pattern: three of the five hardest queries are raw SQL in every candidate.** The question is therefore
not "which ORM writes my queries" — none of them do — but **"what do I get for the queries the ORM can't
write?"** sqlc's answer is build-time verification and generated types for the SQL you were going to write
anyway. That is the trade, and it is why §2 went the way it did.
