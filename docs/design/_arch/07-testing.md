# _arch/07 — Verification strategy

## What is actually hard to get right

A scoring engine is not hard because the formulas are hard. It is hard because:

- **The interesting failures are races.** Every rule the engine enforces — one solve per account, one
  unlock per hint, one first blood, one instance per account, a cap that holds — is a rule about what
  two concurrent requests may not both do. A single-threaded test suite cannot see any of them.
- **Correctness is a property of the schema, not of the handler.** The constraints are the enforcement
  mechanism, so a test that mocks the database is testing the wrong object. It cannot fail the way a
  database fails.
- **The scoring model must be sound under any interleaving**, not merely under the interleavings we
  thought to write down.

So the suite is built in four layers, and the second one is built first.

| Layer | Proves | Oracle |
|---|---|---|
| **1. Invariants (property-based)** | The scoring model is sound under *any* event sequence | Mathematics — pure functions over `internal/domain` |
| **2. Concurrency suite** | Every rule that is a rule about concurrency actually holds | The database's own constraints, under real contention |
| **3. Importer goldens** | We can read real export archives, and we are loud about what is lossy | Real archives |
| **4. Parity tests** | The behaviors we deliberately kept, we kept | The documented decision — because we decided it |

---

## Layer 1 — Invariants (property-based)

`internal/domain` imports nothing but the standard library. That is enforced in CI
(`task check-boundaries` walks the transitive import set from `go list -deps`, so it cannot be
silenced by a `//nolint` comment), and it is exactly what makes this layer fast and total: scoring,
decay, the tiebreak and the policy decision are pure functions, so they can be hammered with
generated event sequences rather than fixtures.

```
∀ event sequences:
  score(account)      ≡ SUM(solves.value) + SUM(awards.value)
  standings           are a total order, deterministic under any interleaving
  standings(as_of=T)  ≡ standings computed from events with date < T
  decay(n)            is monotonically non-increasing in n, clamped to [minimum, initial]
  decay(0)            is well-defined
  a solve             is never counted twice
  zero-valued solves  do not place an account on the board
```

The `as_of` property is only *expressible* because solve values are stamped at solve time. If the
board recomputed each solve from the challenge's current value, there would be no true historical
board to compare against — the property would have no right-hand side. It is what pins scoreboard
time-travel (see the [roadmap](../ROADMAP.md)).

Run with `task test`.

---

## Layer 2 — The concurrency suite

**Build this first.** In a scoring engine the interesting failures are races, so the race suite is
the oracle you want before you have anything to run it against. It is also the claim the project is
sold on, and the difference between asserting the claim and demonstrating it.

```go
// Real Postgres. Not a mock — the invariants live in the CONSTRAINTS,
// and a mock cannot fail the way the database can.
// task test-concurrency   → go test -race -count=1 -p 1 -tags=integration ./test/concurrency/...
```

`-count=1` is not decoration: a race that reproduces one run in twenty must actually run every time,
not replay a cached pass.

