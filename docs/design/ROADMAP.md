# Roadmap

The features below are what make flagfish worth building rather than merely correct.

**The organizing observation:** most of them cost almost nothing, because the architecture already
produced them. Decisions taken for *correctness* reasons turned out to have *product* consequences.
That is usually the sign an architecture is right — and it is why these belong in the plan now rather
than being invented later.

Scope discipline: v1 is core only. Each item is tagged **v1** (near-free — ship it) or **v1.1** (real
work — but shape v1 so it stays cheap).

---

## Scoreboard time-travel — v1 (API) / v1.1 (replay UI)

### The unlock

The per-solve score snapshot produced this by accident. Because `solves.value` is stamped at solve
time and never rewritten, **the scoreboard is a pure function of time**:

```sql
-- name: GetStandingsAsOf :many
-- The scoreboard exactly as it stood at any instant. Not an approximation — the real thing.
SELECT account_id, SUM(value) AS score, MAX(date) AS last_event
  FROM ( … solves ⋃ awards, both < @as_of … )
 GROUP BY account_id
 ORDER BY score DESC, last_event ASC;
```

A scoring engine that recomputes solve values from the challenge's *current* value destroys its own
history as it runs: replaying its board yields standings that were never true at any moment. Ours is
exact, because history is immutable. This is the same property that makes the
[audit trail](TARGET-FEATURES.md#audit-trail) mean anything — one decision, several payoffs.

### What it buys

- **`GET /api/v1/scoreboard?as_of=<ts>` — v1.** It is a `WHERE` clause. Ship it.
- **Animated scoreboard replay ("bar chart race") — v1.1.** The post-event video every CTF org wants
  to publish. People share these, which is distribution.
- **"Your CTF in review" — v1.1.** A per-account shareable recap (solves, first bloods, rank over
  time, the challenge you spent longest on). Near-zero cost once the `as_of` query exists.
- **Scoring simulator — v1.1.** *"What would the board look like if decay were 30?"* Scoring is a pure
  function over immutable events, so this is a recomputation, not a feature. Genuinely useful to
  organizers *before* an event — and impossible if solve values are recomputed on read.

### Design constraints for v1

**Nothing may mutate `solves` or `awards` after insert.** Time-travel is only correct if those tables
are append-only in practice, not merely in intent. Admin grading (a `type` transition on
`submissions`) creates a solve; it must never rewrite an existing solve's `value` or `date`. Setting
a solve's `date` once at creation — backdated to the submission timestamp — is fine. Rewriting it
later is not.

**`as_of` must be clamped to the freeze horizon for everyone but admins.** An unclamped `as_of` is a
freeze bypass by construction: a frozen board is exactly "standings as of the freeze time", so a
public caller who can pass an arbitrary `as_of` can simply ask for *now*. The clamp belongs in the
same place as the freeze predicate itself, not in the handler — see
[the policy layer](_arch/02-policy.md).

---

## Anti-cheat dashboard — v1 (queries) / v1.1 (UI)

### The unlock

This is the project's reason to exist, and the data is already being collected. With flag attribution
stamped on the submission row, most detectors are one query each.

| Signal | Mechanism | New data needed |
|---|---|---|
| **Flag sharing** | `attributed_account_id <> account_id` on a correct submission | none — designed into [unique flags](TARGET-FEATURES.md#unique-flags) |
| **Shared IP across accounts** | group `submissions.ip` by account; flag accounts sharing an IP with no team relationship | none — already recorded |
| **Suspicious timing** | account B solves N seconds after account A's first blood, on a challenge that took everyone else hours | none — `submissions.date` |
| **Solved without downloading** | a correct submission for a `flag_mode='unique'` challenge with **no `flag_issues` assignment** | none — falls out of unique flags |

**The last one is the good one.** With unique flags, submitting a valid flag for a challenge you
never opened is not a heuristic — it is provable, by construction. There is no legitimate way to have
obtained that flag. Most anti-cheat is statistical; this is a deduction.

### Design constraints for v1

Ship the **queries** and the columns they read in v1: they are free. The dashboard UI and any
review-workflow state (`reviewed` / `dismissed` / `actioned`) are v1.1. Detection stays **silent** —
never surface a signal to the player, or the detector teaches the sharer to evade it.

---

## Single static binary — v1

### The unlock

`go:embed` the built SPA and the goose migrations into the binary. Ship **one file plus Postgres**:
`./flagfish serve`. No Redis, no separate worker deployment, no asset server, no runtime file
dependencies.

### Why it is nearly free

Every decision already points here; the feature only has to be collected:

- **Postgres-only** ⇒ no cache or broker to deploy.
- **One binary, two roles** (`--with-worker` on by default) ⇒ no separate worker process to run.
- **No plugin system** ⇒ nothing to load at runtime; the binary is complete.
- **Plain-SQL goose migrations** ⇒ embeddable as files, run behind an advisory lock.
- **React + Vite** ⇒ a static `dist/` that `go:embed` swallows whole.

For self-hosters this is the headline, and it is the most visible expression of "this is a Go
project, and that matters."

### Design constraint for v1

**Do not design yourself out of it.** No runtime file dependencies, no assumed working directory, no
external template directory, no directory scanned at startup. Config from env and flags with sane
defaults. If it cannot run as `./flagfish serve` from an empty directory against a fresh Postgres,
something has gone wrong. See [Package layout, deployment, observability](_arch/06-layout.md).

---

## Challenge-as-code and `flagfishctl` — v1.1 (but shape v1 for it)

### The unlock

Challenge authors are engineers. They want git, not a web form. Challenges as files means challenges
get code review, CI, and — increasingly — machine generation.

A challenge is a directory: TOML frontmatter plus a Markdown body, artifacts alongside it, and one
JSONL file of per-account instances when the challenge issues unique flags.

```
flagfishctl sync ./challenges     # idempotent push. reviewable, diffable, CI-able.
```

The format, the template context, and the CLI's command surface are specified in
[Challenge-as-code](_arch/08-challenge-as-code.md).

### Why it fits

- It is the natural home for the **instance-pool upload**, which is otherwise an awkward admin-UI
  file-upload flow.
- A real CLI is the proof that the public REST API is first-class rather than an afterthought: it is
  a first-party consumer of the same OpenAPI contract third-party bots use, so it keeps the API
  honest. If the CLI needs an endpoint that does not exist, the API was incomplete.
- Challenges in git get **code review**, which is how you catch a broken challenge before an event
  rather than during it.

### Design constraint for v1

Design the **instance-pool upload API** with a CLI in mind — a bulk endpoint with a JSON or plain-text
body, authenticated by token, not a browser multipart form. Do not build a pool-upload flow that only
makes sense from a browser; the CLI ships later, but the endpoint it needs is designed now.

---

## Live event feed and Discord/Slack integration — v1

Already paid for by the Postgres-backed job queue (a `webhooks` queue with transactional enqueue) and
the `LISTEN/NOTIFY` SSE bus. First-blood announcements into Discord are table stakes for a modern
CTF, and the machinery exists.

Two constraints, decided elsewhere and restated because they are easy to lose:

- **Announcements are suppressed during a scoreboard freeze** (see
  [First blood](TARGET-FEATURES.md#first-blood)) — they leak exactly what the freeze hides. The award
  is still written; only the announcement is held.
- **Webhook jobs need a TTL, not just a retry cap.** An announcement that lands three weeks late is
  noise, not delivery.

---

## Observability — v1

OpenTelemetry traces, structured logs, Prometheus metrics. Cheap now, expensive to retrofit.

Two spans matter, and they are the two things the architecture is organised around:

- **the submit transaction** — lock acquisition, flag compare, solve insert, first-blood count.
- **the standings query** — the heaviest read in the product.

Plus one metric that is load-bearing for the product and not just for ops: **instance-pool
utilisation**. Pool exhaustion mid-CTF is a hard failure by design, and the gauge is what makes that
failure *preventable* rather than merely loud. It is the one number that must be right before an
event starts, not after.

---

## Plugin system — no, and here is why

Challenge types and flag types are in-tree Go interfaces, and
[ADR-0007](../adr/0007-no-plugin-system.md) rules out a runtime plugin loader — a "plugin system" in
Go is either a distributed system (the flag compare becomes a network call, inside a locked
transaction) or a recompile. Worse, a plugin system is a permanent API surface: the moment plugins
exist, every internal type they touch is frozen, and the schema changes this project depends on
become breaking changes to somebody's plugin.

That decision stands, and it is a real loss, honestly accounted for in the ADR: every extension is a
fork or a PR.

The work *now* is not to build a loader but to **not preclude a better answer**. Keep the
challenge-type and flag-type interfaces narrow and free of leaks into gameplay and policy. The
direction to explore later is a **declarative** challenge type — data, not code: a scoring formula
plus a compare mode, expressed in the challenge's TOML — which keeps the binary complete. The thing
to avoid is code that reaches past those interfaces into concrete internals, because that is what
would make any out-of-tree implementation impossible.

---

## Summary

| Feature | v1 cost | Why it is cheap |
|---|---|---|
| Scoreboard time-travel API | ~0 | One `WHERE` clause. The per-solve snapshot already paid for it. |
| Anti-cheat detector queries | ~0 | The data is already collected; `submissions.ip` and the attribution stamp exist. |
| Single static binary | ~0 | Postgres-only + one binary/two roles + no plugins + embedded migrations already imply it. |
| Discord/webhook feed | ~0 | The transactional job queue exists. |
| OpenTelemetry | small | Cheap now, expensive to retrofit. |
| Replay UI, recap card, simulator | v1.1 | Pure UI over the `as_of` query. |
| Anti-cheat dashboard UI | v1.1 | Pure UI over the detector queries. |
| `flagfishctl` + challenge-as-code | v1.1 | Real work — but shape the pool-upload API for it in v1. |
| Plugin system | never (see ADR-0007) | Keep the challenge/flag-type interfaces clean; the evolution is declarative types, not a loader. |

**Most of these are near-free consequences of decisions made for correctness reasons.** That is the
whole argument for having done the architecture first.
