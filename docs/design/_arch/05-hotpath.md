# _arch/05 — The submit hot path

The most important 200 lines in the product. Every other decision in the architecture either serves
this path or stays out of its way.

---

## The atomic unit

> *timing-safe check + solve insert + audit write + first-blood detection, one atomic unit*

Delivered literally, by one transaction and one row lock. The code below should read like that
sentence — if it stops doing so, the refactor is wrong.

---

## Ordering: the lock is taken **lazily**

The single most important structural decision on this path, and the easiest to get wrong:

> **The overwhelming majority of submissions are wrong answers. That is what a CTF *is*.**
> A lock at the top of the transaction serializes **every wrong guess** on the hottest challenge
> through one row. Under a brute-force attempt — or just a popular challenge at 3am — you have
> built a queue where you meant to build a guard.

So: **compare first, lock only on the correct path.** Contention then scales with **solves** (a few
per second at absolute peak) instead of with **submissions**.

```
BEGIN
  ── read challenge (no lock) ────────────────────────────────────────────────
  ── flag compare (no lock, no DB writes) ────────────────────────────────────
  │    flag_mode='static' + flags.type='static' → subtle.ConstantTimeCompare
  │    flag_mode='static' + flags.type='regex'  → regexp match  (not timing-safe; see below)
  │    flag_mode='unique'                       → sha256(provided) → indexed pool probe
  │
  ├─ INCORRECT ─→ INSERT submission(type='incorrect'); COMMIT     ← never touches the lock
  │
  └─ CORRECT ───→
       SELECT … FROM challenges WHERE id = $1 FOR NO KEY UPDATE   ← lock ONLY now (see note)
       INSERT submission(type='correct', attributed_account_id)   ← attribution stamp
       INSERT solve(value = ch.value) ON CONFLICT DO NOTHING      ← snapshot; the UNIQUE is arbiter
         └─ no row returned ⇒ already_solved; COMMIT
       count prior solves (excluding hidden/banned)               ← exact, under the lock
       if first && ch.first_blood <> 'none':
           if 'bonus': INSERT award(value = ch.first_blood_bonus)
           river.Insert(tx, AnnounceFirstBlood{…})                ← outbox: SAME tx
       UPDATE challenges.value = f(solve_count)                   ← single stmt, no read-modify-write
COMMIT
```

---

## Why each line is what it is

### The lock: `FOR NO KEY UPDATE`, not `FOR UPDATE`

This is the single most important line in the file, and the weaker lock is the correct one.

An FK insert into `submissions` or `solves` takes `FOR KEY SHARE` on the challenge row it
references. **`FOR UPDATE` conflicts with `FOR KEY SHARE`** — so a plain `FOR UPDATE` here would
block every *wrong-answer* submission insert behind the lock, silently destroying the entire premise
above: wrong answers, ~99% of traffic, must never queue. `FOR NO KEY UPDATE` is compatible with
`FOR KEY SHARE` (wrong answers sail through) yet still conflicts with **itself** (the correct path
still serializes, so first-blood and decay stay exact). We never update `challenges.id`, so the
weaker mode costs nothing. Verified empirically against a live Postgres, and pinned by the
concurrency suite.

Under the lock, the prior-solve count is **exact, not a guess** — so first blood is decided *inside*
the submitting transaction, which is what lets the HTTP response say *"first blood!"* without a
second read. It also makes the decay recalc exact **for free**: the last-writer-wins race on
`challenges.value` disappears as a side effect of a lock we were taking anyway.

**Do the math on the cost:** peak on the most popular challenge in a large CTF is a handful of
*solves* per second. The critical section is ~4 indexed statements against cached rows — low
single-digit milliseconds. **Two to three orders of magnitude of headroom.**

### The duplicate check is the constraint, never a SELECT

```sql
-- name: InsertSolve :one
INSERT INTO solves (submission_id, challenge_id, user_id, team_id, value)
VALUES (@submission_id, @challenge_id, @user_id, @team_id, @value)
ON CONFLICT DO NOTHING
RETURNING id;
```

`ON CONFLICT DO NOTHING … RETURNING` returns **zero rows on conflict** → `pgx.ErrNoRows` → that *is*
the `already_solved` signal. `UNIQUE(challenge_id, user_id)` / `UNIQUE(challenge_id, team_id)` is the
arbiter.

