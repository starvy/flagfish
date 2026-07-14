# ADR-0006: The challenge lock is taken lazily, on the correct-flag path only

- **Status:** Accepted
- **Date:** 2026-07-14

## Context

The submit transaction must be one atomic unit: **timing-safe flag check + solve insert + audit
write + first-blood detection**.

First blood is the hard part, because it is a **race**, not a lookup. Under `READ COMMITTED`, two
concurrent first solvers both see zero prior solves and both believe they got first blood. The
same race destroys the decay recalculation: each concurrent solver reads a stale solve count, and
the last writer wins.

## Options considered

- **(a) Serialize per challenge** — `SELECT … FROM challenges WHERE id = $1 FOR UPDATE`. Everything
  downstream (solve count, first blood, decay) is then race-free by construction, in one
  transaction.
- **(b) Optimistic + derived first blood** — never store a first-blood flag; define it as
  `MIN(solves.id)` for the challenge. A derived fact can never be wrong or double-announced.
- **(c) `SERIALIZABLE` isolation** with retry.

## Decision

**(a) — but the lock is acquired *after* the flag comparison, not before it.**

```
BEGIN
  read challenge + flags                         -- no lock
  match := FlagIssuer.Check(challenge, provided) -- static: constant-time compare
                                                 -- unique: sha256 → indexed instance lookup
  if INCORRECT:
      INSERT submission(type='incorrect'); COMMIT        -- ← never touches the lock

  SELECT … FROM challenges WHERE id = $1 FOR UPDATE      -- ← lock only now
  INSERT submission(type='correct', attributed_account_id=…)
  INSERT solve(value=<current>) ON CONFLICT DO NOTHING   -- ← the unique constraint is the arbiter
      → no row ⇒ already_solved; COMMIT
  count prior solves (excluding hidden/banned)           -- exact, under the lock
  if first && challenge.first_blood <> 'none':
      if 'bonus': INSERT award(...)
      river.Insert(tx, AnnounceFirstBlood{...})          -- ← outbox, same transaction
  UPDATE challenges.value  (ONE statement, no read-modify-write)
COMMIT
```

**The laziness is the point, and it matters more than the choice between (a), (b) and (c).**

The overwhelming majority of submissions are **wrong answers** — that is what a CTF *is*. A
top-of-transaction lock serializes **every wrong guess** on the hottest challenge through one row.
Under a brute-force attempt, or just a popular challenge at peak, you have built a queue where you
meant to build a guard. Wrong answers need no serialization: they insert an independent row and
race with nothing. Lock the correct path only, and contention scales with **solves** — a few per
second at absolute peak — instead of with **submissions**.

**Why not (b),** which is genuinely the more elegant idea: with a derived first blood, first blood
is no longer decided *inside* the submitting transaction, so you cannot return *"🩸 first blood!"* in
the HTTP response without a second read — and that second read reintroduces exactly the race you
removed. The requirement was "check + solve + audit + first-blood, **one atomic unit**." (a) gives
literally that, and the code reads like the sentence.

**Why not (c):** SERIALIZABLE makes every caller handle serialization failures and retry, forever,
on a hot path, to solve a problem one row lock solves exactly. Reserve it for many interacting
invariants across many rows; here there is one invariant scoped to one challenge.

## Consequences

### What this buys

- First blood is exact and can never be double-awarded — under the lock, the prior-solve count is a
  fact, not a guess.
- **The decay recalculation becomes exact for free.** The stale-count, last-writer-wins race is
  closed by a lock we were taking anyway.
- The transactional `river.Insert` sits inside the same transaction, so an announcement can never
  outlive a rolled-back solve.
- Wrong answers — ~99% of traffic — take **zero** locks.

### ⚠️ What we gave up

**Solves on a single challenge serialize.** This is the only real objection, so do the arithmetic:
peak on the single most popular challenge in a large CTF is a handful of solves per second; the
critical section is ~4 indexed statements against cached rows — low single-digit milliseconds. Two
to three orders of magnitude of headroom. It is free *at this scale*, and it would not be at
Facebook's.

**The lazy ordering is a performance property with no correctness symptom.** Nothing breaks if
someone "tidies" the lock to the top of the transaction. It just quietly serializes every wrong
guess in the event, and you find out from a p99 graph during a live CTF. **Only a test can defend
it**, which is why the concurrency suite (`test/concurrency`) asserts that an all-incorrect workload
takes **zero** challenge locks, and why the `submit` trace span reports lock-wait time as its canary.

**Two traps that a well-meaning refactor will hit:**

- **Do not use a data-modifying CTE for the solve insert.** A CTE would insert the `submissions` row
  **even when the `solves` insert conflicts**, orphaning a `type='correct'` submission with no
  solve. Two statements, one transaction.
- `ON CONFLICT DO NOTHING … RETURNING id` returning **zero rows** *is* the `already_solved` signal.
  Do not `SELECT` first — that is the check-then-insert race.

### What would change this decision

Genuinely enormous scale — tens of thousands of concurrent solvers on one challenge — which is not
this product. And the migration to (b) is cheap if it ever comes: stop locking, define first blood
as `MIN(solves.id)`, announce from a job. **The schema does not change.**