| Case | What it asserts | Test |
|---|---|---|
| **Hint-unlock double-charge → negative score** | N goroutines unlock the same hint ⇒ exactly one unlock, exactly one award, and **the score never goes below zero**. The affordability check reads a balance; without a constraint, N concurrent unlocks all see the pre-spend balance. | `TestHintUnlock_NoDoubleCharge`, `TestHintUnlock_ConcurrentDistinctHints_ScoreNeverNegative`, `TestHintUnlock_AlreadyUnlocked_LeavesNoOrphanCharge` |
| **Duplicate solve** | 100 goroutines, one account, one correct flag ⇒ `COUNT(solves) = 1`. | `TestSubmit_DuplicateSolve` |
| **First-blood double-award** | N distinct accounts submit simultaneously ⇒ exactly **one** first-blood award. And a hidden account neither claims nor burns it. | `TestSubmit_FirstBlood_ExactlyOnce`, `TestSubmit_HiddenAccount_CannotClaimOrBurnFirstBlood` |
| **Dynamic-value decay under concurrency** | N concurrent solvers must not each read a stale solve count and let the last writer win: the challenge value must end at `f(N)`, and each solve must be stamped with the value that was current under the lock. | `TestSubmit_DecayIsExactUnderConcurrency`, `TestSubmit_SolveValueIsSnapshotUnderTheLock` |
| **Unique-flag double-issue** | N concurrent first-views ⇒ `UNIQUE(instance_id)` holds; no two accounts are issued the same instance. Repeat views by one account are idempotent, exhaustion fails loudly, and someone else's valid flag is accepted and attributed silently. | `TestFlagPool_NoDoubleIssue`, `TestFlagPool_SameAccount_ConcurrentFirstViews_AreIdempotent`, `TestFlagPool_Exhaustion_FailsLoudly`, `TestFlagPool_SharedFlag_IsAcceptedAndAttributedSilently` |
| **File-location double-insert** | Concurrent uploads of the same content ⇒ one row, not a duplicate-key 500. | `TestFileLocation_NoDoubleInsert` |
| **Registration-cap bypass** | Count-then-insert is a race by construction. N concurrent registrations at the cap ⇒ the cap holds; N concurrent joins for one team slot ⇒ exactly one wins. | `TestRegistrationCap_Holds`, `TestTeamSlotCap_ExactlyOneWins` |
| **Max-attempts bypass** | N concurrent submissions against a challenge with an attempt limit ⇒ the count is exact; the limit cannot be overrun by racing it. | `TestMaxAttemptsExactUnderConcurrency` |
| **Session-tracking upsert** | A racing tracking-row insert must not take the request's transaction down with it and log the user out. Concurrent requests ⇒ the session survives. | `TestTrackingUpsert_SessionSurvives` |
| **Rate-limit counter** | `INSERT … ON CONFLICT DO UPDATE SET n = n+1` is atomic *in Postgres*, which is the whole reason there is no separate cache tier. N concurrent submits ⇒ the counter is exact. | `TestRateLimitCounter_IsExactOnPostgres` |
| **The lazy lock holds** | An all-incorrect workload takes **zero** challenge locks, and wrong answers do not queue behind solvers. | `TestLazyLock_WrongAnswerNeverTakesTheChallengeLock`, `TestLazyLock_MixedWorkload_WrongAnswersDoNotQueueBehindSolvers` |

The lazy-lock case is the sleeper. Taking the challenge lock only on the correct-flag path is a
*performance* property with no *correctness* symptom: nothing breaks if someone "tidies" the lock up
to the top of the transaction, it just quietly serializes every wrong guess — which is 99% of the
traffic. Only a test can defend it. (See [ADR-0006](../../adr/0006-lazy-lock-on-the-hot-path.md).)

### One case is deferred, deliberately

Challenge **ratings** are not in v1: there is no `ratings` table and no migration creates one. The
race that would live there is the constrained-upsert-with-uncaught-conflict shape — the same defect
as the file-location double-insert and the session-tracking upsert, on a table we do not have.
Writing the test would mean inventing the table, i.e. adding v1 scope to serve a test.

`TestRatings_Deferred` fails if a `ratings` table ever appears without a concurrency test alongside
it, so the gap cannot close silently.

### What building this layer first actually bought

Running these against real Postgres falsified three things the design asserted before any of them
reached production. Each is now pinned by the test that caught it.