A `SELECT` here would be a check-then-insert, and a check-then-insert is a race no matter how careful
the surrounding code is. Do not regress this into one.

### Do NOT use a data-modifying CTE for the solve insert

A CTE would insert the `submissions` row **even when the `solves` insert conflicts**, orphaning a
`type='correct'` submission with no solve. **Two statements, one transaction.** This is a real trap
and it is invisible until you have orphans.

### The decay recalc must be ONE statement

```sql
-- name: RecalcChallengeValue :exec
UPDATE challenges c
   SET value = GREATEST(c.minimum, CEIL(
         ((c.minimum - c.initial)::float8 / POWER(NULLIF(c.decay, 0), 2))
         * POWER(GREATEST(cnt.n - 1, 0), 2) + c.initial))
  FROM (
      SELECT COUNT(*) AS n
        FROM solves s
        JOIN teams t ON t.id = s.team_id        -- the account table selected by instance.user_mode
       WHERE s.challenge_id = @challenge_id
         AND t.hidden = false AND t.banned = false
  ) AS cnt
 WHERE c.id = @challenge_id;
```

`UPDATE … FROM (SELECT COUNT(*))`, **never** a read-modify-write: the value is a function of the
solve count, so compute it from the count, in one statement, under the lock. `NULLIF(c.decay, 0)` is
belt and braces — `decay > 0` is already a `CHECK` on the table, because a zero decay is a division
by zero, not a challenge configuration, and the right answer is to reject it at the boundary rather
than coerce it to a number the admin never chose.

**`challenges.value` is only the *current asking price*** — what the board shows a solver they would
earn *now*. It does not feed the standings; `solves.value`, stamped at solve time, does. That is what
makes the scoreboard append-only, and what makes replaying the board to a past instant possible at
all.

### The announcement is enqueued **in the transaction**

```go
river.Insert(tx, AnnounceFirstBlood{ChallengeID: …, AccountID: …})
```

If the transaction rolls back, the announcement was never enqueued. **You cannot announce a first
blood for a solve that did not happen.** This single line is the entire reason the job queue is
Postgres-backed rather than a broker: Kafka or RabbitMQ would put the enqueue outside the
transaction and hand the bug straight back.

