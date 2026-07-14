# ADR-0004: River for background jobs. Not Kafka, not RabbitMQ, not a hand-rolled outbox.

- **Status:** Accepted
- **Date:** 2026-07-14
- **Coupled to:** [ADR-0003](0003-postgres-only-no-redis.md)

## Context

An event of this size does not *demand* a queue on throughput grounds, so we should be precise about
what a queue is actually for here. Three kinds of work want to be jobs:

| Work | Why it wants a queue |
|---|---|
| **Email** (registration, verification, password reset, …) | it blocks the HTTP response on a network call, and a failed send needs a retry rather than a swallowed exception |
| **Webhooks** (first blood → Discord) | outbound HTTP to a flaky third party is the canonical retry/backoff/poison-pill workload |
| **Import / export** | long-running; needs durable status, progress, and a failure surface |

And here is the property that actually decides the choice. Side effects triggered from inside a
database transaction are a **dual write**: do the send inline and a rolled-back transaction has
still sent the email, still hit the webhook, still announced the thing that did not happen. **We
must never announce a first blood for a solve that rolled back.** Avoiding that bug class is the
entire reason to have a queue — so a queue that *reintroduces* it is worse than no queue at all.
That single requirement eliminates most of the options below before any of them is scored on
features.

## Options considered

- **(a) River** — Postgres-backed, jobs live in *our* database.
- **(b) asynq** — Redis-backed.
- **(c) A hand-rolled `SELECT … FOR UPDATE SKIP LOCKED` outbox** — ~200 lines, zero dependencies.
- **(d) Kafka / RabbitMQ.**
- **(e) No queue; a goroutine with a retry for email.**

## Decision

**River**, with three queues: `email`, `webhooks`, `maintenance`. Long-running task *state* lives in
our own `tasks` table (River owns execution; the queue is never our public API). One binary, two
roles: `flagfish serve --with-worker` is the default.

### Why not Kafka or RabbitMQ — and it is not about scale

They live *outside* Postgres, so enqueueing becomes a **dual write**:

```
COMMIT solve to Postgres   ✅        publish to Kafka   ✅
publish to Kafka           ❌   ⇒    COMMIT solve       ❌   ⇒   Discord announces a first blood
  (solve exists, no announcement)      (rolled back)              that never happened
```

That second failure is **exactly the bug a queue exists to prevent**. You would pay a large ops cost
to *reintroduce* it — and then need a Postgres outbox anyway, at which point the broker is doing
nothing the outbox wasn't. They would also be the most operationally complex component in the
deployment, by a wide margin: more complex than the scoring engine they exist to serve.

The rule: **a broker earns its keep when the producer and the consumer are different services owned
by different people.** Ours are the same binary. A broker between two functions in one process is
not architecture; it is a network hop with a YAML file.

### Why River and not a hand-rolled outbox

This was the real contest, and it nearly won. Two things decided it.

**The "200 lines" figure is true right up until the job rescuer.** It covers the table, the poller,
backoff, max-attempts. It does *not* cover: a worker takes a `SIGKILL` mid-send, its job is stuck in
`running` **forever**, and nothing ever picks it up again. Fixing that means leases, heartbeats, or
a reaper that can distinguish "worker died" from "job is legitimately slow" — which is fiddly, and
is where the bugs live. That is the line item that turns 200 lines into 800. And that machinery is
near-worthless for email (SMTP fails fast) and **load-bearing for webhooks** — so the moment
webhooks land, you need the expensive part, which is precisely when you least want to be building
it.

**The asymmetry of being wrong:**

- *Take River, webhooks never happen* → we carried one dependency to send email. Zero harm.
- *Hand-roll it, webhooks happen* → we now maintain a job system as a side project. We find this out
  at 3am, during a live CTF, while Discord is flapping.

Under genuine uncertainty you do not pick the better expected value — **you pick the option whose
bad branch you can live with.**

### Why River and not asynq

**Transactional enqueue.** River's job table lives in *our* Postgres:

```go
tx, _ := pool.Begin(ctx)
// ... INSERT solve ...
river.Insert(tx, AnnounceFirstBlood{...})   // same transaction
tx.Commit(ctx)
```

Either the solve exists **and** the announcement is queued, or neither happened. **You cannot
announce a first blood for a solve that rolled back.** asynq enqueues into Redis — a second system —
which is the same dual-write problem as Kafka, in a smaller package. (asynq is also a one-volunteer
rescue of a project whose author last committed in 2023, against a 216-issue backlog.)

## Consequences

### What this buys

- The dual-write bug class is **structurally impossible**, not merely avoided.
- Zero new infrastructure. The queue is a table in the database we already run.
- Durable retries, backoff, and crash recovery for webhooks — the workload that actually needs them.
- `tasks` + River together give imports a real status row, real progress, and a real cancel.

### ⚠️ What we gave up

**River is still v0.x**, after years. API stability is by convention, not by promise.

**River is MPL-2.0**, not Apache-2.0 — a different license class from everything else in the stack.
It is compatible: MPL copyleft is **file-level**, and we do not modify River's files, so nothing
propagates into ours. We distribute its source and notice. But it is worth a conscious nod rather
than a surprise.

**River is open-core.** "River Pro" is a paid, private Go module that gates workflows, sequences,
encrypted jobs, durable periodic jobs — and the **dead-letter queue**, which is the kind of thing
you assume is table stakes. Verify the free tier still covers what we need before leaning harder on
it.

**Throughput is Postgres-bound**, which is irrelevant at our volume and would not be at someone
else's.

**Decay recalculation stays synchronous, inside the submit transaction.** We *could* defer it now —
that is a real option [ADR-0005](0005-per-solve-score-snapshot.md) unlocked — but the lock we are
already taking makes it free to keep inline, and inline is exact. A decision that unlocks an option
you then decline to take is still a good decision: it is the difference between "we can't" and "we
won't."

**Webhook jobs need a TTL, not just a retry cap.** A first-blood announcement that lands three weeks
late is noise, not delivery. And the announce worker must check freeze state at *send* time, not
enqueue time — announcing a first blood during a scoreboard freeze leaks exactly what the freeze
hides.

### What would change this decision

External consumers appearing — a public event stream, an analytics pipeline. Even then the move is
**not** "adopt a broker": it is write to Postgres transactionally and publish to the bus **from the
outbox**. That path stays open and costs nothing to defer.
