# Target features

Three features define flagfish beyond a correct scoreboard: **unique flags**, an **audit trail**, and
**first blood**. They are grouped here because they share one property — they are the only things in
the product that add new writes to the flag-submission hot path, so they have to be designed
together, against the same cost budget.

---

## The organizing pattern: opt-in per challenge

Flag uniqueness and first blood are per-challenge modes. A challenge that opts out of both is
entirely unaffected: it costs nothing, and none of the machinery below runs for it.

```sql
ALTER TABLE challenges
  -- how flags are ISSUED. Orthogonal to flags.type, which is how they are COMPARED.
  ADD COLUMN flag_mode   text NOT NULL DEFAULT 'static'    -- 'static' | 'unique'
    CHECK (flag_mode IN ('static','unique')),
  ADD COLUMN first_blood text NOT NULL DEFAULT 'none'      -- 'none' | 'announce' | 'bonus'
    CHECK (first_blood IN ('none','announce','bonus')),
  ADD COLUMN first_blood_bonus int;                        -- NULL unless first_blood = 'bonus'
```

One config surface, two independent opt-ins, no interaction between them.

---

## Unique flags

Every account gets its own flag for the challenge. If a flag turns up under an account it was not
issued to, the flag was shared, and that is a deduction rather than a heuristic.

The implementation is an author-supplied pool of pre-generated instances, assigned lazily, behind a
`FlagIssuer` seam. Runtime per-team instancing (an orchestrator, or HMAC-templated flags) is a
drop-in second implementation of that same seam; nothing below assumes the pool is the only source.

### Pool plus lazy assignment

An author cannot supply an `(account, flag)` mapping up front, because **at authoring time the
accounts do not exist**: teams and users register after the challenges are loaded. There is nothing
to key the mapping on. So the pool is uploaded without owners, and ownership is decided later:

| Phase | What happens |
|---|---|
| **Authoring** | The author generates N instances (flag, and optionally an artifact and vars) and uploads them as a pool. |
| **Assignment** | On an account's **first access** to the challenge (view / artifact download), the platform assigns it one unused instance, in a transaction. |
| **Submission** | The submitted flag is hashed and looked up. Attribution is exact and indexed. |

This adds a write on a path that otherwise has none — the first view of a unique-flag challenge. It
is idempotent and cheap, but it is not free, and it is the **only place in the product where reading
a challenge mutates state**. Admin and hidden previews must not consume an instance.

### A pool entry is an instance, not a flag

The challenge body itself is often per-account: a different link, a different binary, a different
port. So a pool entry is not a flag, it is a **bundle** — a flag, an optional artifact, and arbitrary
per-account variables — and the challenge description is a **template**, rendered against the
requesting account's bundle. That is why the table is `challenge_instances`: the flag is one field of an
instance, not the thing itself.

The same shape is what makes runtime instancing a swap rather than a rewrite. A pre-generated bundle
and an orchestrator-provisioned container are the same row to every query in the system; only the
*source* of the instance differs.

### Schema

```sql
CREATE TABLE challenge_instances (
    id            bigserial PRIMARY KEY,
    challenge_id  bigint NOT NULL REFERENCES challenges(id) ON DELETE CASCADE,
    value_hash    bytea  NOT NULL,              -- sha256(flag). The plaintext is NEVER stored.
    artifact_id   bigint REFERENCES files(id) ON DELETE SET NULL,  -- per-account binary/VM/PDF
    vars          jsonb  NOT NULL DEFAULT '{}', -- {"link":"https://…","host":"x.ctf","port":31001}
    generation    int    NOT NULL DEFAULT 1,    -- bump on re-upload; old issues stay attributable
    UNIQUE (challenge_id, value_hash, generation)
);
CREATE INDEX challenge_instances_challenge_idx ON challenge_instances (challenge_id);

-- The assignment. Populated lazily, at first access — NOT at import.
CREATE TABLE flag_issues (
    challenge_id  bigint      NOT NULL REFERENCES challenges(id) ON DELETE CASCADE,
    account_id    bigint      NOT NULL,         -- users.id XOR teams.id, per user_mode
    instance_id   bigint      NOT NULL REFERENCES challenge_instances(id) ON DELETE RESTRICT,
    assigned_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (challenge_id, account_id),     -- one instance per account per challenge
    UNIQUE (instance_id)                        -- an instance is issued to AT MOST ONE account
);
```

