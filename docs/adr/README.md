# Architecture Decision Records

These are the decisions a new contributor would otherwise re-litigate. Each one states what was
decided, what drove it, and — the part that matters — **what was given up**.

**If you disagree with one, open an issue arguing against the ADR.** That is a welcome conversation.
A PR that quietly relitigates one is not.

## The index

| # | Decision | The one line that drove it |
|---|---|---|
| [0001](0001-clean-schema-with-import-adapter.md) | **Model the domain in Postgres; import foreign archives through an adapter** | An archive is an input format; it does not get a vote on the schema. Adopting one would make every check-then-insert race permanent, because each fix would be a divergence from the compatibility contract you just signed. |
| [0002](0002-sqlc-not-an-orm.md) | **sqlc + pgx/v5 + goose. No ORM.** | Three of the five hardest queries are raw SQL in *every* candidate; only sqlc build-verifies them. GORM silently zero-values a renamed column — a wrong scoreboard, and no error anywhere. |
| [0003](0003-postgres-only-no-redis.md) | **Postgres only. No Redis.** | Peak is ~5 writes/sec. Redis is a scaling answer to a problem we do not have, and a cache is a second source of truth with unbounded staleness. |
| [0004](0004-river-for-background-jobs.md) | **River. Not Kafka, not RabbitMQ, not a hand-rolled outbox.** | Transactional enqueue makes the dual-write bug class impossible: you cannot announce a first blood for a solve that rolled back. A broker would reintroduce it. |
| [0005](0005-per-solve-score-snapshot.md) | **Per-solve score snapshot** | An audit trail on top of retroactive revaluation is a contradiction. A snapshot is a fact; a join to a mutable row is an opinion. |
| [0006](0006-lazy-lock-on-the-hot-path.md) | **The challenge lock is taken lazily** | Most submissions are *wrong*; a top-of-transaction lock serializes every wrong guess. Contention should scale with solves, not submissions. |
| [0007](0007-no-plugin-system.md) | **No plugin system. Ever.** | "Plugin system" in Go is a distributed system or a recompile. The extension surface people actually want is two interfaces — ship them in-tree. |
| [0009](0009-annotations-and-portal-views.md) | **Challenge annotations and portal views** | A tag is membership; an annotation is a lookup by name. A view is a value the client has code for, so the set is closed and an unknown spelling is refused at the write. |

> The sequence has a gap at 0008: that ADR was withdrawn. Numbers are never reused and never
> renumbered — links, commit messages, and code review comments point at these, so a stable
> identifier is worth more than a tidy sequence.

## The two principles underneath them

Almost every decision here is one of these two, applied:

**1. Stamp the fact; don't recompute it.** Settles the score model (`solves.value`), flag attribution
(`submissions.attributed_account_id`), and the team-score divergence.

**2. Back the invariant with a constraint, not a check.** Every `SELECT`-then-`INSERT` is a race.
This independently settles flag issuance (`UNIQUE(instance_id)`), duplicate solves (`ON CONFLICT` is
the arbiter), config (`UNIQUE(key)`), and singleton tasks (a partial unique index).

## Settled, but not yet written up here

The long-form design in [`docs/design/`](../design/) also settles: Huma for the API, chi as the
router, the `tasks` table alongside River's job table, React + Vite + TanStack, the `FlagIssuer` seam
for unique flags, account mode fixed at setup, config as an in-memory snapshot, the two
infrastructure seams, import-only archive compatibility, Argon2id with bcrypt verification, the flag
types, Apache-2.0, and goose behind a `pg_advisory_lock`.

Promote one to an ADR when a contributor starts re-litigating it.

## Adding one

Copy [`0000-template.md`](0000-template.md), take the next free number, and add a row to the index.
The **"what we gave up"** section is mandatory — an ADR that lists only benefits is marketing, and the
next reader will not trust it.
