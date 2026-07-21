# Why flagfish is built the way it is

flagfish is a capture-the-flag platform written in Go. It ships as one static binary, talks to one
Postgres (plus an optional S3-compatible bucket for challenge files), and is built around a single
idea:

> **A scoring system's worst failure is not a crash. It is a wrong number that nobody notices.**

A crash pages someone. A silently incorrect score does not — it renders cleanly, sits on the
scoreboard, and decides who won. Every decision below follows from taking that seriously: put the
invariant in the database where it cannot be forgotten, stamp the fact at the moment it happens
instead of recomputing it later, and make every failure loud. What follows is the long form of the
five properties the README summarizes, with the real constraints and the honest trade-offs. Each
stack choice has an [architecture decision record](adr/) that argues its cost; each subsystem has a
[design chapter](design/) that specifies it. This document is the map between them.

Nothing here is sold. Where something is designed but not built, this document says so in the section
that describes it.

---

## Anti-cheat

**Deduce, don't guess.**

Most flag-sharing detection is a heuristic: correlate IPs, cluster submission times, eyeball the
graph, and make a judgment call you cannot defend to the accused. flagfish is built so that the two
questions that matter most are answered by *deduction* — a fact the database can prove — and only the
genuinely statistical question is labelled statistical.

Set `flag_mode = "unique"` on a challenge and each account is issued its own flag from an
author-supplied pool, along with (optionally) its own artifact and its own rendered challenge body.
An entry in the pool is not a bare string; it is a bundle — a flag, an optional file, and arbitrary
per-account template variables — modelled as a `challenge_instances` row, and `flag_issues` records
which account was issued which instance. Two constraints carry the entire feature
([`internal/db/migrations/00004_unique_flags.sql`](../internal/db/migrations/00004_unique_flags.sql)):

```sql
PRIMARY KEY (challenge_id, account_id)   -- an account cannot be issued two instances
UNIQUE      (instance_id)                -- an instance cannot be issued to two accounts
```

The comment in the migration names the second one for what it is: *the constraint that makes the
anti-cheat property true.* Because an instance can be issued to exactly one account, the flag inside
it belongs to exactly one account — provably, by the database, not by a report someone runs.

The other half is stamping. Every correct submission writes `submissions.attributed_account_id` —
the account the matched flag was *issued to* — inside the submit transaction:

```sql
-- 00003_gameplay.sql
-- The account the matched flag was issued to. Stamped inside the submit transaction, never
-- joined at query time — so attribution survives instance rotation or deletion and sharing
-- detection is a predicate on this table alone.
attributed_account_id bigint
```

It is stamped, not joined, on purpose: the attribution has to survive the instance being rotated or
deleted, and sharing detection has to be a predicate over one table rather than a join that can go
stale. From those two facts the deductions fall out:

| Question | How it is answered |
|---|---|
| *Did team B submit a flag issued to team A?* | `attributed_account_id <> account_id` on a correct submission. **Provable.** |
| *Did team B solve a challenge they never opened?* | A correct submission on a unique-flag challenge with **no `flag_issues` row** for that account. They could not have obtained that flag legitimately. **Provable.** |
| *Are these two accounts the same person?* | Shared `submissions.ip`, no team relationship. **Statistical — and labelled as such.** |

The first two are deductions. The third is a hint, and the UI presents it as one. That distinction is
the whole point: an accusation you can defend in front of the accused, kept separate from a
correlation you cannot.

Detection is **silent**. A shared flag is accepted as an ordinary solve and the player sees an
ordinary `Correct!`. Rejecting it would tell the cheater the platform tracks provenance, and they
would adapt — submit from the right account, and you have taught them how to get away with it.
Evidence accumulates in the ledger; a human decides. The concurrency suite pins the silence directly:
`TestFlagPool_SharedFlag_IsAcceptedAndAttributedSilently` asserts that a submitted-elsewhere flag is
accepted *and* attributed to its true owner in one transaction.

Two operational properties make the feature safe to rely on rather than merely present:

