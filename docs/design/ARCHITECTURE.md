# Architecture — index

flagfish is a CTF platform: a scoreboard, a challenge catalog, and one very hot write path where
players submit flags. This directory is the long-form rationale for how it is built. The short form
— the decisions a contributor would otherwise re-litigate — lives in [`../adr/`](../adr/README.md).

## The stack

> **Postgres 17 (only)** · sqlc + pgx/v5 · goose · River · Huma + chi
> **React + Vite** · TanStack Table + TanStack Query · types generated from Huma's OpenAPI
> Per-solve score snapshots · lazily-locked hot path
> **One static binary** (`go:embed` SPA + migrations) · Apache-2.0

## The two principles

Nearly every decision in this directory is one of these two, applied.

**Stamp the fact; don't recompute it.** A solve's point value is recorded at solve time
(`solves.value`); attribution is recorded at submit time (`submissions.attributed_account_id`).
A snapshot is a fact; a join to a mutable row is an opinion. This is what makes an audit trail
mean anything.

**Back the invariant with a constraint, not a check.** Every `SELECT`-then-`INSERT` is a race,
and a scoring engine is exactly the system where players will find it. If the database can express
the rule, it must: one solve per account per challenge, one issued instance per flag, one row per
config key, one running task of a kind.

## The chapters

| | Doc | What's in it |
|---|---|---|
| 01 | [`_arch/01-schema.md`](_arch/01-schema.md) | The tables, with full Postgres 17 DDL. Every race, and the named constraint that kills it. Indexes, each tied to a real query. Verified against a live Postgres 17 — every constraint was watched to fire. |
| 02 | [`_arch/02-policy.md`](_arch/02-policy.md) | Authorization: four layers, a pure route gate, resource guards, the SQL predicate set, field redaction. The policies that are deliberate and must not be "fixed" by accident. |
| 03 | [`_arch/03-api.md`](_arch/03-api.md) | Huma + chi. RFC 7807 for every error, bare resources on success. The namespace map, the admin surface, the middleware chain. |
| 04 | [`_arch/04-import.md`](_arch/04-import.md) | Importing a CTFd archive: the format, the version switch, the translation table, the complete lossy list. And our own export format. |
| 05 | [`_arch/05-hotpath.md`](_arch/05-hotpath.md) | The submit transaction. The lazy lock. The `ON CONFLICT` arbiter. The invariants the concurrency suite pins. **Read this before touching `internal/gameplay`.** |
| 06 | [`_arch/06-layout.md`](_arch/06-layout.md) | Packages (`domain` imports nothing, and CI enforces it). Config as an in-memory snapshot. One binary, two roles. Observability. |
| 07 | [`_arch/07-testing.md`](_arch/07-testing.md) | Invariants → concurrency suite → importer goldens → parity tests. Why the race suite is the oracle we build first. |
| 08 | [`_arch/08-challenge-as-code.md`](_arch/08-challenge-as-code.md) | TOML frontmatter + Markdown body. Per-account instance templates. `flagfishctl test` = provable solvability. |

Alongside them:

- [`DECISIONS.md`](DECISIONS.md) — the long form behind the ADRs: the alternatives that were
  weighed, the evidence, and the same five hard queries written in every candidate data layer.
- [`TARGET-FEATURES.md`](TARGET-FEATURES.md) — unique flags, audit trail, first blood.
- [`ROADMAP.md`](ROADMAP.md) — what comes after v1, and what will never come.
- [`http-api.md`](http-api.md) — the implementation-level view of `internal/httpapi`.
- [`frontend.md`](frontend.md) — the SPA, and how it gets embedded in the binary.

## Where the design is load-bearing

Three places are easy to break without noticing, and each is argued at length in its chapter.

**The submit path is 99% wrong answers.** The challenge lock is taken lazily, on the correct-flag
path only. A lock at the top of the transaction would serialize every wrong guess in the event.
Contention must scale with solves, not with submissions.

**The freeze exemption belongs to the endpoint, not to the role.** The public scoreboard is frozen
for everyone, admins included; the admin scoreboard is live. That is deliberate — an admin needs to
be able to see exactly what the players see. Anything that reads "the current standings" must say
which of the two it means, and scoreboard time-travel must clamp to the freeze horizon for
non-admins or it is simply a freeze bypass.

**Decay never rewrites history.** A challenge's value falls as it is solved, but a solve that
already happened is a fact. Standings sum stamped values. The first solver keeps the points they
earned.