The worker checks freeze state at **send** time, not at enqueue time: announcements are **suppressed
during freeze**, because an announcement would leak exactly what the freeze hides. See
[First blood](../TARGET-FEATURES.md#first-blood).

---

## The Go shape

```go
func (s *Service) Submit(ctx context.Context, in SubmitInput) (Result, error) {
    tx, err := s.pool.Begin(ctx)
    if err != nil { return Result{}, err }
    defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
    q := s.q.WithTx(tx)

    ch, err := q.GetChallenge(ctx, in.ChallengeID)          // no lock yet
    if err != nil { return Result{}, err }

    // FlagIssuer picks the compare path by ch.FlagMode:
    // static+static → ConstantTimeCompare | static+regex → regexp | unique → hash probe
    match, err := s.flags.Check(ctx, q, ch, in.Provided)
    if err != nil { return Result{}, err }

    if !match.Correct {
        _, err = q.InsertSubmission(ctx, db.InsertSubmissionParams{
            Type: "incorrect", ChallengeID: ch.ID,
            UserID: in.UserID, TeamID: in.TeamID, IP: in.IP, Provided: in.Provided,
        })
        if err != nil { return Result{}, err }
        return Result{Status: Incorrect}, tx.Commit(ctx)     // ← never took the lock
    }

    // ── correct path only, from here ────────────────────────────────────────
    ch, err = q.LockChallengeForSubmit(ctx, ch.ID)           // SELECT … FOR NO KEY UPDATE (see note)
    if err != nil { return Result{}, err }

    sub, err := q.InsertSubmission(ctx, db.InsertSubmissionParams{
        Type: "correct", ChallengeID: ch.ID,
        UserID: in.UserID, TeamID: in.TeamID, IP: in.IP, Provided: in.Provided,
        AttributedAccountID: match.IssuedTo,                 // ← NULL unless flag_mode='unique'
    })
    if err != nil { return Result{}, err }

    solve, err := q.InsertSolve(ctx, db.InsertSolveParams{
        SubmissionID: sub.ID, ChallengeID: ch.ID,
        UserID: in.UserID, TeamID: in.TeamID,
        Value: ch.Value,                                     // ← snapshot, re-read under the lock
    })
    if errors.Is(err, pgx.ErrNoRows) {
        return Result{Status: AlreadySolved}, tx.Commit(ctx) // ON CONFLICT DO NOTHING
    } else if err != nil { return Result{}, err }

    var firstBlood bool
    if ch.FirstBlood != "none" {
        prior, err := q.CountSolvesExcluding(ctx, db.CountSolvesExcludingParams{
            ChallengeID: ch.ID, ExcludeSolveID: solve.ID,    // excludes hidden/banned
        })
        if err != nil { return Result{}, err }
        firstBlood = prior == 0
    }

    if firstBlood {
        if ch.FirstBlood == "bonus" {
            if err := q.InsertAward(ctx, /* value: ch.FirstBloodBonus */); err != nil {
                return Result{}, err
            }
        }
        // Transactional enqueue. Rolls back with us.
        if _, err := s.river.InsertTx(ctx, tx, AnnounceFirstBlood{
            ChallengeID: ch.ID, AccountID: in.AccountID,
        }, nil); err != nil { return Result{}, err }
    }

    if ch.Function != "static" {
        if err := q.RecalcChallengeValue(ctx, ch.ID); err != nil { return Result{}, err }
    }

    return Result{Status: Correct, FirstBlood: firstBlood, Value: solve.Value}, tx.Commit(ctx)
}
```

**Note `solve.Value` comes from the challenge row re-read under the lock** — `ch.Value` from the
pre-lock read could be stale if another solver decayed the challenge in between. Snapshot what the
lock says, not what the optimistic read said. This is subtle, and it is exactly the kind of thing the
concurrency suite exists to pin.

---

## Attribution is stamped, never joined

`submissions.attributed_account_id` is written **here**, in this transaction: the account the matched
flag was issued to. The consequence is that sharing detection is a **predicate on `submissions`
alone**, with no join to `flag_issues` — so it survives the flag being rotated, regenerated or
deleted, and it survives swapping the `FlagIssuer` implementation for an HMAC scheme later. See
[Unique flags](../TARGET-FEATURES.md#unique-flags).

Same principle as `solves.value`: **stamp the fact, don't recompute it.** A snapshot is a fact; a
join to a mutable row is an opinion.

---

## What is deliberately NOT here

- **No cache invalidation call.** Standings are a query over immutable rows. There is no cache to
  thrash, and in particular nothing on the wrong-answer path — which is most of the traffic — has any
  business touching the scoreboard.
- **No email, no webhook send, no notification publish.** Those are jobs, enqueued in the tx and
  executed outside it.
- **No audit trigger.** `submissions` and `solves` must **never** carry one — they *are* the gameplay
  audit trail already, and a trigger would double the write volume on the hottest path in the product
  for zero information.
- **No rate-limit check.** That is middleware (`INSERT … ON CONFLICT DO UPDATE SET n = n + 1` on a
  window-keyed row), upstream of this function.

---

## Invariants the concurrency suite pins

These are the claims the product is *sold on*. Each one is a race that the database, not the Go code,
is required to win. The suite is described in [the verification strategy](07-testing.md).

| Invariant | Test |
|---|---|
| Exactly one solve under N concurrent correct submissions | 100 goroutines, same account, same flag → `COUNT(solves) = 1` |
| First blood is awarded exactly once | 100 goroutines, N distinct accounts → exactly one `first_blood` award |
| A wrong answer never takes the challenge lock | assert no lock contention on an all-incorrect workload |
| Decay is exact under concurrency | N concurrent solvers → `challenges.value` = f(N), not f(last writer) |
| A pool instance is issued to at most one account | N concurrent first-views → `UNIQUE(instance_id)` holds |
| Score never goes negative | N concurrent hint unlocks → no double-charge |
| Standings are deterministic | any interleaving → same final board |

Run under `go test -race` against a real Postgres. **Not a mock** — the invariants live in the
constraints, and a mock cannot fail the way the database can.