Both constraints are load-bearing, and they replace what would otherwise be two check-then-insert
races:

- `PRIMARY KEY (challenge_id, account_id)` — an account cannot be issued two instances, even under
  concurrent first-views. This is also what makes assignment idempotent: a retry finds the existing
  row.
- `UNIQUE (instance_id)` — an instance cannot be issued to two accounts, even under concurrent
  assignment. **This is the constraint that makes the whole anti-cheat property true.** If it can be
  violated, "this flag was issued to exactly one account" stops being a fact, and every deduction
  built on it collapses.

Picking a free instance is serialized per challenge by a transaction-scoped advisory lock, taken as
its own statement before the pick. Under READ COMMITTED a statement's snapshot is taken at statement
start, so a lock acquired *inside* the pick would still read the pre-wait snapshot and hand out an
instance a concurrent transaction had just claimed; the `UNIQUE` violation would then surface to the
player as a 500 at CTF start, which is exactly the busiest minute of the event. The unique index is
still the backstop — it is what makes the property true regardless of how the pick is written — but
the lock is what keeps the happy path from raising.

### The flag unit is `account_id` — it follows `user_mode`

Teams mode issues per team; users mode issues per user. This is not a new concept: it is the same
`account_id` that every other gameplay query keys on.

**The consequence is the point.** Teammates legitimately share artifacts and flags with each other —
that is what a team *is*. Sharing detection therefore fires **only across accounts**, which is the
thing actually worth catching. Intra-team sharing is invisible, by design. Per-user flags in team
mode were considered and rejected: normal teamwork would trip the detector constantly, and a detector
that fires on correct behavior is a detector nobody reads.

### Pool exhaustion: hard fail, plus a pre-event gauge

300 teams register; the author uploaded 200 instances. Defined behavior:

- The challenge becomes **unavailable** to accounts with no assignment — a clear, loud error — and an
  admin alert fires.
- **The real fix is prevention.** The admin UI shows pool utilization *before the event starts*:
  ```
  pwn/heap   ███░░░░░░░   47/200 issued
  rev/vm     █████████░  190/200 issued
  ```
- **Falling back to a shared static flag is explicitly rejected.** It never blocks a player, and it
  *silently destroys the uniqueness property* for late registrants — so sharing detection quietly
  stops working for exactly the accounts you were most suspicious of. A loud failure beats a silent
  one, and a degraded anti-cheat property that nobody is told about is the worst outcome available.

### Submitting someone else's valid flag: accept it, and flag it silently

This is the central anti-cheat semantic. A player submits a flag that is **valid for the challenge**
but was issued to a **different account**:

- **The submission is accepted as correct.** A normal solve is created. The player sees an ordinary
  `Correct!` — no signal whatsoever that anything was detected.
- `submissions.attributed_account_id` is stamped with the account the flag was *issued* to.
- Because that differs from the submitter's account, the row surfaces in the admin review queue.

**Why silent acceptance rather than rejection.** Rejecting the flag tells the sharer they were
caught. They learn immediately that the platform tracks flag provenance, and they adapt: get the flag
from a different source, or stop leaving evidence. A detector that announces itself is not a
detector. Accepting silently means you accumulate evidence, they learn nothing, and the penalty is a
human decision made out of band. False positives — a shared machine, an account handover, a team
merge — get a human in the loop instead of an instant mid-event ban.

Automatic penalisation was considered and rejected for the same reason, plus one more: there is no
appeal path, and a false positive becomes an instant ban during a live event.

### Two compare paths

Two orthogonal axes, easy to conflate and expensive to conflate:

- **`challenges.flag_mode`** ∈ `static | unique` — how flags are **issued** (one shared flag, or one
  per account).
- **`flags.type`** ∈ `static | regex` — how a flag is **compared**. Only meaningful when
  `flag_mode = 'static'`: a regex flag cannot be pool-issued, because a pool entry is a concrete
  string that was baked into an artifact.

| Challenge | Mechanism |
|---|---|
| `flag_mode='static'`, `flags.type='static'` | Load the challenge's flags; `subtle.ConstantTimeCompare` in Go. |
| `flag_mode='static'`, `flags.type='regex'` | Load and evaluate. **Regex cannot be made timing-safe** — accepted, and documented as a property of choosing a regex flag. |
| `flag_mode='unique'` | `sha256(provided)` → one indexed probe on `challenge_instances (challenge_id, value_hash)` → the instance → its issuing account. Exact match only, by construction. |

Constant-time comparison against a pool of 500 flags is not viable, and it is not necessary. The
unique path never does a byte-wise comparison at all: it hashes and probes an index. There is no
per-byte timing channel to leak, the cost is O(1) rather than O(N_accounts), and the result is
*stronger* than a constant-time compare rather than a compromise for it.

**Correctness does not depend on who the flag was issued to; attribution does.** A valid flag is
correct no matter who submits it. The issuing account is recorded, not consulted.

### Attribution is stamped at submit time, not joined at query time

`submissions.attributed_account_id` is written **inside the submit transaction**. This is the most
consequential choice in the feature:

- Sharing detection becomes a predicate on `submissions` alone — no join to `flag_issues` at all:
  ```sql
  -- name: FindFlagSharing :many
  SELECT sub.id, sub.date, sub.user_id, sub.team_id, sub.challenge_id,
         sub.attributed_account_id
    FROM submissions sub
   WHERE sub.type = 'correct'
     AND sub.attributed_account_id IS NOT NULL
     AND sub.attributed_account_id <> sub.team_id     -- account column per user_mode
   ORDER BY sub.date DESC
   LIMIT @lim;
  ```
- Attribution **survives** the instance being rotated, regenerated, or deleted. A join-based audit
  trail is only as durable as the rows it joins to. A snapshot is a fact; a join to a mutable row is
  an opinion.
- Swapping `FlagIssuer` to a different implementation later changes one method and leaves every
  query, report and audit view untouched.

Same principle as `solves.value`: **stamp the fact, don't recompute it.**

### Rotation and regeneration

Re-uploading a pool bumps `generation`. Existing `flag_issues` rows keep pointing at their original
instance, so **submissions attributed under a previous generation stay attributable**. Rotation is
additive, and it has no mid-event blast radius.

This is a concrete advantage of a pool over a keyed-HMAC scheme, where flags are derived from a
server key: rotating that key invalidates every outstanding flag in the event, at once, with no undo.

---

## Audit trail

Two mechanisms, not one. Conflating them is the trap: they have different costs, different storage,
and different consumers.

### Gameplay events — nearly free

`submissions`, `solves`, `awards` and `unlocks` are **already append-only tables**. The gameplay audit
trail *is* those tables, plus two columns the schema already carries:

- `submissions.attributed_account_id` — who the flag was *issued* to.
- `solves.value` — what the solve was *worth at the time*.

The second is why the per-solve snapshot and the audit trail are the same decision wearing two hats:
**an audit record that says "awarded N points" is worthless if N is recomputed on read.** You cannot
build a defensible audit trail on top of retroactive revaluation. Stamping the value is what makes
the trail mean anything.

Deliverable: a read API over these tables. No new writes, no new hot-path cost.

### Admin actions — Postgres triggers plus a middleware-supplied actor

Everything an admin mutates is captured, with before/after diffs. The mechanism matters, because the
obvious implementation does not work:

| Layer | Knows **who** | Knows **what changed** |
|---|---|---|
| HTTP middleware | yes — actor, IP, route | no — it never sees the row's prior state |
| Postgres trigger | no — Postgres does not know it is alice | yes — `OLD` / `NEW` as JSONB |

