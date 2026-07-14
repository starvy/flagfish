# ADR-0005: `solves.value` is stamped at solve time. Decay never rewrites history.

- **Status:** Accepted
- **Date:** 2026-07-14
- **This is the most consequential decision in the project.**

## Context

Dynamic scoring decays a challenge's point value as more teams solve it: the hundredth solver of a
challenge earns less than the first. That is the intended behavior, and it is not in question here.

The question is **what happens to the first solver's score when the hundredth solve lands.**

There are only two coherent answers, and they differ in where the score lives:

- If the score is *derived* — standings `SUM(challenges.value)` by joining to the current challenge
  row — then decaying the challenge changes what every past solve was worth. Scores move
  retroactively. There is no record anywhere of what a solve was worth when it happened, because the
  only copy of that number was overwritten.
- If the score is *stamped* — `solves.value` written at insert and never touched again — then the
  board is a ledger. Decay changes what the *next* solve is worth and nothing else.

A platform can be built either way, and platforms are. But we are building one whose pitch is that
the scoreboard is **defensible** — auditable, replayable, and explainable to the team that lost by
three points. That pitch constrains the answer.

## Options considered

- **(a) Derived score / retroactive revaluation.** Standings join to the live challenge row. Simple;
  one source of truth for a challenge's value; a decay curve change re-prices the whole event for
  free.
- **(b) Per-solve snapshot.** Stamp `solves.value` at insert; standings `SUM` the stamped values.

## Decision

**(b). `solves.value` is stamped inside the submit transaction and is never rewritten.** Standings
`SUM` it.

The argument in one line: **an audit trail on top of retroactive revaluation is a contradiction.** An
audit record that says *"awarded 347 points"* is worthless if 347 is recomputed on read. A defensible
scoreboard cannot be a function of a mutable row.

There is a fairness argument too, and it is the one players actually feel: under (b), **the first
solver keeps the higher value they earned for being first.** Under (a) they do not — the reward for
being early evaporates as the field catches up.

The general principle, which independently settles several other decisions in this project: **stamp
the fact, don't recompute it. A snapshot is a fact; a join to a mutable row is an opinion.**

## Consequences

### What this buys

Three features, for the price of one column — and they were not designed for. They *fell out*.

- **The scoreboard becomes a pure function of time.** `GET /api/v1/scoreboard?as_of=<ts>` is a
  `WHERE date < $1`. Not an approximation, not a reconstruction: the board **exactly as it stood** at
  that instant. That in turn buys the animated scoreboard replay, the per-team "your CTF in review"
  card, and a scoring simulator (*"what would the board look like if decay were 30?"* — which is now
  a recomputation over immutable facts, not a feature that has to be built).
- **The audit trail means something.** Every award is a number that was true when it was written and
  is still true now.
- **The first solver keeps the higher value they earned.**

### ⚠️ What we gave up

**Standings can no longer be recomputed from first principles to catch a bug.** Under (a) the score
is derivable at any time from the current challenge values, so a scoring bug is self-healing: fix the
formula, and the board corrects itself on the next read. Under (b) the stamped values **are** the
truth. If a bug ever stamps a wrong value, that wrong value is a fact in the ledger, and fixing it is
a data migration with a human decision in it. This is the strongest argument against this ADR and it
is a real cost.

**`solves` and `awards` become append-only in practice, not merely in intent.** Time travel is
correct only if nothing mutates them after insert. Admin grading may transition a `submissions.type`
and create a solve backdated to the submission timestamp — that is fine, because the value is set
**once, at creation**. But a future feature that "corrects" a solve's value with an `UPDATE` would
silently break scoreboard replay, and **nothing would fail**. There is no constraint that can catch
it. **This one must be defended by a test, forever.**

**A decay-curve change does not re-price the past** — which is the flip side of the whole decision. If
an organizer misconfigures the curve and notices after fifty solves, they cannot fix it by editing
the challenge. They have to decide, explicitly, what to do about the fifty scores already in the
ledger. That is more honest and more painful, and we chose it deliberately.

**Import from an archive is lossy here, and the loss is unrecoverable in principle.** A source
platform that used model (a) never recorded what a past solve was worth, so for a decayed challenge
**the information does not exist upstream**. Our default is to stamp the challenge's *current* value
on import — historically wrong, but it makes the imported scoreboard equal the source's on day one,
which is the least surprising possible migration. Curve replay (reconstructing the decay curve from
solve counts) is available behind a flag, and it rests on four unverifiable assumptions. The importer
logs the lossiness per affected challenge, loudly. **We do not claim scoring compatibility with any
other platform, and we must never claim it.**

### What would change this decision

Nothing short of a hard requirement to produce a scoreboard identical to some other platform's — and
that is a requirement to build a clone. This is not a clone.