- **Issuance is O(1) and timing-flat.** The submit path for a unique flag hashes the provided string
  and does one indexed probe against `challenge_instances.value_hash` — `sha256(provided)` → indexed
  lookup, not a scan across every account's flag, and no per-byte comparison to leak a timing channel.
  The plaintext flag is never stored; only its SHA-256 is.
- **Exhaustion is a hard failure, by design.** If the pool runs dry mid-event, issuance fails loudly
  (a 503) rather than silently falling back to a shared flag — because a silent fallback would quietly
  destroy the very attribution property the feature exists to provide. The pre-event pool-utilisation
  gauge (issued / total per challenge) is the number an organizer must get right *before* the doors
  open, and it is exported for exactly that reason. `TestFlagPool_Exhaustion_FailsLoudly` pins it.

**The trade-off, stated plainly.** Unique flags cost you a pre-generated pool that has to be large
enough for the field, and pool exhaustion is a real failure you have to provision against rather than
an edge case that degrades gracefully. That is the deliberate choice: loud and provisioned beats
quiet and wrong.

→ Deeper: [Unique flags in TARGET-FEATURES](design/TARGET-FEATURES.md#unique-flags) ·
[the schema chapter](design/_arch/01-schema.md) · [the submit hot path](design/_arch/05-hotpath.md)

---

## Time travel

**The scoreboard is a pure function of time.**

Dynamic scoring decays a challenge's point value as more teams solve it: the hundredth solver earns
less than the first. That is intended and not in question. The question — [and it is the single most
consequential decision in the project](adr/0005-per-solve-score-snapshot.md) — is what happens to the
*first* solver's score when the hundredth solve lands.

There are only two coherent answers, and they differ in where the score lives. If the score is
**derived** — standings `SUM` the live `challenges.value` by joining to the current row — then
decaying the challenge retroactively re-prices every past solve, and there is no record anywhere of
what a solve was worth when it happened, because the only copy of that number was overwritten. If the
score is **stamped** — `solves.value` written at insert and never touched again — then the board is a
ledger, and decay changes only what the *next* solve is worth.

flagfish stamps. `solves.value` is written inside the submit transaction, under the challenge lock,
and never rewritten. Standings `SUM` the stamped values
([`internal/db/migrations/00003_gameplay.sql`](../internal/db/migrations/00003_gameplay.sql)):

```sql
-- The per-solve snapshot, the decision this schema turns on. Summing the live challenges.value
-- for standings would let decay retroactively revalue every past solve, so the first solver
-- keeps no advantage. Stamped under the challenge lock; makes the scoreboard append-only.
value int NOT NULL,
...
-- Append-only. `value` and `date` are set once, at insert. Never rewritten.
```

The argument in one line: **an audit trail on top of retroactive revaluation is a contradiction.** An
audit record that says "awarded 347 points" is worthless if 347 is recomputed on read. And there is a
fairness argument players actually feel — under stamping, the first solver *keeps* the higher value
they earned for being first; under derivation the reward for being early evaporates as the field
catches up.

Three features fall out of that one column. They were not designed; they *fell out*:

- **Time travel is exact.** `GET /api/v1/scoreboard?as_of=<ts>` is a `WHERE date < $1` over immutable
  facts — the board exactly as it stood at that instant, not a reconstruction and not an
  approximation. That buys the animated scoreboard replay, the per-team "your CTF in review" card, and
  a scoring simulator (*"what would the board look like if decay were 30?"*) that is a recomputation
  over stamped facts rather than a feature that has to be built.
- **The audit trail means something.** Every award is a number that was true when it was written and
  is still true now.
- **The freeze is honest.** A scoreboard freeze is a strict `date < freeze_at` predicate, and whether
  a caller is exempt is a property of the *endpoint*, not of a role (more below) — so the public board
  is frozen for everyone, admins included, and the frozen board is a query, not a snapshot job that
  can drift from the truth.

The stamping shows up throughout the scoring queries: they never join `challenges` at all. Attribution
is stamped the same way — `solves.team_id` is written at solve time, so a player who changes teams
does not drag their past solves onto the new roster.

**What this costs, stated in full.** Standings can no longer be recomputed from first principles to
catch a bug. Under derivation a scoring bug is self-healing: fix the formula and the next read
corrects the board. Under stamping, the stamped values *are* the truth, so a bug that stamps a wrong
value writes a wrong fact into the ledger, and fixing it is a data migration with a human decision in
it. That is the strongest argument against this design and it is a real cost. It also means `solves`
and `awards` are append-only in practice, not merely in intent — time travel is correct only if
nothing mutates them after insert, and **there is no constraint that can catch a future "correction"
that rewrites a solve's value.** That property can only be defended by a test, forever. And a
decay-curve misconfiguration cannot be fixed by editing the challenge after the fact; the organizer has
to decide explicitly what to do about the scores already in the ledger. More honest, more painful,
chosen deliberately.

One consequence for imports: a source platform that used derived scoring never recorded what a past
solve was worth, so for a decayed challenge *that information does not exist upstream*. flagfish
imports the challenge's current value by default (so the imported board matches the source on day one)
and logs the lossiness per challenge, loudly. **flagfish does not claim scoring compatibility with any
other platform, and never will.**

→ Deeper: [ADR-0005 — the per-solve snapshot](adr/0005-per-solve-score-snapshot.md) ·
[the policy layer's freeze rules](design/_arch/02-policy.md)

---

## Concurrency

**Correct, and tested.**

A CTF is a system where hundreds of clients hammer one endpoint in the same second. Almost every bug a
scoring platform has under load is the same bug: a `SELECT`-then-`INSERT` that two requests interleave.
flagfish's rule is that if the database can express an invariant, the database enforces it — the fix is
never a mutex in a Go process, because a mutex fixes it on one process while a constraint fixes it on
all of them, forever, including from `psql` at 3am.

The proof that it works is `test/concurrency`: **24 test functions** that spawn N goroutines at each
race and assert on the rows that come out, against **real Postgres, never a mock** — because the
invariants live in the constraints, and a mock cannot fail the way a database can. A selection, each a
real function in the suite:

| The race | What the suite asserts | Test |
|---|---|---|
| Duplicate solve | 100 goroutines, same flag ⇒ exactly one `solves` row | `TestSubmit_DuplicateSolve` |
| First blood | N goroutines across N accounts ⇒ exactly **one** first blood | `TestSubmit_FirstBlood_ExactlyOnce` |
| Decay recalculation | N concurrent solves ⇒ `value = f(N)`, not `f(last writer)` | `TestSubmit_DecayIsExactUnderConcurrency` |
| Snapshot under the lock | the stamped `solves.value` is the value at solve time | `TestSubmit_SolveValueIsSnapshotUnderTheLock` |
| Flag-pool issuance | N concurrent first-views ⇒ `UNIQUE(instance_id)` holds; no shared flag | `TestFlagPool_NoDoubleIssue` |
| Pool rotation | rotation under concurrent issuance ⇒ new issues use the newest generation, prior issues stay pinned | `TestFlagPool_Rotation_NewIssuesUseNewestGeneration_PriorIssuesPinned` |
| Idempotent first-view | one account, concurrent first-views ⇒ one issue | `TestFlagPool_SameAccount_ConcurrentFirstViews_AreIdempotent` |
| File upload | concurrent uploads of one content address ⇒ one `files` row | `TestFileLocation_NoDoubleInsert` |
| Registration cap | N concurrent registrations at the cap ⇒ the cap holds | `TestRegistrationCap_Holds` |
| Team-slot cap | N racing for the last slot ⇒ exactly one wins | `TestTeamSlotCap_ExactlyOneWins` |
| Rate-limit counter | N concurrent submits ⇒ the counter is exact, on Postgres, no Redis | `TestRateLimitCounter_IsExactOnPostgres` |
| Hint affordability | N goroutines unlock hints ⇒ one charge each, **score never goes negative** | `TestHintUnlock_ConcurrentDistinctHints_ScoreNeverNegative` |
| The lazy lock | an all-incorrect workload takes **zero** challenge locks | `TestLazyLock_WrongAnswerNeverTakesTheChallengeLock` |

The arbiter in each case is a database primitive, not application code. Duplicate solves are decided by
a unique constraint and `INSERT … ON CONFLICT DO NOTHING RETURNING id` — zero rows returned *is* the
already-solved signal, so there is no check-then-insert to lose a race on. First blood is decided under
a per-challenge lock plus a partial unique index that refuses a second bonus even if the lock is ever
bypassed by an admin grant or a `psql` session
([`00003_gameplay.sql`](../internal/db/migrations/00003_gameplay.sql)):

```sql
CREATE UNIQUE INDEX awards_one_first_blood_per_challenge
    ON awards (challenge_id) WHERE type = 'first_blood';
```

Caps are a database trigger under an advisory lock, not a count-then-insert in the handler. Rate-limit
counters are a `COUNT(*)` over an index on a row the submit path already writes — atomic by
construction, which is why there is no Redis.

### The lazy lock — a performance invariant with no correctness symptom

The sleeper in that table is the last row. The submit transaction must be one atomic unit — timing-safe
flag check, solve insert, audit write, first-blood detection — and the clean way to make first blood
and decay race-free is to serialize per challenge with `SELECT … FROM challenges WHERE id = $1 FOR
UPDATE`. flagfish does exactly that, **but takes the lock *after* the flag comparison, not before it**
([ADR-0006](adr/0006-lazy-lock-on-the-hot-path.md)):

```
BEGIN
  read challenge + flags                 -- no lock
  match := Check(challenge, provided)    -- static: constant-time compare; unique: sha256 → indexed probe
  if INCORRECT:
      INSERT submission(type='incorrect'); COMMIT   -- ← never touches the lock
  SELECT … FROM challenges WHERE id = $1 FOR UPDATE  -- ← lock only now, on the correct path
  ...
```

Wrong answers are ~99% of a CTF's traffic. A top-of-transaction lock would serialize *every wrong
guess* on the hottest challenge through one row — a queue where you meant to build a guard, and under a
brute-force attempt or just a popular challenge at peak, that queue is the event's p99. Locking the
correct path only means contention scales with **solves** (a handful per second at absolute peak)
rather than with **submissions**.

Here is why this needs a test rather than a comment: **the lazy ordering is a performance property with
no correctness symptom.** Nothing breaks if someone "tidies" the lock to the top of the transaction —
every invariant still holds, every test that checks a row still passes. It just quietly serializes the
whole event, and you find out from a latency graph during a live CTF. So the suite asserts that an
all-incorrect workload takes zero challenge locks, and the mixed-workload test
(`TestLazyLock_MixedWorkload_WrongAnswersDoNotQueueBehindSolvers`) proves wrong answers do not queue
behind solvers. Two traps a well-meaning refactor will hit are called out in the ADR: do not fold the
solve insert into a data-modifying CTE (it would orphan a `type='correct'` submission when the solve
conflicts), and do not `SELECT` before the insert (that is the check-then-insert race, reintroduced).

**The trade-off:** solves on a single challenge serialize. Do the arithmetic — peak on the single most
popular challenge in a large CTF is a handful of solves per second, and the critical section is a few
indexed statements against cached rows, low single-digit milliseconds. Two to three orders of magnitude
of headroom. It is free at this scale and it would not be at Facebook's, which is the honest boundary of
the claim.

→ Deeper: [ADR-0006 — the lazy lock](adr/0006-lazy-lock-on-the-hot-path.md) ·
[the submit hot path](design/_arch/05-hotpath.md) · [the testing chapter](design/_arch/07-testing.md)

---

## One binary

**No Redis, no broker.**

```
./flagfish serve   +   Postgres 17   +   an S3 bucket (for challenge files)
```

That is the entire deployment. The React SPA and the migrations are `go:embed`ed into the binary;
nothing is read from disk at runtime. Object storage is the one moving part beyond Postgres, and it is
**optional**: leave the `FLAGFISH_S3_*` variables unset and the store is a disabled implementation
whose every method returns a "not configured" error rather than panicking. The binary boots, the event
runs, and only file attachments are unavailable — a missing bucket is not a failed deploy. Configure it
and challenge files are content-addressed by SHA-256, deduplicated, and gated on the same policy as the
challenge they belong to. `deploy/compose.yaml` ships MinIO for exactly this, so a self-hoster needs no
cloud account; any S3-compatible endpoint works.

**No Redis.** A CTF platform has four jobs a cache would conventionally do — SSE fan-out, sessions,
memoization, rate-limit counters — and each has an obvious Redis answer. The question
[ADR-0003](adr/0003-postgres-only-no-redis.md) settles is whether they are worth a *second stateful
system*, and the numbers say no: peak load for a large CTF is a few hundred submissions per minute,
roughly **five writes per second**. There is no scaling argument for Redis here, only a habit of
reaching for it — and a second stateful system is the single most expensive thing you can add to an ops
story, because it has to be deployed, monitored, secured, backed up, failed over, upgraded, and reasoned
about during every incident. So:

- **SSE rides `LISTEN/NOTIFY`.** The contract is broadcast-only with no per-user routing and no replay —
  a drop-in semantic match for NOTIFY, not a compromise. The 8 kB payload cap is a feature: publish the
  id, let clients re-read, and the stream becomes a cache-invalidation signal rather than a second,
  divergent copy of the data.
- **Sessions are a table** with an index on `expires_at`.
- **Config is an in-process `atomic.Pointer[Snapshot]`** swapped wholesale on a `NOTIFY` — no cache, no
  TTL, nothing to get wrong, and *no second source of truth that can disagree with the database and
  win.* For a scoring system that last property is the whole ballgame.
- **Rate-limit counters are an upsert** on a row the submit path already writes.

**No broker.** Background jobs run on [River](https://riverqueue.com), whose job table lives in the same
Postgres — so `INSERT solve` and `enqueue AnnounceFirstBlood` commit in **one transaction**
([ADR-0004](adr/0004-river-for-background-jobs.md)):

```go
tx, _ := pool.Begin(ctx)
// ... INSERT solve ...
river.Insert(tx, AnnounceFirstBlood{...})   // same transaction
tx.Commit(ctx)
```

Either the solve exists and the announcement is queued, or neither happened. You cannot announce a first
blood for a solve that rolled back — the dual-write bug class that a queue exists to prevent is
**structurally impossible**, not merely avoided. A broker living outside Postgres (Kafka, RabbitMQ,
Redis-backed asynq) would *reintroduce* that bug: commit to Postgres, publish to the broker, and a
failure between the two either announces something that did not happen or loses something that did. The
rule River satisfies and a broker does not: a broker earns its keep when the producer and consumer are
different services owned by different people; here they are the same binary, and a broker between two
functions in one process is a network hop with a YAML file.

Roles are a flag, not a topology — the same binary is API, worker, or both:

| Command | What it runs |
|---|---|
| `flagfish serve --with-worker` | API + background worker in one process. **The default.** |
| `flagfish serve` | API only. Insert-only River client — enqueues, never works. |
| `flagfish worker` | Workers only. Large events split these so an import cannot touch API p99. |
| `flagfish migrate` | Migrations behind a `pg_advisory_lock` — safe with N replicas. `--status` to inspect. |
| `flagfish admin create` | Create (or `--promote`) the first admin — the one thing the admin API cannot do for you. |
| `flagfish import <archive.zip>` | Import a CTFd export archive. One-way, one transaction, loud about what it drops. |
| `flagfish export [--safe\|--backup] <out.zip>` | Export. `--safe` is field-masked and shareable; `--backup` is full fidelity. |
| `flagfish restore <in.zip>` | **Replaces** this instance's data from a `--backup` archive. |
| `flagfish healthcheck` | `GET /healthz` on the local listener; exit non-zero unless 200. |
| `flagfish env` | Print the resolved environment (password redacted) and exit non-zero if anything is wrong. |

**The trade-off:** horizontal scale-out is bounded by one Postgres. There is no read-replica story for
the scoreboard yet and no cross-region topology; if a workload appears that genuinely needs many app
replicas across regions, ADR-0003 is the decision that has to move. The migration is not scary —
sessions and rate-limit counters sit behind interfaces, so Redis can be added *for those two things* if
it ever earns its keep. Postgres-first is not a bet against Redis; it is a decision not to pay for it
until it does.

→ Deeper: [ADR-0003 — Postgres only, no Redis](adr/0003-postgres-only-no-redis.md) ·
[ADR-0004 — River for background jobs](adr/0004-river-for-background-jobs.md)

---

## Challenges as code — designed, not yet built

> **`flagfishctl` is a stub.** Every subcommand returns `not implemented yet (stub)` and exits
> non-zero. This section describes the intended design so the shape is on the record and can be
> reviewed before the code exists — none of it works today.

Challenge authors are engineers, so the unit of authorship should be a directory in git, not a web
form. The planned format is TOML frontmatter plus a Markdown body, one directory per challenge —
self-contained, `git mv`-able, atomically creatable, and something an agent can be told to *produce*
rather than to surgically edit inside a shared file. TOML rather than YAML is a deliberate failure-mode
choice: YAML's significant indentation and its silent coercions (`no`, `on`, `y` become booleans — the
Norway problem) turn a bad file into a *silently wrong config*, whereas a bad TOML file is a *parse
error*. Loud beats silent, and it matters more here because these files will often be machine-generated.

The point of the planned `flagfishctl test` is to make **solvability a build gate**: run the author's
own solver against the built challenge and refuse to merge one it cannot solve.

```
$ flagfishctl test ./challenges/pwn/heap-overflow
  ✓ frontmatter parses, schema-valid
  ✓ referenced files exist
  ✓ template renders against every instance
  ✓ instances.jsonl: 250 bundles, 250 distinct flags
  ✓ solver produced a flag valid for this challenge     ← PROVABLY solvable
```

*"The challenge was unsolvable and nobody noticed until 40 teams had burned three hours on it"* happens
at every CTF, and it is a checkable property, not a matter of care. The whole loop is agent-friendly by
construction — a published JSON Schema for the frontmatter (the schema *is* the prompt) plus
`--json` output on `validate` and `test` — but flagfish ships **no model, no API key, no prompt, no
inference code**. The acceptance criterion is `flagfishctl test`: a generated challenge is provably
solvable or it is not merged. Two safety rails are baked into the design: `sync` uploads
`value_hash = sha256(flag)` and never the plaintext, and `sync` only creates and updates — it never
deletes a challenge absent from the repo, because deleting a challenge mid-event destroys its solves.

There is one guarantee here that is stronger than a policy: for a unique-flag challenge the body is a
`text/template` rendered against the caller's own instance, and the template's data context has **no
`Flag` field and no access to any other account's variables**. Cross-account leakage is not prevented,
it is *unrepresentable* — there is no syntax by which a template can reach another account's data, and
an author cannot leak the flag into their own description even by accident.

→ Deeper: [challenge-as-code](design/_arch/08-challenge-as-code.md)

---

## The ledger refuses to forget

The gameplay tables — `submissions`, `solves`, `awards`, `hint_unlocks`, and the `flag_issues`
anti-cheat record — are memory. A parent delete that cascaded into any of them would rewrite history
with no trace of what was lost, so every foreign key into these tables refuses the delete
([`00014_ledger_restrict_parent_deletes.sql`](../internal/db/migrations/00014_ledger_restrict_parent_deletes.sql),
[`00010_solves_restrict_challenge_delete.sql`](../internal/db/migrations/00010_solves_restrict_challenge_delete.sql)):

```sql
ADD CONSTRAINT solves_challenge_id_fkey FOREIGN KEY (challenge_id)
    REFERENCES challenges(id) ON DELETE RESTRICT;
```

`RESTRICT`, not `NO ACTION` — the difference matters: `NO ACTION` is checked at end of statement, so a
ledger row removed by another cascade path in the same statement passes silently, while `RESTRICT`
fires immediately. Deleting a challenge that has solves is not a cascade that quietly erases the
scoreboard's history; it is a `409` telling the admin the challenge has solves. Retiring a challenge or
an account is done by hiding or banning it, which is a state change the ledger keeps, not a delete it
cannot see. The one deliberate exception is surgical: `solves.submission_id` is `ON DELETE SET NULL`,
because deleting an *attempt* must not retract *points*.

The same instinct runs through the smaller invariants. A team's join secret used to be optional, and a
NULL hash meant the team admitted anyone — a rival could walk onto the roster and read the team's
solves. That is now impossible by construction
([`00022_team_join_secret_required.sql`](../internal/db/migrations/00022_team_join_secret_required.sql)):
the column is `NOT NULL` with a `DEFAULT` that mints a *locked* Argon2id hash — a well-formed hash over
bytes no password ever produced, so it does the full argon2 work and can never verify. A team nobody
remembered to give a secret admits *nobody*, never everybody. Fail-closed, in the schema, for rows that
already exist and for any future insert that forgets.

---

## The audit trail cannot be forgotten

Every admin mutation is captured — before and after, as JSONB — by a Postgres trigger, not by
application code ([`00006_audit.sql`](../internal/db/migrations/00006_audit.sql)). The split is
deliberate: HTTP middleware knows *who* (it stamps the actor and IP into a transaction-local setting)
but never sees the row's prior state; the trigger knows *what changed* but not that it was alice. One
generic trigger function joins the two. The reason it lives at the table rather than in a Go
interceptor is completeness by construction:

```sql
-- Triggers rather than a Go-side interceptor, for completeness by construction: the audit lives at
-- the table, not the handler. A new admin endpoint cannot forget to audit itself. Neither can a
-- migration, a console session, or a psql fix at 3am.
```

A new admin endpoint *cannot* forget to audit itself, because the audit is not something the endpoint
does. Two details are load-bearing. First, `password_hash` and `secret` are stripped in the trigger
(`to_jsonb(OLD) - 'password_hash' - 'secret'`), not at the read site — a redaction you have to remember
is a redaction you will forget. Second, the trigger is deliberately *not* on `submissions` or `solves`:
those are already-immutable gameplay facts on the hottest path in the product, and a trigger there
would double the write volume for zero information. `awards` *is* audited, because it is admin-mutable.

The honest boundary: the audit log captures everything, but it is explicitly **not** tamper-*proof* —
there is no hash chain and no revoked-DELETE grant. That is a chosen non-goal, named in the migration
rather than left as a surprise.

---

## The policy layer is one function

Authorization is one layer, four tiers, one input tuple. There are no authorization decorators
scattered across handlers, no per-endpoint filter helpers, no template-level "should I render this"
checks. Every allow/deny decision on every route is made by one pure function, `Decide(Policy) →
Outcome`, over one struct — no database handle, no row, no I/O
([the policy chapter](design/_arch/02-policy.md)). The tiers split exactly along *what input a check
needs*: the pure route gate (L1), per-row resource guards (L2), a closed set of SQL `WHERE` predicates
(L3), and field redaction at serialization (L4).

Purity is the point. Because `Decide` is a pure function of an enumerable input, its entire surface is a
finite cross product, and the golden table test walks every cell of it —
`RouteClass × {anon, unverified, verified, teamless, banned, team-banned, admin} × {4 visibility
settings} × {3 phases} × {paused} × {2 modes}` — in milliseconds, with no fixtures. The moment one
row-dependent check leaks into `Decide`, that exhaustiveness argument dies, which is why row-dependent
checks are pushed down to L2.

The subtle rules — the ones that look like bugs to a competent engineer reading the code cold — are
each a **named policy with a named test**, because a comment that says "this is deliberate" is
indistinguishable from a comment someone left on a bug, while a failing test named after the rule is
not. A few, to show the shape:

- **The freeze exemption is call-site-driven, not role-driven.** The public scoreboard is frozen *for
  everyone, including admins*; the live board lives at a separate admin URL. "Admins always see live
  data" is false on purpose — an organizer must be able to load the exact page a player is looking at,
  and a role-keyed exemption would make the state everyone is arguing about the one state the organizer
  cannot render.
- **Pause has no admin exemption.** While the CTF is paused, no one — not even an admin — can record a
  solve, because a pause is a statement that *nothing happened during this window*, and an admin "just
  testing" a flag would stamp a solve at a timestamp the players could not have reached. Admins can read
  everything; they cannot score.
- **A ban covers every credential.** The principal is resolved from the session *or* the API token
  *before* `Decide` runs, and `Decide` reads `pr.Banned` without knowing which credential produced it —
  so a ban that stops the browser but not the token is not reachable by accident; there is exactly one
  ban check and exactly one principal.

Each of those is a spot where a well-meaning "fix" would silently change the product, and each is
pinned. The list is longer in the design chapter, along with the deliberate 403-vs-404 asymmetry
between challenges and accounts.

→ Deeper: [the policy layer](design/_arch/02-policy.md)

---

## Dynamic scoring, exactly

The decay curve that sets a challenge's asking price is a pure function in `internal/domain/scoring`,
a package that imports nothing outside the standard library and never mutates its inputs. There are
three curves — static (never recomputed), linear, and logarithmic — and the whole model is four
columns: `function`, `initial`, `minimum`, `decay`. A malformed curve fails at the boundary
(`Curve.Validate` at create/update and import time), and an unknown curve name is a hard parse error,
never a silent fallback that would let a typo change a challenge's scoring model.

The value after N solves is computed over the integers, deliberately — and this is the kind of detail
that separates "correct-looking" from correct. For a logarithmic curve the drop is
`floor((initial − minimum) · n² / decay²)` with `n = max(solves − 1, 0)`, so the first solver always
pays full `initial`. The comment in [`decay.go`](../internal/domain/scoring/decay.go) explains why it is
not floating point:

```go
// In float64 the curve is wrong at the boundary: for (initial=3901, minimum=205,
// decay=142) the true value at n == decay is 205, but the float lands a hair
// high and Ceil lifts it to 206, so the challenge never reaches its floor.
// The same curve also runs in SQL (RecalcChallengeValue), and Go and Postgres
// need not round a float identically — displayed and stored price would
// silently diverge.
```

Two engines compute this value — Go for display, Postgres for the stored recompute — and if they
rounded a float differently, the price a player sees would silently disagree with the price the ledger
charges. Integer truncating division is the one operation both engines do identically. It is the same
theme as everything above: the failure being designed out is not a crash, it is a number that is quietly
wrong.

---

## Why sqlc, and not an ORM

The data layer is sqlc generating against pgx/v5, with plain-SQL goose migrations, and no query builder
([ADR-0002](adr/0002-sqlc-not-an-orm.md)). The migrations directory *is* the schema, and sqlc reads that
same directory as its type source, so there is one place where the truth lives. The reliability argument
outranks ergonomics, and it is best made by asking what happens when someone renames a column: sqlc
fails the build in CI; a runtime-mapped ORM yields a `Scan` that maps by name and **silently produces the
zero value** — scores read `0`, the page renders, and the scoreboard is wrong with nothing anywhere
saying so. For a system whose worst failure is a wrong number nobody notices, build-time SQL verification
is the highest-value property on offer, and it is the one thing every ORM here would have given up.

The honest costs are on the record in the ADR: sqlc cannot build dynamic queries, so the admin
list-filter is a closed field enum encoded directly in SQL (index-unfriendly, and it does not matter at
admin-screen scale); codegen is real friction; and sqlc has a bus factor of one — accepted, because it
is a build-time generator whose artifact is committed Go source, so an abandoned sqlc leaves a repository
that still compiles, still tests, and still ships.

---

## Status, honestly

flagfish is **pre-1.0, and no event has been run on it yet.** More is built than that implies — the
schema and migrations, the submit hot path, the concurrency suite, the pure-domain scoring and policy
code, auth, the full public API and embedded SPA, the admin console, the CTFd importer, and flagfish's
own export/restore are all real and tested. What is *not* built is `flagfishctl` (every subcommand is a
stub, as its own section says plainly), alternate challenge types, and OAuth. If you are running a CTF
that matters next month, run something mature. The whole reason this document exists is that a platform
which will tell you exactly what is and is not true about itself is the only kind worth trusting with a
scoreboard.

→ The full stack, with an ADR per choice: [`docs/adr/`](adr/). The long-form design — schema, policy,
API surface, importer, hot path, testing: [`docs/design/`](design/).