Neither alone is sufficient, so use both. Middleware stamps the actor into a transaction-local
setting; one generic trigger function does the capture.

```sql
CREATE TABLE audit_log (
    id           bigserial PRIMARY KEY,
    actor_id     bigint,                        -- NULL = system/migration/console
    action       text        NOT NULL,          -- INSERT | UPDATE | DELETE
    target_table text        NOT NULL,
    target_id    bigint,
    before       jsonb,
    after        jsonb,
    at           timestamptz NOT NULL DEFAULT now(),
    ip           inet
);
CREATE INDEX audit_log_target ON audit_log (target_table, target_id, at DESC);
CREATE INDEX audit_log_actor  ON audit_log (actor_id, at DESC);

CREATE FUNCTION audit_capture() RETURNS trigger AS $$
BEGIN
    INSERT INTO audit_log (actor_id, action, target_table, target_id, before, after, ip)
    VALUES (
        nullif(current_setting('app.actor_id', true), '')::bigint,
        TG_OP, TG_TABLE_NAME,
        COALESCE(NEW.id, OLD.id),
        -- password_hash is masked out of both sides: an audit row must never become a
        -- second, less-guarded copy of the credential store.
        CASE WHEN TG_OP IN ('UPDATE','DELETE') THEN to_jsonb(OLD) - 'password_hash' END,
        CASE WHEN TG_OP IN ('INSERT','UPDATE') THEN to_jsonb(NEW) - 'password_hash' END,
        nullif(current_setting('app.ip', true), '')::inet
    );
    RETURN NULL;   -- AFTER trigger
END $$ LANGUAGE plpgsql;
```

```go
// middleware: one SET LOCAL per request transaction. Scoped to the tx; no leakage across pooled conns.
tx.Exec(ctx, `SET LOCAL app.actor_id = $1`, actorID)
tx.Exec(ctx, `SET LOCAL app.ip       = $1`, clientIP)
```

**Why triggers rather than a Go-side interceptor: completeness by construction.** The audit lives at
the *table*, not at the handler. A new admin endpoint cannot forget to audit itself. Neither can a
migration, a console session, or a `psql` fix at 3am. That is the difference between "we audit the
operations we remembered to wrap" and "we audit those *and the three someone adds next year*", and it
is the whole reason to accept the trigger's costs.

Three rules keep those costs bounded:

1. **Attach it to admin-mutable tables only** — `challenges`, `flags`, `challenge_instances`,
   `hints`, `users`, `teams`, `config`, `awards`, `pages`, `notifications`.
2. **Never attach it to `submissions` or `solves`.** They are already immutable gameplay facts. A
   trigger there would double the write volume on the hottest path in the product for zero
   additional information. This is the single easiest way to wreck the hot path.
3. **The bulk restore is exempt.** The importer's restore runs with
   `session_replication_role = replica`, which disables triggers — so a million-row restore does not
   generate a million audit rows. That is intentional, not a leak: an import is one admin action, not
   a million.

**Non-goal, deliberately: tamper-evidence.** The audit log is admin-readable and admin-deletable.
There is no hash chain and no revoked `UPDATE`/`DELETE` grant. This is recorded as a *chosen*
non-goal so that "the audit log is not tamper-proof" is never discovered as a surprise. It is worth
revisiting only if the trail is ever expected to hold up as evidence in a dispute.

---

## First blood

Opt-in per challenge: `none | announce | bonus`. Not every challenge wants it, and a bonus changes
the scoring, so it is never on by default.

### Detection happens inside the submit transaction, under the challenge lock

The submit path takes `SELECT … FROM challenges WHERE id = $1 FOR UPDATE` on the **correct-flag path
only** — wrong answers, which are the overwhelming majority of submissions, never touch the lock (see
[The submit hot path](_arch/05-hotpath.md)). Under that lock the prior-solve count is **exact, not a
guess**, so first blood is decided in the same transaction that creates the solve.

