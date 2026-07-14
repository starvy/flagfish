# ADR-0001: Model the domain in Postgres; import foreign archives through an adapter

- **Status:** Accepted
- **Date:** 2026-07-14

## Context

flagfish imports CTFd export archives, so a migrating organizer can bring an event's history with
them. That capability raises a schema question, and it has to be answered before anything else is
built: **does the archive format get a vote on our schema?**

The tempting answer is yes. Adopt the source schema and import becomes free — the archive maps
one-to-one, and in principle a live instance could be re-pointed at our binary. That is a real
benefit and it should not be waved away.

But an archive format is a *serialization of another system's object model*, and it carries
structures that are artifacts of that model rather than of the CTF domain:

1. **Single-table inheritance keyed on a `type` discriminator.** A subtype that needs a foreign key
   adds that column to the shared parent table, nullable for every sibling — so one `files` table
   carries `challenge_id`, `page_id`, and `solution_id`, mutually nullable, with the real invariant
   ("exactly one of these is set, depending on `type`") living in application code rather than in the
   database.
2. **A joined-table `solves` subtype** whose `id` is a foreign key to `submissions.id`, so every
   scoring query carries a `submissions ⋈ solves` join.
3. **An EAV `config(key TEXT, value TEXT)` table with no unique constraint on `key`**, read back
   through a coercion function that guesses the type from the string (`isdigit()` → int,
   `"true"`/`"false"` → bool, else string).

Each of those is a place where an invariant is a convention rather than a constraint. This decision
**gates the data layer** ([ADR-0002](0002-sqlc-not-an-orm.md)) and had to be settled first.

## Options considered

- **(a) Adopt the archive's schema.** Same tables, same inheritance, same EAV. Import is free;
  in-place migration becomes conceivable.
- **(b) Model the domain properly, and translate at the edge.** A clean schema, plus a
  one-directional importer that reads foreign archives.
- **(c) A clean schema and no import at all.** Greenfield; a migrating organizer starts from zero.

## Decision

**(b). Model the domain properly in Postgres, and write a one-directional archive importer.**

One table per concept. No `type` discriminator with sibling-nullable foreign keys. `solves` is a
single table. `config` has `UNIQUE(key)` and typed accessors. Real `challenge_instances`,
`flag_issues`, `first_blood`, and `audit_log` tables. 23 tables total, and **every known
check-then-insert race is closed by a named constraint**.

Two facts drove it.

**Go has no inheritance.** Under (a), every single-table-inheritance family becomes a hand-written
discriminated union over a wide nullable struct — thirteen times over. Each one is a place where a
subtype's invariant lives in a code comment instead of in the database. That is precisely the failure
mode this project exists to eliminate; adopting it wholesale on day one would be self-defeating.

**(a) would make the races permanent.** Nearly every check-then-insert race in this domain is fixed
by `ALTER TABLE … ADD UNIQUE` — the hint-unlock double-charge, the `files.location` double-insert,
the missing `UNIQUE(config.key)`. Under schema compatibility, **each of those fixes is a divergence
from the compatibility contract you just signed.** You would be preserving compatibility with a
schema whose defining property is that it cannot express its own invariants, and doing it to make one
migration — run once per organizer, ever — marginally cheaper.

The archive is an *input format*. It does not get a vote on the schema.

## Consequences

### What this buys

- **Every check-then-insert race is closed by a constraint**, not by application code. This is the
  single decision that makes the concurrency suite pass.
- One Go type per table. No wide nullable structs, no runtime type discrimination.
- Config is validated **once, at boot** — a malformed value fails loudly instead of silently becoming
  the wrong type at a call site three months later.
- The features this platform is built for — per-account instances, the audit trail, first blood — are
  natural tables rather than awkward inheritance siblings.

### ⚠️ What we gave up

**In-place migration is impossible, forever.** You cannot point the flagfish binary at another
platform's live database. Migration is import-from-archive: one-way, best-effort, and it costs the
organizer a maintenance window.

**The importer is real, ongoing work.** It must handle polymorphic `type` columns, the `requirements`
JSON-vs-string historical drift, MariaDB's `field_entries.value` double-encoding, ID preservation,
and sequence resync — across every archive revision we claim to support. It has a golden-file test
suite and it will need maintenance as the upstream format moves. This is a permanent tax we pay so
that the schema can stay clean. It is charged to us rather than to the user, which is the right place
for it, but it is not free.

**The import is lossy, and some of the loss is unrecoverable in principle.** Pre-decay solve values
were never recorded in the source, so they cannot be reconstructed
([ADR-0005](0005-per-solve-score-snapshot.md)). Plugin data, themes, and pages do not come across.
The complete lossy list is pinned by a golden-file test, so it cannot rot silently — but it is still
a list of things a migrating organizer loses, and they should be told before they run it, not after.

**We do not write foreign archives, ever.** An export in someone else's format would be a permanent
compatibility promise that quietly re-decides this ADR — and, because that format has no field
masking, it would ship a data-exfiltration primitive as a feature (see
[SECURITY.md](../../SECURITY.md)). Our export format is our own: field-masked, default-deny, and
restorable only by us.

### What would change this decision

One thing only: if in-place migration of a live foreign instance became a hard product requirement.
It is not, and we would push back hard if it appeared — a one-time operation should not dictate the
permanent shape of the schema.
