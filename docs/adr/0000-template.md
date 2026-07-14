# ADR-0000: Title — the decision, stated as a claim

- **Status:** Proposed | Accepted | Superseded by [ADR-XXXX](XXXX-….md) | Deprecated
- **Date:** YYYY-MM-DD
- **Deciders:** who actually made the call

## Context

What forces are in play? State the problem, not the solution. Cite evidence — a source file, a
benchmark, a measured number. Be explicit about what *kind* of claim you are making: "the profiler
says this query takes 40ms" and "this feels slow" are not the same claim, and the reader cannot tell
them apart unless you say.

Name the constraint that makes this decision *forced* rather than *preferred*, if there is one.

## Options considered

- **(a) …** — the honest case for it.
- **(b) …** — the honest case for it.
- **(c) …** — including the ones that are obviously wrong, because "obviously" is doing work there
  and the next reader will not see it.

## Decision

State it as one sentence, in the present tense, as a rule. Then explain what drove it — the one or
two facts that actually decided it, not a summary of everything above.

## Consequences

### What this buys

Concrete. Ideally, things that were expensive before and are now free.

### ⚠️ What we gave up

**This section is mandatory and it is the point of the document.** An ADR that lists only benefits is
marketing, and the next contributor will (rightly) not trust the rest of it. State the cost in its
strongest form — the version a critic would use.

### What would change this decision

Name the fact that would flip it. If nothing would, say that, and say why.