| What the design assumed | What Postgres actually does |
|---|---|
| The hint-unlock contract takes `FOR UPDATE` on the account row. | `FOR UPDATE` conflicts with the `FOR KEY SHARE` that the `submissions` / `solves` / `awards` FK inserts take on that same row — so an account buying a hint **blocks its own wrong answers**. That is the lazy lock's premise defeated one table over. Now `FOR NO KEY UPDATE`, which still self-conflicts (spends serialize) but lets the FK inserts through. |
| `FOR UPDATE … SKIP LOCKED` makes instance assignment safe, and a `UNIQUE(instance_id)` conflict would be swallowed by `ON CONFLICT DO NOTHING`. | **Both halves are false.** SKIP LOCKED locks the `challenge_instances` row, but the "is it issued?" predicate reads `flag_issues` — a *different* table — so a transaction whose statement snapshot predates a concurrent commit still sees the instance as free, and by then the other transaction has released the row lock, so nothing is skipped. The loser raises `UNIQUE(instance_id)` (23505), which the `ON CONFLICT` clause does **not** catch: its target is the primary key. Players get a 500 at CTF start. Now serialized by `pg_advisory_xact_lock` per challenge, taken as its **own statement** before the pick — under READ COMMITTED a lock acquired *inside* the pick would still read the pre-wait snapshot. |
| (implicit) The transactions work at any isolation level. | They require **READ COMMITTED**. Under REPEATABLE READ or SERIALIZABLE a row lock on a concurrently-updated row raises `40001` instead of waiting, so every contended submit would reject a correct flag. Now pinned explicitly in `pgx.TxOptions` rather than inherited from the server default. |

---

## Layer 3 — Importer goldens

flagfish imports CTFd export archives, one-way. That is the only place a foreign format is
interpreted, and the only place it is tested.

- Real `export.zip` archives → import → assert the resulting database against a golden state, plus a
  golden import report.
- Assert the **lossy** parts are lossy *loudly*. A per-solve value cannot be reconstructed for past
  solves of a decayed challenge: the information was never recorded in the source archive at all.
  The importer logs a warning per affected challenge and records it in the report; it must not
  fabricate a number that looks authoritative.
- Assert imports are **idempotent**, and that a conflicting archive is **atomic**: inject a failure
  mid-restore and assert the database is untouched. A restore is one transaction, not a row-by-row
  commit loop.
- Assert legacy password hashes survive, still verify, and are upgraded on first login.
- Assert malformed archives and zip-slip paths are rejected rather than tolerated.

### Why there is no differential oracle

The tempting move — run the source platform and flagfish side by side, replay identical traffic, diff
the responses — does not work here, and it is worth saying why rather than leaving it as a gap.

flagfish **deliberately scores differently**. Solve values are stamped at solve time rather than
recomputed from the challenge's current value, so on any decayed challenge the two systems produce
different past scores *by design*. A differential oracle would report a diff on every decayed
challenge in every event and be useless — you would spend the whole exercise triaging intended
differences. Differential testing is a strategy for building a clone, and this is not a clone. The
compatibility surface is the archive format, and it is tested where it lives: in the importer.

---

## Layer 4 — Parity tests (only where a behavior was deliberately kept)

Table-driven, because these are matrices, not scenarios. These pin the decisions that look like bugs
and are not — the ones someone will otherwise "fix" in year two.

| Matrix | Shape |
|---|---|
| **Visibility** | entity shapes × roles (anon / unverified / verified / banned / hidden / admin) × config (public / private / hidden) |
| **Freeze** | `date < freeze` (strict `<`); admins see frozen data on the *public* board and live data on the *admin* board. Call-site-driven, not role-driven. |
| **Tiebreak** | earliest *last* non-zero scoring event; `MAX(id)` across two id spaces; accounts with only zero-valued solves are absent from the board entirely |
| **Deliberately-odd behaviors** | a pause has no admin exemption and does not block hint purchases; unverified logged-in users are *more* restricted than anonymous ones; removing a member hard-deletes their submissions while deleting a team detaches them — asymmetric on purpose |

Every entry in that last row needs a test whose name says *"this looks like a bug and is not."* The
test is the comment that cannot rot.

---

## CI

```
task check-boundaries    # internal/domain imports nothing but stdlib. Not defeatable by //nolint.
task test                # layer 1 — pure, fast, -race -shuffle=on
task test-concurrency    # layer 2 — real Postgres, -count=1, -p 1
task test-integration    # HTTP-level behavior against a real database
task test-security       # the authorization suite: each test fails if its guard is removed
task sqlc-diff           # schema/query drift ⇒ build fails
git diff --exit-code openapi.yaml   # API contract drift ⇒ build fails
```

Those last two are the point of generating the query layer and the API contract rather than
hand-writing them: **drift becomes a build failure, not a bug report.**
