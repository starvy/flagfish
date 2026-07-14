# ADR-0003: Postgres only. No Redis.

- **Status:** Accepted
- **Date:** 2026-07-14
- **Coupled to:** [ADR-0004](0004-river-for-background-jobs.md)

## Context

A CTF platform has four jobs that a cache or a Redis would conventionally be doing:

| Job | What it needs |
|---|---|
| SSE fan-out across workers | broadcast pub/sub — a notification, delivered to every connected process |
| Sessions | a keyed blob with a TTL |
| Standings / score / config memoization | a read-through cache plus an invalidation graph |
| Rate-limit counters | atomic increment with expiry |

Each of those has an obvious Redis answer. The question this ADR settles is whether they are worth
a second stateful system.

## Options considered

- **(a) Postgres only.**
- **(b) Postgres + Redis** — the conventional answer, and genuinely the right tool for counters and
  pub/sub taken in isolation.

## Decision

**One stateful dependency. No Redis.** SSE over `LISTEN/NOTIFY`, sessions in a table, rate-limit
counters as an upsert, config as an in-process snapshot swapped on a `NOTIFY`.

**Run the numbers before running the Redis.** Peak load for a large CTF is *a few hundred flag
submissions per minute* — roughly **five writes per second**. Postgres on a laptop does five orders
of magnitude better than that. There is no scaling argument for Redis here; there is a *habit* of
reaching for Redis, and it should be resisted, because **a second stateful system is the single
most expensive thing you can add to an ops story.** It must be deployed, monitored, secured, backed
up, failed over, version-upgraded, and reasoned about during every incident.

Going through the four uses honestly, Postgres wins or ties on each:

- **SSE fan-out.** `LISTEN/NOTIFY` is not a compromise — it is a **drop-in semantic match**. The
  contract is broadcast-only, with no per-user routing and no replay, which is exactly what NOTIFY
  provides. The 8 kB payload cap is a *feature*: publish the id, let clients re-read. That makes the
  SSE stream a cache-invalidation signal rather than a second, divergent copy of the data.
- **Sessions.** A KV blob with a TTL is a table with an index on `expires_at` and a nightly
  `DELETE`.
- **Memoization.** The hard part is reproducing the *invalidation graph*, and that hard part is
  **identical in both options**. Config is ~100 rows: hold it in memory as an
  `atomic.Pointer[Snapshot]`, swapped wholesale on `LISTEN config_changed`. No cache, no TTL,
  nothing to get wrong.
- **Rate-limit counters.** The one genuinely Redis-shaped workload — atomic increment with expiry —
  so meet it head-on: `INSERT … ON CONFLICT DO UPDATE SET n = n + 1` on a window-keyed row. **We
  already write a `submissions` row for every submission.** The marginal cost is one more upsert on
  a path that already writes, and the atomicity is the database's, not a property of which cache
  backend happens to be configured.

## Consequences

### What this buys

- **`./flagfish serve` + Postgres.** That is the whole deployment. No Redis to run, secure, or fail
  over.
- **There is no cache to be a second source of truth.** A memoizing cache is a second copy of the
  data with an *unbounded* staleness failure mode: miss one invalidation and it is wrong until
  someone notices. Not having one collapses the entire class of bug where the scoreboard and the
  database disagree **and the scoreboard wins**. For a scoring system, that is the whole ballgame.
- One `LISTEN/NOTIFY` channel pays twice: the SSE bus **and** the config-snapshot invalidation.
- It is what makes [ADR-0004](0004-river-for-background-jobs.md)'s transactional enqueue possible at
  all. These two ADRs are one decision in two halves.

### ⚠️ What we gave up

**Horizontal scale-out is bounded by one Postgres.** There is no read-replica story for the
scoreboard yet, and no cross-region topology. If a workload appears that genuinely needs many app
replicas across regions, this is the ADR that has to move.

**Rate limiting costs a write.** It is cheap — one upsert on a path that already inserts a row — but
it is not free, and Redis would have been faster.

**We are unusual, and operators will ask about it.** "Where's the Redis?" is a question we will
answer forever.

**We give up in-memory caching of standings.** The standings query (a `UNION ALL` re-grouped and
joined) runs against Postgres each time it is asked for. At CTF scale it is fine, and the traced
`standings` span is watched precisely so we find out if that stops being true.

### What would change this decision

A hard requirement for many app replicas across regions, or a credible projection an order of
magnitude past "a few hundred submissions per minute."

Note the migration is not scary: rate-limit counters and sessions both sit behind an interface. If
we ever need Redis, we add it **for those two things**, having avoided the dual-write problems in
the meantime. **Postgres-first is not a bet against Redis; it is a decision not to pay for it until
it earns its keep.**