```
BEGIN
  read challenge                                        -- no lock
  match := FlagIssuer.Check(challenge, provided)        -- static/regex: constant-time compare
                                                        -- unique:  sha256 → indexed instance probe
  if INCORRECT: INSERT submission; COMMIT   -- ← never touches the lock

  SELECT … FROM challenges WHERE id=$1 FOR UPDATE       -- lock only now
  INSERT submission(type='correct', attributed_account_id=…)   -- ← attribution stamp
  INSERT solve(value=<current>) ON CONFLICT DO NOTHING         -- ← value snapshot; unique = arbiter
    → no row ⇒ already_solved; COMMIT
  count prior solves (excluding hidden/banned)          -- exact, under the lock
  if first && challenge.first_blood <> 'none':
      if 'bonus': INSERT award(value=first_blood_bonus)
      river.Insert(tx, AnnounceFirstBlood{…})           -- ← outbox: same tx
  UPDATE challenges.value  (single stmt, no read-modify-write)
COMMIT
```

The `river.Insert` inside the transaction is the reason the job queue lives in Postgres:
**transactional enqueue**. If the transaction rolls back, the announcement was never enqueued — you
cannot announce a first blood for a solve that did not happen. A broker outside the database (Kafka,
RabbitMQ) reintroduces exactly that failure, and it reintroduces it on the one code path where the
announcement is public and irrevocable.

### Hidden and banned accounts cannot take first blood

They are excluded from the prior-solve count, consistent with how they are already excluded from
dynamic-value decay. An admin test-solving a challenge from a hidden account must not burn its first
blood, and a banned account must not hold one.

### Freeze suppresses the announcement, not the award

Announcing *"Team X drew first blood on pwn/heap"* during a scoreboard freeze leaks exactly the
information the freeze exists to hide: who is solving what, and therefore who is moving.

- The **award row is still written**, and is hidden from the public scoreboard by the normal
  `date < freeze` predicate. Scores stay correct; nothing is recomputed when the freeze lifts.
- The **announcement is suppressed.** When the freeze lifts, the standings and the first bloods are
  revealed together.

Implementation: the `AnnounceFirstBlood` worker checks freeze state at *send* time, not at enqueue
time. This composes with the webhook TTL — a job older than its TTL is cancelled rather than
delivered — because an announcement must not resurrect a moment that has passed.

### `bonus` mode is coupled to the tiebreak

First-blood bonuses are `awards` rows, and the standings tiebreak is
`ORDER BY score DESC, MAX(date) ASC, MAX(id) ASC` taken across a `UNION ALL` of **solves and awards**.
So a first-blood bonus moves you in a tie: it raises your score (intended) *and* updates your
`MAX(date)`, which pushes you **backwards** against an account on the same score.

This is accepted, and it is worth stating rather than discovering. It is consistent with how every
other award behaves, including hint-unlock penalties, and the alternative — excluding first-blood
awards from the tiebreak's `MAX(date)` while still counting them in `SUM(value)` — buys a marginally
fairer outcome in a tie that will almost never occur, at the cost of a permanent special case in the
hottest read query in the product. A uniform scoring ledger is worth more than that.

---

## What these three cost on the hot path

| Path | New work | When |
|---|---|---|
| Challenge **first view** | 1 idempotent INSERT (`flag_issues`) | Only for `flag_mode='unique'`; admin/hidden views must not consume an instance |
| Submit — **incorrect** | **nothing** | Wrong answers never take the lock, never audit |
| Submit — **correct** | flag-hash probe + `attributed_account_id` stamp + `solves.value` stamp + prior-solve count + (conditional) award + (conditional) enqueue | Inside the existing challenge lock |
| Admin writes | 1 trigger INSERT per mutated row | Admin tables only |

**The hot path grows only on the correct-flag branch, which is the rare one.** That is not luck; it
is what the lazy-lock ordering was chosen to buy, and these features were designed to spend it.
