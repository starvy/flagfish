# _arch/04 — Import (CTFd archives) & Export (flagfish's own format)

**Owns:** `internal/platform/importer`, `internal/platform/exporter`, `internal/platform/tasks`.

Import and export move a **whole instance**, not a selection: a dump is a database, a restore is a
database. flagfish keeps a clean schema and puts all the ugliness of the source format into a
*translating adapter* at the edge — nothing downstream of the importer ever learns that CTFd exists.

Two rules bound the whole chapter:

- **Import is one-way.** flagfish reads CTFd export archives. It never *writes* one. Our export is
  our own format, field-masked ([§6](#6-the-flagfish-export-format)).
- **The archive is untrusted input.** It arrives over an admin upload form. It contains credentials.
  Nothing in it is allowed to reach a SQL string, a filesystem path, or a log line unexamined.

---

## 1. The CTFd archive format

### 1.1 Zip layout

```
export.zip
├── db/<table_name>.json        one file per table, for EVERY table in the source DB
├── db/alembic_version.json     the schema pin (always present)
└── uploads/<parent>/<file>     the upload tree, flattened to one level
```

- The table set is **discovered by introspection** of the source database, not written from a fixed
  list. An archive therefore contains core tables **plus** any plugin tables the source instance had,
  plus `alembic_version`. There is no exclusion list, so we cannot assume the set of `db/*.json` files
  is closed. See [§3.5](#35-everything-else-straight-copies-ids-preserved) for what we do with the
  ones we do not recognise.
- `uploads/` member names preserve only the **immediate parent directory** of each file. An upload
  nested more than one level deep is collapsed. In practice files are stored one directory deep
  (`<hash-ish dir>/<filename>`), which survives.

### 1.2 The JSON envelope

Every `db/<table>.json` is **one** JSON object, UTF-8, compact separators:

```json
{"count": 3, "results": [ {...row...}, {...}, {...} ], "meta": {}}
```

- `results` rows are **raw database rows**: exact column names, exact values, **IDs included**.
- `datetime`/`date` values are ISO-8601 strings; `Decimal` values are strings.
- A value whose type is not JSON-native and not one of those two is serialised as **`null`**. This
  happens *inside the source archive*, before we ever see it — a `null` is indistinguishable from a
  genuine `NULL`, and we have no way to detect the difference. It is listed in the lossy set
  ([§4.4](#44-smaller-losses-each-gets-a-report-line)) as a known unknown.
- `meta` is always `{}`. It carries nothing; do not look for anything there.
- The dump is unfiltered — every column of every row, including credential columns
  ([§1.4](#14-credentials-in-the-archive)).

### 1.3 The alembic pin

`db/alembic_version.json` is the archive's **only** reliable version marker:

```json
{"count": 1, "results": [{"version_num": "48d8250d19bd"}], "meta": {}}
```

A `ctf_version` string also exists, but only as a *row inside* `db/config.json`. It is advisory: it
records what the *source* instance was running, and it is overwritten by whatever CTFd last restored
that archive. **Branch on the alembic revision. Treat `ctf_version` as a hint for the import report,
never as a condition.**

### 1.4 Credentials in the archive

An archive is not just data. It is a **live credential set**, and that single fact drives several
design decisions elsewhere in this document.

| Field | Content |
|---|---|
| `users.password`, `teams.password` | bcrypt hashes. They import and keep working: we verify bcrypt on login and rehash to Argon2id in place. |
| `tokens.value` | **API tokens in plaintext** — stored and compared verbatim, not hashed. An archive hands you working bearer credentials for the source instance. |
| `config` rows | `mail_password`, `mailgun_api_key`, `oauth_client_secret` — plaintext. |
| `users.secret`, `teams.secret` | Present and dumped; nothing reads or writes them. Dropped on import. |

Three consequences, each stated as a flagfish rule:

1. **Tokens are never imported.** Our schema does not store bearer tokens in the clear, and importing
   another instance's live credentials into ours is not something an admin can consent to on behalf of
   its users. Every token is dropped, and the count goes in the report
   ([§4.2](#42-tokens--never-imported-by-policy)).
2. **A table name from the archive never reaches a SQL string.** Every statement in the restore names
   tables from a **compile-time list** ([§5.1](#51-import-order)). Any code path that would
   interpolate an identifier taken from an uploaded file is an injection sink; we do not have one and
   must not grow one.
3. **The upload path is admin-only, size-capped, and never echoed back.** The archive's contents do
   not appear in error messages or logs.

flagfish also refuses to *emit* this format, for the same reason ([§6](#6-the-flagfish-export-format)).

### 1.5 Archive validation

Reject before touching the database:

- **Zip-slip:** any member name that is absolute, `/`-prefixed, contains `..`, or contains `//` or `\\`.
- **Per-member size cap.**
- **Total-uncompressed-size and member-count caps** — the per-member cap alone does not stop a zip bomb.
- **A hard cap on total `uploads/` bytes.**
- **Structure:** a missing `db/` directory, or an unreadable/absent `db/alembic_version.json`, is a
  reject. Without the pin we cannot resolve [§2.3](#23-the-four-places-where-the-meaning-of-a-field-changed).

All of these are checked while streaming the zip central directory, before a single row is parsed and
before the restore transaction opens.

---

## 2. Version handling — a `switch`, not a migration engine

**We do not reimplement alembic and we never migrate the source schema.** We read JSON and translate
it into the current flagfish schema. The revision is used for exactly three things: admission control,
disambiguating **semantics that column presence cannot reveal**, and the import report.

### 2.1 The revision graph is linear — exploit it

All 31 shipped 2.x/3.x revisions form a single chain: each `down_revision` is referenced exactly once,
so there is no branch or merge to handle. A revision therefore maps to an **ordinal**, and every
version gate is `if ord(archive) >= ord(REV_X)`.

| # | Revision | First shipped in | What it changes |
|---|---|---|---|
| 0 | `8369118943a1` | 2.0.0 | Initial revision — the 2.x baseline |
| 1 | `4e4d5a9ea000` | 2.1.0 | `awards.type` |
| 2 | `b5551cd26764` | 2.1.0 | `teams.captain_id` |
| 3 | `b295b033364d` | 2.1.1 | ON DELETE CASCADE on FKs (no data impact) |
| 4 | `080d29b15cd3` | 2.2.0 | `tokens` table |
| 5 | `a03403986a32` | 2.3.0 | theme code-injection config keys |
| 6 | `1093835a1051` | 2.3.0 | default email-template config keys |
| 7 | `0366ba6575ca` | 3.1.0 | `comments` table |
| 8 | `75e8ab9a0014` | 3.1.0 | `fields` + `field_entries` tables |
| 9 | `07dfbe5e1edc` | 3.4.0 | `pages.format` |
| 10 | `ef87d69ec29a` | 3.4.0 | `topics` + `challenge_topics` |
| 11 | `6012fe8de495` | 3.4.0 | `challenges.connection_info` |
| 12 | `4d3c1b59d011` | 3.5.0 | `challenges.next_id` |
| 13 | `46a278193a94` | 3.5.1 | **MySQL datetime precision → fsp=6.** Semantic — [§2.3](#23-the-four-places-where-the-meaning-of-a-field-changed) |
| 14 | `0def790057c1` | 3.6.0 | `users.language` |
| 15 | `9e6f6578ca84` | 3.6.0 | `tokens.description` |
| 16 | `5c4996aeb2cb` | 3.7.0 | `files.sha1sum` |
| 17 | `9889b8c53673` | 3.7.0 | `brackets` table + `users.bracket_id` / `teams.bracket_id` |
| 18 | `a02c5bf43407` | 3.7.0 | `pages.link_target` |
| 19 | `4fe3eeed9a9d` | 3.7.4 | `challenges.attribution` |
| 20 | `a49ad66aa0f1` | 3.7.7 | `hints.title` |
| 21 | `62bf576b2cd3` | 3.8.0 | `solutions` table (+ `files.solution_id`, `unlocks.type='solutions'`) |
| 22 | `f73a96c97449` | 3.8.0 | `challenges.logic` (`any`/`all`/`team`) |
| 23 | `662d728ad7da` | 3.8.0 | `users.change_password` |
| 24 | `364b4efa1686` | 3.8.0 | `ratings` table |
| 25 | `5c98d9253f56` | 3.8.0 | **theme rename** `core-beta`→`core`. Semantic |
| 26 | `55623b100da8` | 3.8.0 | `tracking.target` |
| 27 | `24ad6790bc3c` | 3.8.0 | **ratings 5-star → ±1 votes.** Semantic |
| 28 | `67ebab6de598` | 3.8.1 | **`challenges.initial/minimum/decay/function`.** Semantic |
| 29 | `48d8250d19bd` | 3.8.3 | `challenges.position` |
| 30 | `336b8c601b94` | (unreleased at 3.8.6) | `modules`, `audiences`, `audience_members`, `module_audience_access`, `challenges.module_id` |

The revision→release mapping is derived mechanically (first release tag containing the commit that
added each migration file) and regenerated when we widen support.

### 2.2 Admission control

| Archive revision | Verdict |
|---|---|
| Any of the 13 CTFd-1.x revisions (`1ec4a28fe0ff`, `2539d8b5082e`, `7e9efd084c5a`, `87733981ca0e`, `a4e30c94c360`, `c12d2a1b0926`, `c7225db614c1`, `cb3cfcc47e2f`, `cbf5620f8e15`, `d5a224bf5862`, `d6514ec92738`, `dab615389702`, `e62fd69bd417`) | **Reject.** Their schema predates the 2.x model entirely; there is no useful translation. |
| In the known chain, ordinals 0–30 | **Accept.** Gate columns by ordinal. |
| Unknown (newer than we know) | **Reject by default**, with the revision string in the error. `--assume-revision=<known>` opts in to translating it as the newest known shape — loud, logged, unsupported. |

The default-reject on an unknown revision is deliberate. An unknown revision means an unknown
*semantic* change, and this format has shipped several of those in a single minor series
([§2.3](#23-the-four-places-where-the-meaning-of-a-field-changed)). Guessing produces a wrong
scoreboard, and a wrong scoreboard is discovered mid-event.

**Column presence is self-describing.** The JSON rows carry exactly the columns the source table had,
so the ordinal is *not* needed to know whether `hints.title` exists — `row["title"]` either is there or
is not. Missing → our column default. Unknown key → report, drop. The ordinal is needed **only** for
what follows.

### 2.3 The four places where the *meaning* of a field changed

These cannot be recovered from the row shape. They are the whole reason we read the revision.

1. **Dynamic scoring lives in two places (ord ≥ 28).**
   - **ord < 28:** `challenges` has **no** `initial/minimum/decay/function`. Dynamic challenges are
     `type='dynamic'` with a joined-table row in `db/dynamic_challenge.json` carrying
     `dynamic_initial`, `dynamic_minimum`, `dynamic_decay`, `dynamic_function`.
   - **ord ≥ 28:** `challenges.initial/minimum/decay/function` exist, and the **`standard`** type now
     uses them (`function` defaults to `"static"`). But for `type='dynamic'` rows those columns are
     **NULL** and the truth is *still* in `dynamic_challenge`: the migration adds the columns and does
     not backfill, and the dynamic type reads through to the joined table.
   - **Rule, at every ordinal:**
     `initial/minimum/decay/function := coalesce(dynamic_challenge.dynamic_*, challenges.*)`.
     Never read one source alone.
2. **`ratings.value` is 1–5 stars below ord 27, ±1 votes at and above it.** The migration folds
   `1,2 → -1` and `3,4,5 → 1`; the granularity is not recoverable. We apply the same fold on ord < 27.
   Ratings are not in v1, so the rows land in a staging table or are dropped with a report line —
   either way the fold is part of the translator and must not be lost if ratings land later.
3. **`config.ctf_theme` values were renamed at ord 25**: `core-beta`→`core`,
   `hacker-beta-theme`→`hacker-theme`, `learning[-beta]-theme`→`learning-theme`. flagfish has no theme
   system, so `ctf_theme` is a *dropped* config key. It is recorded here because it is the archetype of
   a **value-level** rename — the kind a column-presence check cannot see.
4. **Datetime precision (ord ≥ 13).** On MySQL sources, `DATETIME` had whole-second resolution before
   this revision. An archive from a pre-3.5.1 **MySQL** instance therefore has second-resolution
   `submissions.date` and `awards.date`, which is *below* the resolution of the scoreboard tiebreak
   (`ORDER BY … MAX(date) ASC, MAX(id) ASC`). Ties are real, and the `id` tiebreak decides them. **Not
   fixable** — record it in the report, so that a changed scoreboard order is *explained*, not
   *discovered*. Postgres and SQLite sources are unaffected.

Everything else in the chain is an additive column or table: absent → our default.

---

## 3. The translation table

**Reading direction only.** `→` means "this archive row lands here". Column names follow
[the schema chapter](01-schema.md); if that renames a column, only this table moves.

### 3.1 Type-discriminated tables, unwound

The source schema packs thirteen families into single tables keyed by a `type` discriminator. flagfish
gives each family its own table or its own typed column.

| Archive table | `type` values | → flagfish |
|---|---|---|
| `users` | `user`, `admin` | `users` + `users.role ∈ {user, admin}`. `admins` is a discriminator alias, **never a real table** — there is no `db/admins.json`. |
| `submissions` | `correct`, `incorrect`, `partial`, `discard`, `ratelimited` | `submissions`, all of them, verbatim — see [§3.2](#32-submissions--solves--the-joined-table-split) |
| `solves` | *(joined-table subclass of `submissions`, identity `correct`)* | `solves` — see [§3.2](#32-submissions--solves--the-joined-table-split) |
| `files` | `standard`, `challenge`, `page`, `solution` | `files` + the owner link. The sibling-nullable `challenge_id`/`page_id`/`solution_id` columns collapse into our exactly-one-owner model. |
| `flags` | `static`, `regex` | `flags.kind`. `flags.data == "case_insensitive"` is the only `data` value the format ships → `flags.case_insensitive bool`. A `type` outside `{static, regex}` (a plugin flag type) is a **hard row failure**, reported: silently dropping a flag makes a challenge unsolvable, which is worse than a failed import. |
| `unlocks` | `hints`, `solutions` | `hint_unlocks` / `solution_unlocks`. `unlocks.target` is an **untyped int** with no FK behind it; resolve it against the `type`. Dangling → report + drop. |
| `tracking` | discriminated, but no subclasses ship; values are written ad-hoc by call sites | `tracking`, flat, `type` as plain text — if we keep the table at all ([§8](#8-open-questions)). |
| `tokens` | `user` | `api_tokens` — **never imported**, [§4.2](#42-tokens--never-imported-by-policy). |
| `comments` | `standard`, `challenge`, `user`, `team`, `page` | Not in v1. Row-count report line only. |
| `fields` | `standard`, `user`, `team` | `custom_fields.scope ∈ {user, team}` |
| `field_entries` | `standard`, `user`, `team` | `custom_field_values` + `user_id`/`team_id` per scope. Value encoding: [§3.4](#34-field_entriesvalue--the-double-encoding). |
| `awards` | `standard` | `awards`, flat; drop the discriminator |
| `hints` | `standard` | `hints`, flat |
| `audiences` | `standard` | Not in v1. |
| `challenges` | `standard`, `dynamic`, + plugin types | `challenges.kind`. An **unknown** `challenges.type` is a **hard failure** naming the type. The source tolerates it by degrading silently to `standard`, but a plugin challenge type may have carried a different scoring rule, and a wrong `value` is a wrong scoreboard. `--force-unknown-challenge-type=standard` opts in to importing it as standard; the report says so. |

### 3.2 `submissions` / `solves` — the joined-table split

`solves` is a **separate table** whose `id` is an FK to `submissions.id`. It carries only
`{id, challenge_id, user_id, team_id}`. The `date`, `ip`, `provided` and `type` fields live **only** on
the parent `submissions` row. So:

- `db/solves.json` rows have **no date and no provided value.** Join on `id` into `db/submissions.json`
  (where `type == "correct"`) to reconstruct them.
- Our `UNIQUE(challenge_id, user_id)` / `UNIQUE(challenge_id, team_id)` constraints are the idempotency
  primitive we keep. The archive can only violate them if the source database already did.
- A `submissions.type='correct'` row **without** a matching `solves` row — or a `solves` row with no
  parent — is corruption. **Report and fail.** It means the source had FK damage, and importing it
  would silently produce a scoreboard that does not match the instance the admin is migrating from.

### 3.3 `requirements` — JSON or string, historically

`challenges.requirements`, `hints.requirements` and `awards.requirements` are JSON columns, but in
2.0.x they were persisted as a **JSON-encoded string** instead of a JSON object. Both shapes occur in
the wild, and the exporter's own normalisation of them is best-effort (it swallows the parse error and
leaves the string in place), so we cannot assume it ran.

**Our rule, applied to all three tables at every ordinal:**

```
v := row["requirements"]
switch:
  nil / ""                        → NULL
  json.RawMessage starting with { → use as-is
  string                          → json.Unmarshal(string) → object; on failure → report + NULL
```

The expected shape is `{"prerequisites": [ids], "anonymize": bool}`. An unknown key goes to the report.

### 3.4 `field_entries.value` — the double encoding

On MariaDB the JSON column type is backed by `LONGTEXT`, so a value round-trips as a **JSON string
containing JSON**: `"\"test\""` rather than `"test"`. The exporter tries to undo this, but its check is
a **fingerprint** — it normalises only when the row's key set is exactly
`{field_id, id, team_id, type, user_id, value}`. An archive whose `field_entries` gained a column is
therefore *not* normalised.

**The factual consequence for us: a `field_entries.value` may arrive either normalised or
double-encoded, and the archive does not say which.** So we normalise on **shape**, not on source:

```
peel(v):  while v is a string that parses as valid JSON → v = parse(v)   (bounded to 2 peels)
```

Two peels covers `"\"test\""` → `"test"` → `test`, and leaves a value that merely *looks* like JSON
once alone. The residual ambiguity is real and irreducible: a user whose custom-field answer is
literally `123` or `true` is indistinguishable from a double-encoded one. We **land every custom-field
value as `text`** (typed for display by `fields.field_type`) and report the count of values whose shape
changed. That makes the ambiguity a display concern, not a data-integrity one.

### 3.5 Everything else (straight copies, IDs preserved)

`teams`, `challenges` (merged with `dynamic_challenge`, [§2.3](#23-the-four-places-where-the-meaning-of-a-field-changed)),
`tags`, `topics` + `challenge_topics`, `notifications`, `pages`, `brackets`, `solutions`, `config`,
`awards`, `hints`.

- **`config`.** The source stores config as stringly-typed key/value rows **with no unique key on
  `key`**, so an archive **can** contain duplicate keys. flagfish's config is typed with
  `UNIQUE(key)`, so we must resolve: **last row wins by `id`; every duplicate key gets a report line.**
  Unknown keys → report + drop (they belong to subsystems that are not in v1).
- **Uploads.** Stream `uploads/<parent>/<file>` through the artifact-store seam. `files.location` in the
  row must match an archive member; a `files` row with no member (or a member with no row) → report.
  **Recompute the checksum** rather than trusting `sha1sum` — the column does not exist below ord 16.
- **Unrecognised tables.** Any `db/*.json` outside the known set is a plugin table. flagfish has no
  plugin system and will not invent a table for one. Report each as `unmapped_table{name, rows}` and
  drop it. That is the honest failure mode, and it is *visible*.

---

## 4. The lossy parts — the complete list

Every item here emits a structured line into the **import report**
([§4.6](#46-the-report-is-a-first-class-artifact)), which is persisted on the `tasks` row and rendered
by the admin UI. **Nothing here fails silently.** An organizer migrating a live event needs to know
exactly what did not come across, before the event, not during it.

### 4.1 `solves.value` — reconstructed, and loudly

**The constraint.** flagfish stamps a value on every solve: a solve's points are a **fact recorded at
solve time**, not a join to a mutable challenge row. *A snapshot is a fact; a join to a mutable row is
an opinion.*

**The archive has no such column.** In the source model, standings are computed by joining each solve
to the challenge's **current** `value`, and a decaying challenge's `value` is UPDATEd in place on every
solve. The value a solve was worth at the moment it happened was never recorded anywhere. It is not
missing from the archive; it never existed.

So it must be reconstructed, and every reconstruction is a guess about history. Two are defensible.

**(A) Stamp the challenge's current value — the default.**

```
solves.value := challenges.value (as of export)     -- for every solve of that challenge
```

- **The imported scoreboard is identical to the one the organizer was already looking at**, exactly, on
  day one — because the source scoreboard *is* `SUM(current challenge value)`. Standings, places,
  tiebreaks: the same. For a live migration this property is worth more than historical fidelity, and
  it is the only property we promise.
- The cost: every past solve of a decayed challenge is stamped with the **final, lowest** value. The
  first solver does not keep their higher value. The per-solve timeline is flat and wrong; only its
  *sum* is right. New solves get true stamped values, so an imported event has a visible discontinuity
  in its scoring history at the moment of import.

**(B) Replay the decay curve — behind `--reconstruct-decay`.**

The value is a pure function of `(initial, minimum, decay, function, solve_count)`, and the archive has
every solve with a date plus every account's hidden/banned flags. So we *can* order a challenge's solves
by date and compute `value_k = f(initial, minimum, decay, k)`.

It is still lossy, and **unverifiably** so, because it assumes all four of:

1. no account's `hidden`/`banned` flag changed after it solved — the flags are current-state only, the
   flip is not timestamped, and banning an account retroactively re-values the challenge;
2. no admin deleted a solve — a deleted solve *counted* at the time and is gone from the archive;
3. `initial`/`minimum`/`decay`/`function` were never edited mid-event — only the current values are
   stored, edits are not recorded;
4. `user_mode` never changed.

None of the four is checkable from the archive, and the price of being wrong is a scoreboard whose
**order** differs from the instance the organizer just migrated off.

**Settled:** (A) is the default; (B) is a flag for organizers who want a plausible history more than a
stable board. Both write a `SOLVE_VALUE_RECONSTRUCTED` report line naming every affected challenge and
the number of solves whose value was fabricated.

### 4.2 `tokens` — never imported. By policy.

`tokens.value` is a plaintext, live API credential
([§1.4](#14-credentials-in-the-archive)). Importing it would carry another instance's working bearer
tokens into ours, and it would mean *our* database now stores plaintext credentials — which our schema
deliberately does not. **Drop every token; report the count.** Users re-issue. This is a security
decision, not a limitation, and the report says so:

```
TOKENS_DROPPED{count: 47, reason: "plaintext credentials are never imported"}
```

### 4.3 Tables with no counterpart in the archive — empty by construction

| flagfish table | Why it is empty after import |
|---|---|
| `challenge_instances`, `flag_issues` | The source has no per-account flag issuance, so there is nothing to translate. Imported challenges are all `flag_mode='static'`; [unique flags](../TARGET-FEATURES.md) are configured **after** import. |
| `first_blood` | No counterpart. **Do not backfill it** from "earliest solve per challenge": first blood in our model is a *detected event with a bonus award and an announcement*, and awarding retroactive bonuses would change the imported scoreboard — breaking the one guarantee [§4.1](#41-solvesvalue--reconstructed-and-loudly)(A) makes. A **read-only** "earliest solver" view over imported solves is free and honest; a `first_blood` row with a bonus attached is not. |
| `audit_log` | No counterpart. Starts empty, **and the restore itself generates no rows** ([§5](#5-the-restore-transaction)). The audit trail begins the moment the import commits. |
| `submissions.attributed_account_id` | No provenance data exists to import. Set to `submissions.account_id` (self-attributed) for every imported row — "no sharing detected", which is the truth: we have no evidence either way. |
| `solves.value` | [§4.1](#41-solvesvalue--reconstructed-and-loudly). |

### 4.4 Smaller losses (each gets a report line)

- **`users.secret` / `teams.secret`** — dropped; nothing in the source reads them.
- **`tracking`** — the source's IP/user-agent log. `type` was never constrained and its values are
  ad-hoc call-site strings. If we keep the table it lands flat; if not, it is dropped with a count
  ([§8](#8-open-questions)).
- **Subsystems not in v1:** `comments`, `ratings`, `topics`/`challenge_topics`, `solutions`,
  `audiences`/`audience_members`/`modules`/`module_audience_access`, theme config keys, OAuth config
  keys. Each → `DEFERRED_TABLE{name, rows}`. **Counted, named and shown**, never silently dropped —
  these are precisely the rows an organizer will ask about.
- **`pages`** — the rows *are* imported even though flagfish v1 ships no CMS renderer. They are cheap
  to hold, and dropping them loses an organizer's rules and sponsor pages irrecoverably; nobody notices
  they are gone until the event is live. Imported, not rendered, reported.
- **Unrecognised (plugin) tables** — [§3.5](#35-everything-else-straight-copies-ids-preserved).
- **Pre-3.5.1 MySQL datetime precision** — [§2.3](#23-the-four-places-where-the-meaning-of-a-field-changed), item 4.
- **Pre-3.8.0 5-star ratings** — [§2.3](#23-the-four-places-where-the-meaning-of-a-field-changed), item 2; the fold is irreversible.
- **Values the source exporter serialised as `null`** ([§1.2](#12-the-json-envelope)) — *undetectable*.
  Named here so that it is a known unknown rather than a surprise.

### 4.5 What *does* round-trip cleanly

Users, teams, memberships and captains, brackets, challenges (including dynamic parameters), flags,
hints, files, tags, notifications, core config keys, **submissions and solves with their IDs, dates and
IPs**, and **awards** — including the negative awards that materialise hint-unlock costs, so unlock
spend round-trips through `awards` and the score arithmetic is preserved. Password hashes import and
keep working (bcrypt verified on login, rehashed to Argon2id in place). This is the 95%.

### 4.6 The report is a first-class artifact

```go
type ImportReport struct {
    SourceRevision     string          // "48d8250d19bd"
    SourceOrdinal      int             // 29
    SourceVersionHint  string          // config.ctf_version — advisory only (§1.3)
    Rows               map[string]int  // per source table: read / written / dropped
    Findings           []Finding       // {severity, code, table, count, detail}
    StartedAt, EndedAt time.Time
}
```

Persisted as JSONB on the `tasks` row, returned by `GET /admin/tasks/{id}`, and rendered as a
**mandatory post-import screen**. A finding of severity `error` blocks the commit
([§5](#5-the-restore-transaction)).

A partial import with a clear report beats a failed one, and beats a silently partial one by an
infinite margin.

---

## 5. The restore transaction

The restore is **one transaction**. A half-imported instance is worse than a failed import: the
organizer can retry a failure, but they cannot un-see a scoreboard that is missing 40,000 rows. Any
error at any point leaves the instance *exactly* as it was.

The design rules, and why each is load-bearing:

| Rule | Why |
|---|---|
| Restore into a schema-managed database; `TRUNCATE … RESTART IDENTITY CASCADE` **inside the tx**. Never drop the database. | A dropped database cannot report its own failure, and the wipe must be as rollback-able as the load. |
| Our schema is always at goose head. We never migrate *to* the archive's revision — we translate to ours. | The importer is an adapter, not a time machine. |
| `COPY … FROM STDIN` per table, one transaction. | Row-at-a-time inserts with a commit per row have no rollback point and are two orders of magnitude slower. |
| Task status lives in the `tasks` table, written from a **second connection**. | Status for a database restore must survive in the database, not in a cache that can evict it. |
| The job is a River job with `tasks`-owned state and a cancel path. | Fire-and-forget subprocesses cannot be cancelled, supervised, or resumed. |

```go
// internal/platform/importer — the shape. The two-connection rule is load-bearing.
progress := pool.Acquire(ctx)   // autocommit. writes tasks.progress. NOT in the tx.
defer progress.Release()

tx, _ := pool.Begin(ctx)        // ONE transaction. The whole restore.
defer tx.Rollback(ctx)          // no-op after Commit; the safety net otherwise

// 1. Singleton + FK/trigger suppression, session-scoped, so it dies with the connection.
//    Also disables our audit triggers: a million-row restore must not write a million audit rows.
tx.Exec(ctx, `SET LOCAL session_replication_role = replica`)
tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, importLockKey)   // belt & braces over the
                                                                  // tasks partial unique index

// 2. Wipe. Inside the tx — a failed import does not lose the old instance.
tx.Exec(ctx, `TRUNCATE users, teams, challenges, ... RESTART IDENTITY CASCADE`)

// 3. Translate + COPY, in FK-dependency order (§5.1). Order still matters for our own readers and
//    keeps the report legible, even though FK enforcement is off.
for _, t := range importOrder {
    rows := translate(t, archive, ordinal)      // §2, §3 — pure; unit-testable without a DB
    n, err := tx.CopyFrom(ctx, t.Table, t.Cols, pgx.CopyFromSlice(len(rows), ...))
    if err != nil { return err }                // → rollback. Nothing partial. Ever.
    progress.Exec(ctx, `UPDATE tasks SET progress=$1, detail=$2 WHERE id=$3`, pct, t.Name, taskID)
}

// 4. Sequences. COPY with explicit IDs does not advance a bigserial's sequence; the next INSERT
//    would collide with an imported row.
//
//    Table identifiers come from OUR compile-time list, never from the archive. A table name read
//    out of an uploaded file must never reach a SQL string.
for _, t := range importOrder {
    tx.Exec(ctx, `SELECT setval(pg_get_serial_sequence($1,'id'),
                                 COALESCE((SELECT MAX(id) FROM `+t.Ident+`), 0) + 1, false)`, t.Name)
}

// 5. FK validation. session_replication_role=replica does not defer FKs, it *ignores* them —
//    flipping it back at the end validates nothing. So check explicitly, still inside the tx:
tx.Exec(ctx, `SET LOCAL session_replication_role = DEFAULT`)
for _, c := range deferredFKChecks { /* NOT VALID → VALIDATE, or an anti-join count per FK */ }
//    A dangling FK → rollback + a report line. An import must not produce a corrupt instance.

tx.Commit(ctx)   // ← the only moment anything becomes visible

// 6. Uploads are streamed to the artifact store BEFORE the commit. Object storage is not
//    transactional: write blobs first, keyed by content hash; the DB rows are what make them
//    reachable. A rolled-back import leaves unreferenced blobs (GC'd), never broken rows.
```

**Why the second connection is not optional.** Progress `UPDATE`s issued *inside* the big transaction
are invisible until it commits: the bar sits at 0% for eight minutes and then jumps to 100%. The
tempting fix — commit per table — throws away the atomicity, which is the entire point. Design for the
second connection now.

**Cancel falls out for free.** `river.JobCancel` → the worker's `ctx` is cancelled → pgx cancels the
in-flight query → the transaction rolls back. Cancel is safe *because* it is one transaction.

### 5.1 Import order

FK-topological over **our** schema — a compile-time constant, never derived from the archive:

```
brackets → teams → users → (teams.captain_id backfill) → challenges → flags → hints → tags → files →
submissions → solves → awards → unlocks → notifications → custom_fields → custom_field_values →
tracking → config
```

`teams.captain_id → users.id` and `users.team_id → teams.id` are a **genuine cycle** in the source
data. We break it explicitly: COPY `teams` with `captain_id = NULL`, COPY `users`, then a single
`UPDATE teams SET captain_id = …` — inside the transaction, before FK validation.

---

## 6. The flagfish export format

**Designed, not inherited.** One hard rule: **the artifact the admin UI hands you must never contain a
password hash, an API token, or a secret.** An export is a file that gets emailed, dropped in a Slack
channel, and attached to a bug report.

### 6.1 Two profiles, because "backup" and "share" are different products

| | `safe` (**default**; the only thing the UI button produces) | `backup` (CLI only) |
|---|---|---|
| Password hashes | **omitted** | included |
| API tokens | **omitted, always** — in both profiles; they are plaintext-equivalent bearer credentials | **omitted, always** |
| Secret config keys | **omitted** | included |
| Emails | included (`--pseudonymize-emails` hashes them) | included |
| Restorable into a live event? | Yes — users land with `must_reset_password = true` | Yes, fully |
| At rest | plain zip | **encrypted** (age/X25519 recipient, required — not a flag with a default) |
| Audit | `audit_log` row | `audit_log` row, severity `warning` |

Field masking is the default and the UI's only option. A credential-bearing backup is a separate,
deliberately inconvenient, encrypted artifact produced only from `flagfishctl`.

### 6.2 The mask, declared next to the schema

```go
// One table. Reviewed in the same PR as any migration that adds a column.
var mask = map[string][]Rule{
  "users":       {Omit("password_hash"), Omit("secret"), Set("must_reset_password", true)},
  "teams":       {Omit("password_hash"), Omit("secret")},
  "api_tokens":  {OmitTable{}},                       // both profiles
  "config":      {OmitKeys(secretConfigKeys)},        // mail_password, mailgun_api_key,
                                                      // oauth_client_secret, smtp_*, webhook secrets
  "challenge_instances": {Omit("value_hash")},        // §6.5
  "audit_log":   {OmitTableIf(profile == Safe)},      // holds before/after JSONB of masked rows
  "sessions":    {OmitTable{}},                       // live credentials
  "river_job":   {OmitTable{}},                       // not our schema
}
```

**Default-deny:** a table with no mask entry is **not exported**. A new table must be added to `mask`
to appear in an export at all, so the failure mode of forgetting is "a table is missing", never "a
secret leaked". A CI test asserts that every table at goose head has a `mask` entry — present or
explicitly omitted.

### 6.3 The container

```
flagfish-export-2026-07-14T12-00-00Z.zip
├── manifest.json
├── data/<table>.jsonl        newline-delimited JSON — one row per line
├── uploads/<sha256>/<name>
└── SHA256SUMS
```

```json
// manifest.json
{
  "format": "flagfish/export",
  "format_version": 1,                    // ours. Bumped only on a breaking change.
  "schema_version": "20260714120000",     // goose revision — the pin
  "product_version": "1.2.3",
  "profile": "safe",                      // machine-readable: the restorer refuses to invent hashes
  "exported_at": "2026-07-14T12:00:00Z",
  "exported_by": 1,
  "user_mode": "teams",
  "tables": {"users": {"rows": 412, "sha256": "…"}, "solves": {"rows": 8901, "sha256": "…"}},
  "omitted": ["api_tokens", "sessions", "config.mail_password"]   // the mask is declared in the file
}
```

**Why JSONL and not `{count, results: […]}`.** A single JSON object per table forces the **entire
table into memory** on both write and read. JSONL streams in both directions: `COPY … TO STDOUT` → line
writer, and line reader → `COPY … FROM STDIN`, at constant memory. `count` moves into the manifest,
where it doubles as a checksum — `rows` must equal the number of lines read.

`schema_version` plays the role for our format that the alembic pin plays for the archive format: it
identifies the shape. Because we never migrate *to* an old shape, an older flagfish export is read by
the same ordinal-switch trick as [§2](#2-version-handling--a-switch-not-a-migration-engine), against
our own goose history. That machinery already exists for the importer; reusing it costs nothing.

### 6.4 Importing our own format

Same transaction ([§5](#5-the-restore-transaction)), a different translator, and strictly simpler: no
discriminated tables to unwind, no `requirements` variants, no double-encoding, and `solves.value` is
**present and true**.

`profile: "safe"` means users arrive with `password_hash = NULL, must_reset_password = true`. The
restorer **must not invent** a hash, and must not overwrite an existing hash with NULL — restore is
whole-instance, never a merge.

### 6.5 `challenge_instances.value_hash` — masked

It is `sha256(flag)`. Not a secret in the credential sense, but flags are short and low-entropy: an
exported hash list is **brute-forceable back to the plaintext flags**. A `safe` export of a *live*
event that leaks its own flags would be an own-goal. Omit it from `safe`; the pool is re-uploadable by
the author. It is named in the manifest's `omitted` list.

---

## 7. Golden tests

Importer golden tests are the **only** place archive compatibility is tested. Bounded, at the edge,
offline — no other package in the tree knows the format exists.

### 7.1 Fixture generation — reproducible, not curated

CTFd ships a deterministic seeder and a CLI export command, which is enough to generate archives
mechanically:

```
for tag in 2.1.0 3.1.0 3.4.0 3.5.0 3.6.0 3.7.0 3.8.0 3.8.6:
  for db in postgres mysql mariadb:      # mariadb is required — §3.4 is only reachable there
    docker run ctfd:$tag → seed(deterministic, --seed=1) → export → testdata/$tag-$db.zip
```

The archives are checked into `testdata/` (they are small — the seeder produces fake upload locations).
A `make fixtures` target regenerates them, and the generation script is the documentation of where they
came from. **Not** hand-edited archives: a hand-edited fixture proves only that we can read our own
fiction.

Plus a set of **hand-built adversarial archives** — these *are* fiction, on purpose, and are labelled so:
a duplicate `config.key`; an orphan `solves` row; an unknown `challenges.type`; an unrecognised table; a
2.0.x string-encoded `requirements`; a double-encoded `field_entries.value`; a zip-slip member; a zip
bomb; an archive at an unknown revision.

### 7.2 Coverage matrix — the *shapes*, not the versions

One fixture per **shape boundary** ([§2.1](#21-the-revision-graph-is-linear--exploit-it) ordinals),
because a shape boundary is the only thing that can differ:

| Fixture | Proves |
|---|---|
| 2.1.0 (ord 1) | The 2.x floor. `requirements`-as-string. No `fields`, no `tokens.description`, no `brackets`. |
| 3.1.0 (ord 8) | `fields`/`field_entries` appear; `comments` appear (not in v1 → report line). |
| 3.4.0 (ord 11) | `topics`, `pages.format`, `connection_info`. |
| **3.5.0 + MariaDB** (ord 12) | **The double-encoding ([§3.4](#34-field_entriesvalue--the-double-encoding)).** The only fixture that exercises it. |
| 3.5.0 + MySQL (ord 12) | The same archive *without* the encoding — proves `peel()` is idempotent. |
| 3.5.1 vs 3.5.0, MySQL (ord 13) | Second- vs microsecond `date` precision → the tiebreak report line. |
| 3.7.0 (ord 17) | `brackets` land; `bracket_id` on both account types. |
| 3.8.0 (ord 27) | 5-star→vote fold; theme rename; `solutions`; `challenges.logic`. |
| **3.8.6 (ord 29)** | **`challenges.initial` exists AND `dynamic_challenge` still holds the truth.** The nastiest case, and the current one. |

### 7.3 What each golden asserts

1. **Row-count identity** — for every mapped table,
   `rows_written == rows_in_archive - rows_reported_dropped`. No silent loss, by arithmetic.
2. **ID preservation** — every `id` in the archive exists in our database with the same value, and every
   FK still points at the same logical row.
3. **Sequence resync** — after import, an `INSERT` into every table succeeds and does not collide with
   an imported row. Assert `nextval > max(id)` per table.
4. **Scoreboard identity — the load-bearing one.** Compute standings on the imported database and
   assert they equal a **golden scoreboard checked into the fixture**, generated from the source
   instance's own scoreboard API at export time. This is a *snapshot* comparison against a JSON file
   produced once, not differential testing: we do not run anything but flagfish in CI. It exists because
   [§4.1](#41-solvesvalue--reconstructed-and-loudly)(A) *promises* exactly this property and nothing
   else, and a promise with no test is a wish.
5. **The lossy report is exact** — golden-file the `ImportReport`. If a future change starts silently
   dropping a table, the report diff fails the test. *This is the test that keeps
   [§4](#4-the-lossy-parts--the-complete-list) honest, and it is the most valuable one here.*
6. **Translation unit tests** — `translate()` is pure (`archive rows + ordinal → our rows`).
   Table-driven, no database, microseconds. Every semantic in
   [§2.3](#23-the-four-places-where-the-meaning-of-a-field-changed) and every discriminated family in
   [§3.1](#31-type-discriminated-tables-unwound) gets a case.
7. **Restore atomicity** — inject a failure at table *k*; assert the database is byte-identical to its
   pre-import state and the `tasks` row reads `failed` with the error. Run it for every *k*.
8. **Adversarial archives** — each is rejected with its *specific* error, and **nothing is written**.
9. **Round-trip, our format** — export → import → export. Assert artifact equality (modulo timestamps)
   and full database equality under `backup`; assert masked fields are absent and users are
   `must_reset_password` under `safe`.
10. **Mask completeness** — CI asserts every table at goose head has a `mask` entry
    ([§6.2](#62-the-mask-declared-next-to-the-schema)). The test that makes "we forgot to mask the new
    column" impossible.

---

## 8. Open questions

- **Is `tracking` in our schema at all?** It is roughly one row per login and per challenge view — the
  largest table after `submissions` — and nothing in the v1 feature set reads it. If it stays out,
  imported tracking rows are dropped with a count and
  [§5.1](#51-import-order) loses a table.
