# _arch/01 — The schema

Postgres 17, and only Postgres 17. The schema is where flagfish keeps its invariants: **if the
database can express a rule, the database enforces it.** Everything below follows from that, plus
one more rule that the gameplay tables turn on — **a snapshot is a fact; a join to a mutable row is
an opinion.**

The DDL here is the shipped schema. It runs against a live Postgres 17, and every constraint in it
is *watched to fire*: the concurrency suite drives the race that the constraint exists to lose, and
asserts the database — not the Go code — is the one that says no. See
[the verification strategy](07-testing.md).

**Conventions.** `bigserial`/`bigint` ids everywhere. `timestamptz` everywhere, never `timestamp`:
a naive-UTC column is a bug waiting for the first DST-adjacent import, and there is no query in this
product that is cheaper for having dropped the offset. `text`, never `varchar(n)` — a length limit
is a domain constraint or it is nothing, and none of these are domain constraints. No extensions
required: `inet`, `bytea`, `jsonb` and `num_nonnulls` are all core.

**Migration order.** The domain groups below are a reading order, not an emission order. Migrations
must emit in FK-dependency order:
`instance → config → brackets → teams → users → (ALTER teams ADD captain FK) → fields/field_entries
→ challenges → files → flags/hints/tags → challenge_instances → submissions → solves → awards →
flag_issues → hint_unlocks → tracking → api_tokens → notifications → tasks → audit_log → triggers`.
(`files.challenge_id → challenges` and `challenge_instances.artifact_id → files`, hence challenges
before files before the instance pool. §2.3 is presented before §2.4 for reading only.)
Migrations run behind `pg_advisory_lock`, so N replicas starting at once produce one migration run.
River's `river_*` tables are created by River's own migrator — **we do not define them**, and we
never read them. Another library's schema is not our API.

---

## 1. The tables

Twenty-three tables, in seven groups. The recurring question is *why is this its own table*, and the
recurring answer is one of three: a distinct lifetime, a distinct set of invariants, or a distinct
write frequency. Nothing here is a class hierarchy flattened into a discriminator column; where a
`type` column survives it is a **status or a strategy selector**, and it is `CHECK`-constrained to a
closed set.

If you are importing an archive from another platform, the mapping from its shapes onto these tables
is the importer's business, and it is documented there: see [the importer](04-import.md).

### Instance & config

| Table | What it holds | Why it is its own table |
|---|---|---|
| `instance` | The singleton row: `user_mode` (users \| teams), setup time, schema version. | `user_mode` decides which column every gameplay query groups by. It must be *fixed at setup and immutable*, and a row with a `CHECK (id)` singleton key plus an immutability trigger is the only way to say that in the database. Keeping it in `config` would make the most load-bearing value in the product an editable string. |
| `config` | Instance settings, keyed strings. | Deliberately a key/value store, because the importer must ingest keys we have never heard of. Everything read on a hot path is read through a typed Go accessor over a whole-table snapshot in an `atomic.Pointer`, refreshed on `LISTEN/NOTIFY` — so the untyped shape never reaches the query layer. `UNIQUE(key)` is non-negotiable: config is read *by key*, and a duplicate row makes that read nondeterministic. |

### Accounts

| Table | What it holds | Why it is its own table |
|---|---|---|
| `users` | Every human account, with `role IN ('user','admin')`. | Admin is not a subtype — it adds no data, only authority. That is a flag, and it is modelled as one. |
| `teams` | Team accounts: name, credentials, captain, bracket. | Teams score, users score; both are accounts, but they are *different* accounts, with different membership rules and different deletion semantics. |
| `brackets` | Named scoring divisions (`applies_to` users or teams). | A bracket is referenced from two account tables and outlives both. |
| `fields` | Admin-defined registration fields (text \| boolean, required, public). | Registration is gated on "all required fields filled", so the field definition has to be queryable, not a JSON blob in config. |
| `field_entries` | One account's answer to one field. | `CHECK (num_nonnulls(user_id, team_id) = 1)` — an entry has exactly one owner. `UNIQUE(field_id, user_id)` / `UNIQUE(field_id, team_id)`: an account answers a field once, and that is a database rule, not a form-validation rule. |
| `api_tokens` | `sha256` of an issued token, its owner, its expiry. | Tokens have their own lifetime (expiry sweep) and their own secret-handling rule: only the hash is stored. |
| `tracking` | The IPs an account has been seen from. | Written on every authenticated request path and read only by admin/anti-cheat screens; mixing that write rate into `users` would put a hot `UPDATE` on the row every authorization check joins to. |

### Files

| Table | What it holds | Why it is its own table |
|---|---|---|
| `files` | One stored blob: location, `sha256`, size, original name, optional owning challenge. | **A file has exactly one owner, and its identity belongs to the blob, not the owner.** One table, with the ownership expressed as a `CHECK (num_nonnulls(...) <= 1)`. Splitting per owner type would make `UNIQUE(location)` unenforceable — that uniqueness must hold across *all* files or it holds across none — and the per-account artifact on `challenge_instances` needs a single FK target. `<= 1`, not `= 1`: an ownerless media-library file is legitimate. |

### Challenges, flags, hints

| Table | What it holds | Why it is its own table |
|---|---|---|
| `challenges` | The challenge, including its dynamic-scoring parameters (`function`, `initial`, `minimum`, `decay`) and its two feature opt-ins (`flag_mode`, `first_blood`). | One store for one concept. Dynamic scoring is not a subtype — it is four columns and a `CHECK` that says they are all present exactly when `function <> 'static'`. |
| `flags` | The accepted answers: `type IN ('static','regex')`, content, case-insensitivity as a real `boolean`. | A challenge has many flags; a flag has a comparison strategy. `type` selects a compare function, it does not select a table. |
| `hints` | Purchasable hints: content, cost, prerequisites, position. | Independent lifetime (a hint is added mid-event), and `hint_unlocks` needs a real FK to point at. |
| `tags` | Free-text labels, `UNIQUE(challenge_id, value)`. | A tag is a set membership; a set does not contain the same element twice. |

### Unique flags

Per-account flags (see [Unique flags](../TARGET-FEATURES.md#unique-flags)). The anti-cheat property —
*a leaked flag names the account it leaked from* — is only as true as the constraint underneath it.

| Table | What it holds | Why it is its own table |
|---|---|---|
| `challenge_instances` | The pool. One row is a **bundle**: `sha256(flag)`, an optional per-account artifact, and `vars` (e.g. host/port) that the challenge description renders against. The plaintext flag is never stored. | The pool is of *instances*, not of flags, because what an account is issued is a whole environment, and the flag is one field of it. That is also the seam for live per-team instancing: swapping pre-generated bundles for a runtime provider changes the source of a row and leaves the schema, the rendering and every query untouched. |
| `flag_issues` | Which account holds which instance. | `PRIMARY KEY (challenge_id, account_id)` and `UNIQUE (instance_id)` are the two halves of the property: an account is issued at most one instance, and an instance is issued to at most one account. Both are enforced under concurrent first-views, which is when they matter. |

### Gameplay

| Table | What it holds | Why it is its own table |
|---|---|---|
| `submissions` | Append-only log of **every** attempt: correct, incorrect, rate-limited. Carries the IP and the stamped `attributed_account_id`. | An event. It happened; it is never rewritten. |
| `solves` | The scoring **fact**: challenge, account, `value` *as of the moment it was earned*, date. | **A solve is a fact; a submission is an event.** Keeping them apart is what lets the standings query be a scan of `solves` with no join to `submissions`, and what lets a solve exist with no submission at all (an import, an admin grant). `UNIQUE(challenge_id, user_id)` / `UNIQUE(challenge_id, team_id)` is the idempotency primitive of the entire hot path. |
| `awards` | The points ledger: manual awards, hint spends (negative), first-blood bonuses. `challenge_id` present, so "at most one first-blood bonus per challenge" is expressible. | Points that are not solves. Same append-only rule; `type` is a real category, not a class. |
| `hint_unlocks` | Which account bought which hint, and the `award_id` that charged them. | `UNIQUE(hint_id, user_id)` / `UNIQUE(hint_id, team_id)` plus `award_id NOT NULL UNIQUE` is what makes a hint purchase charge exactly once. Without a table, there is nothing to hang those uniques on. |

### Content, audit, jobs

| Table | What it holds | Why it is its own table |
|---|---|---|
| `notifications` | Broadcast announcements. | Every notification goes to every client. There are no targeting columns, because there is no targeting. |
| `tasks` | The user-visible state of an import or export: state, progress, error. | `tasks` owns the **state**; River owns the **execution**. A partial unique index (`kind` WHERE state IN ('queued','running')) makes "at most one import in flight" a database fact — not a worker-pool setting, which is per-process and would let three replicas run three concurrent database restores. |
| `audit_log` | Who changed what, before/after as JSONB, when, from where. | Written by triggers on the admin-mutable tables, read by nobody on a hot path. It must survive the deletion of the actor it names, which is why `actor_id` has no FK. |

River's `river_job` / `river_leader` / `river_queue` tables exist in the same database and are created
by River's migrator. `tasks` is the API surface; the job tables are an implementation detail we do
not query.

---

## 2. DDL

### 2.1 Instance & config

```sql
-- The singleton exists so that user_mode is enforceable in the DATABASE. It selects which account
-- column every scoring query groups by; if it could be flipped mid-event, every past solve would
-- silently re-point at a different account. Fixed at setup, immutable thereafter.
CREATE TABLE instance (
    id          boolean     PRIMARY KEY DEFAULT true CHECK (id),   -- singleton
    user_mode   text        NOT NULL CHECK (user_mode IN ('users','teams')),
    setup_at    timestamptz NOT NULL DEFAULT now(),
    version     text        NOT NULL
);

CREATE FUNCTION instance_user_mode_is_immutable() RETURNS trigger AS $$
BEGIN
    IF NEW.user_mode IS DISTINCT FROM OLD.user_mode THEN
        RAISE EXCEPTION 'user_mode is fixed at setup and cannot be changed';
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;

CREATE TRIGGER instance_user_mode_immutable
    BEFORE UPDATE ON instance
    FOR EACH ROW EXECUTE FUNCTION instance_user_mode_is_immutable();

-- Key/value storage with a typed accessor in Go; the whole table is held in an atomic.Pointer
-- snapshot refreshed on LISTEN/NOTIFY. The untyped shape survives because the importer must ingest
-- arbitrary keys, including ones authored by plugins we do not have.
CREATE TABLE config (
    id          bigserial   PRIMARY KEY,
    key         text        NOT NULL,
    value       text,
    updated_at  timestamptz NOT NULL DEFAULT now()
);
-- Config is read BY KEY. A duplicate row makes that read nondeterministic, and no amount of
-- application care prevents two concurrent writers from creating one.
CREATE UNIQUE INDEX config_key_uniq ON config (key);
```

### 2.2 Accounts

```sql
CREATE TABLE brackets (
    id          bigserial PRIMARY KEY,
    name        text NOT NULL,
    description text,
    applies_to  text NOT NULL CHECK (applies_to IN ('users','teams'))
);

CREATE TABLE teams (
    id           bigserial   PRIMARY KEY,
    name         text        NOT NULL,
    email        text,
    password_hash text,                          -- Argon2id; a legacy bcrypt hash verifies and is
                                                 -- rehashed on next login
    secret       text,
    website      text,
    affiliation  text,
    country      text,
    bracket_id   bigint      REFERENCES brackets(id) ON DELETE SET NULL,
    captain_id   bigint,                          -- FK added after `users` exists (circular)
    hidden       boolean     NOT NULL DEFAULT false,
    banned       boolean     NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now()
);
-- Enrollment joins a team BY NAME, and creation checks "name taken". Without this index both are
-- lost races and the lookup is ambiguous the moment they lose. The index is the arbiter.
CREATE UNIQUE INDEX teams_name_uniq  ON teams (name);
-- Email is normalized on write and unique case-INSENSITIVELY. A case-sensitive unique plus a
-- lowercasing registration path is two different notions of identity, and the gap between them is
-- an account-takeover surface at password reset.
CREATE UNIQUE INDEX teams_email_uniq ON teams (lower(email));

CREATE TABLE users (
    id            bigserial   PRIMARY KEY,
    name          text        NOT NULL,
    email         text        NOT NULL,
    password_hash text,                           -- NULL for OAuth-only accounts
    role          text        NOT NULL DEFAULT 'user' CHECK (role IN ('user','admin')),
    secret        text,
    website       text,
    affiliation   text,
    country       text,
    language      text,
    bracket_id    bigint      REFERENCES brackets(id) ON DELETE SET NULL,
    team_id       bigint      REFERENCES teams(id) ON DELETE SET NULL,
    hidden        boolean     NOT NULL DEFAULT false,
    banned        boolean     NOT NULL DEFAULT false,
    verified      boolean     NOT NULL DEFAULT false,
    must_change_password boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_uniq ON users (lower(email));

-- ON DELETE SET NULL, not a hand-nulling loop in the delete handler: any write path that bypasses
-- the handler would otherwise orphan the members.
ALTER TABLE teams
    ADD CONSTRAINT teams_captain_fk FOREIGN KEY (captain_id)
        REFERENCES users(id) ON DELETE SET NULL;

-- A user leaves a team (team_id -> NULL) or joins from teamless (NULL -> team_id). A direct switch
-- is never legal: solves stamp team_id at solve time, so a silent switch lets one user feed two
-- teams. Enforced here so no future write path can do it by accident.
CREATE FUNCTION users_team_change_is_join_or_leave() RETURNS trigger AS $$
BEGIN
    IF OLD.team_id IS NOT NULL AND NEW.team_id IS NOT NULL
       AND NEW.team_id <> OLD.team_id THEN
        RAISE EXCEPTION 'user % is already on a team', OLD.id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;

CREATE TRIGGER users_team_switch_guard
    BEFORE UPDATE OF team_id ON users
    FOR EACH ROW EXECUTE FUNCTION users_team_change_is_join_or_leave();

-- Registration caps (num_users / num_teams / team_size). Counting and then inserting is a race: N
-- concurrent registrations all read the pre-insert count and all sail past the cap. The cap is a
-- config VALUE, so it cannot be a CHECK. Serializing the count on a transaction-scoped advisory
-- lock makes the count-then-insert atomic, which is the property we actually need.
-- The constraint name is stamped into the error so callers can map it to "registration is full"
-- rather than reading any check_violation on `users` as a cap.
CREATE FUNCTION enforce_account_caps() RETURNS trigger AS $$
DECLARE cap int; n int;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext('registration'));
    IF TG_TABLE_NAME = 'users' THEN
        -- how many users exist is an INSERT question; a team join answers only to team_size
        IF TG_OP = 'INSERT' THEN
            SELECT NULLIF(value,'')::int INTO cap FROM config WHERE key = 'num_users';
            IF cap IS NOT NULL AND cap > 0 THEN
                SELECT count(*) INTO n FROM users WHERE banned = false AND hidden = false;
                IF n >= cap THEN RAISE EXCEPTION 'num_users cap (%) reached', cap
                    USING ERRCODE = 'check_violation', CONSTRAINT = TG_NAME; END IF;
            END IF;
        END IF;
        IF NEW.team_id IS NOT NULL THEN
            SELECT NULLIF(value,'')::int INTO cap FROM config WHERE key = 'team_size';
            IF cap IS NOT NULL AND cap > 0 THEN
                SELECT count(*) INTO n FROM users WHERE team_id = NEW.team_id;
                IF n >= cap THEN RAISE EXCEPTION 'team_size cap (%) reached', cap
                    USING ERRCODE = 'check_violation', CONSTRAINT = TG_NAME; END IF;
            END IF;
        END IF;
    ELSE
        SELECT NULLIF(value,'')::int INTO cap FROM config WHERE key = 'num_teams';
        IF cap IS NOT NULL AND cap > 0 THEN
            SELECT count(*) INTO n FROM teams WHERE banned = false AND hidden = false;
            IF n >= cap THEN RAISE EXCEPTION 'num_teams cap (%) reached', cap
                USING ERRCODE = 'check_violation', CONSTRAINT = TG_NAME; END IF;
        END IF;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;

CREATE TRIGGER users_caps  BEFORE INSERT OR UPDATE OF team_id ON users
    FOR EACH ROW EXECUTE FUNCTION enforce_account_caps();
CREATE TRIGGER teams_caps  BEFORE INSERT ON teams
    FOR EACH ROW EXECUTE FUNCTION enforce_account_caps();
-- The importer runs under `session_replication_role = replica`, which disables these triggers.
-- That is correct: a restore reproduces a past state, it does not register accounts. The audit
-- triggers get the same exemption for the same reason.
```

```sql
-- Custom registration fields. Kept in v1 because registration is gated on "all required fields
-- filled", which makes the field definitions queryable data, not a config blob.
CREATE TABLE fields (
    id          bigserial PRIMARY KEY,
    name        text NOT NULL,
    applies_to  text NOT NULL CHECK (applies_to IN ('user','team')),
    field_type  text NOT NULL CHECK (field_type IN ('text','boolean')),
    description text,
    required    boolean NOT NULL DEFAULT false,
    public      boolean NOT NULL DEFAULT false,
    editable    boolean NOT NULL DEFAULT false,
    position    int     NOT NULL DEFAULT 0
);

CREATE TABLE field_entries (
    id       bigserial PRIMARY KEY,
    field_id bigint NOT NULL REFERENCES fields(id) ON DELETE CASCADE,
    user_id  bigint REFERENCES users(id) ON DELETE CASCADE,
    team_id  bigint REFERENCES teams(id) ON DELETE CASCADE,
    value    jsonb,
    CONSTRAINT field_entries_one_owner CHECK (num_nonnulls(user_id, team_id) = 1),
    UNIQUE (field_id, user_id),
    UNIQUE (field_id, team_id)
);
-- `value` is jsonb, not text. Imported archives frequently carry this column double-encoded (a JSON
-- string containing JSON); the importer unwraps it once, at the boundary, and it never happens again.

-- Only the hash of a token is stored, and the plaintext is shown exactly once, at creation. A token
-- is a credential; a credential the server can read back is a credential the server can leak.
CREATE TABLE api_tokens (
    id          bigserial   PRIMARY KEY,
    user_id     bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  bytea       NOT NULL UNIQUE,      -- sha256(token). Never the plaintext.
    description text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL
);

CREATE TABLE tracking (
    id      bigserial   PRIMARY KEY,
    user_id bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    ip      inet        NOT NULL,                 -- a real inet, not a string
    date    timestamptz NOT NULL DEFAULT now(),
    -- With this constraint the write is a real `ON CONFLICT (user_id, ip) DO UPDATE SET date = now()`
    -- upsert. Without it, it is a SELECT-then-INSERT on every authenticated request — the highest-
    -- frequency check-then-insert in the product, and the one whose failure mode (an IntegrityError
    -- on a shared session) is the easiest to mistake for an auth bug.
    UNIQUE (user_id, ip)
);
```

### 2.3 Files (the exactly-one-owner decision)

```sql
CREATE TABLE files (
    id           bigserial   PRIMARY KEY,
    location     text        NOT NULL,            -- content-addressed; carries no human name
    name         text        NOT NULL DEFAULT '', -- the original upload filename, for Content-Disposition
    sha256sum    bytea       NOT NULL,
    size_bytes   bigint      NOT NULL,
    challenge_id bigint      REFERENCES challenges(id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),

    -- ONE TABLE, NOT THREE. Ownership is a link; the blob's identity (location, hash, size) belongs
    -- to the blob. Concretely:
    --   (a) UNIQUE(location) below must hold across ALL files — split the table per owner type and
    --       it becomes unenforceable;
    --   (b) challenge_instances.artifact_id needs a single FK target;
    --   (c) `<= 1`, not `= 1`: an ownerless media-library file is legitimate.
    -- When pages and solutions land: add page_id/solution_id and widen to
    --   CHECK (num_nonnulls(challenge_id, page_id, solution_id) <= 1)
    CONSTRAINT files_at_most_one_owner CHECK (num_nonnulls(challenge_id) <= 1)
);

-- Two concurrent uploads that resolve to the same path both find nothing and both insert. The
-- unique index is the only thing that can decide which one wins.
CREATE UNIQUE INDEX files_location_uniq ON files (location);
```

### 2.4 Challenges, flags, hints

```sql
CREATE TABLE challenges (
    id              bigserial   PRIMARY KEY,
    name            text        NOT NULL,
    category        text        NOT NULL,
    description     text        NOT NULL DEFAULT '',
    attribution     text,
    connection_info text,

    -- No plugin system: challenge types are in-tree interfaces, so the set is closed and the CHECK
    -- can say so.
    type            text NOT NULL DEFAULT 'standard' CHECK (type IN ('standard','dynamic')),
    state           text NOT NULL DEFAULT 'visible'  CHECK (state IN ('visible','hidden')),

    -- The CURRENT ASKING PRICE, and nothing more. It is not what past solves are worth — solves.value
    -- is (§2.6). Summing a live challenges.value in the standings query is what makes decay
    -- retroactively revalue history and erase the first solver's advantage.
    value           int NOT NULL,

    -- Dynamic scoring lives HERE, in one store. It is four columns and a CHECK, not a subtype.
    function        text NOT NULL DEFAULT 'static'
                        CHECK (function IN ('static','linear','logarithmic')),
    initial         int,
    minimum         int,
    decay           int,
    CONSTRAINT challenges_dynamic_params CHECK (
        function = 'static'
        OR (initial IS NOT NULL AND minimum IS NOT NULL AND decay IS NOT NULL
            AND decay > 0 AND minimum >= 0 AND initial >= minimum)
    ),
    -- decay > 0 is deliberate: the decay curve divides by decay², so decay = 0 is not a challenge
    -- configuration, it is a division by zero. Reject it at the boundary rather than coercing it to
    -- some other number the admin did not ask for.

    max_attempts    int  NOT NULL DEFAULT 0 CHECK (max_attempts >= 0),
    -- Every enum column declares its default and closes its set. An unconstrained text column with
    -- a permissive dispatcher means an empty string silently behaves like 'any'.
    logic           text NOT NULL DEFAULT 'any' CHECK (logic IN ('any','all')),
    position        int  NOT NULL DEFAULT 0,
    next_id         bigint REFERENCES challenges(id) ON DELETE SET NULL,
    requirements    jsonb NOT NULL DEFAULT '{}'::jsonb,   -- {prerequisites:[id], anonymize:bool|"preview"}

    -- Two orthogonal opt-ins. flag_mode = how flags are ISSUED; flags.type = how they are COMPARED.
    -- A regex flag cannot be pool-issued (§5).
    flag_mode         text NOT NULL DEFAULT 'static' CHECK (flag_mode IN ('static','unique')),
    first_blood       text NOT NULL DEFAULT 'none'   CHECK (first_blood IN ('none','announce','bonus')),
    first_blood_bonus int,
    CONSTRAINT challenges_fb_bonus CHECK ((first_blood = 'bonus') = (first_blood_bonus IS NOT NULL)),

    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE tags (
    id           bigserial PRIMARY KEY,
    challenge_id bigint NOT NULL REFERENCES challenges(id) ON DELETE CASCADE,
    value        text   NOT NULL,
    UNIQUE (challenge_id, value)   -- a tag is set membership; a set holds an element once
);

CREATE TABLE flags (
    id               bigserial PRIMARY KEY,
    challenge_id     bigint NOT NULL REFERENCES challenges(id) ON DELETE CASCADE,
    -- static + regex, closed set. `type` picks a compare function; it does not pick a table.
    type             text NOT NULL CHECK (type IN ('static','regex')),
    content          text NOT NULL,
    -- A boolean, stored as a boolean. Case-insensitivity is a property of the comparison, not a
    -- magic string smuggled through a free-text column.
    case_insensitive boolean NOT NULL DEFAULT false
);
-- Not a DB constraint, but part of the contract: regex matching CANNOT be made timing-safe. `static`
-- and `unique` flags compare with subtle.ConstantTimeCompare; a regex flag leaks match progress
-- through timing. Documented, accepted, and the reason regex is not allowed on a unique-flag
-- challenge (§5).

CREATE TABLE hints (
    id           bigserial PRIMARY KEY,
    challenge_id bigint NOT NULL REFERENCES challenges(id) ON DELETE CASCADE,
    title        text,
    content      text   NOT NULL,
    cost         int    NOT NULL DEFAULT 0 CHECK (cost >= 0),
    requirements jsonb  NOT NULL DEFAULT '{}'::jsonb,   -- {prerequisites:[hint_id]}
    position     int    NOT NULL DEFAULT 0
);
```

### 2.5 Unique flags

See [Unique flags](../TARGET-FEATURES.md#unique-flags) for the feature. A pool entry is not a flag,
it is a **bundle**: a flag, an optional artifact, and per-account variables the challenge description
(a Go `text/template`) renders against. Hence the table is `challenge_instances`, and an issue points
at an instance.

```sql
CREATE TABLE challenge_instances (
    id           bigserial PRIMARY KEY,
    challenge_id bigint NOT NULL REFERENCES challenges(id) ON DELETE CASCADE,
    value_hash   bytea  NOT NULL,               -- sha256(flag). The plaintext is never stored.
    artifact_id  bigint REFERENCES files(id) ON DELETE SET NULL,  -- the per-account binary/VM/PDF
    vars         jsonb  NOT NULL DEFAULT '{}'::jsonb,             -- {"host":"x.ctf","port":31001}
                                                                  -- never put the flag in here: the
                                                                  -- view type has no Flag field, by
                                                                  -- construction.
    generation   int    NOT NULL DEFAULT 1,     -- bump on re-upload; old issues stay attributable
    UNIQUE (challenge_id, value_hash, generation)
);

CREATE TABLE flag_issues (
    challenge_id bigint      NOT NULL REFERENCES challenges(id) ON DELETE CASCADE,
    -- users.id XOR teams.id per instance.user_mode. No FK: the target table is instance-dependent,
    -- and a conditional FK is not expressible in Postgres (§5).
    account_id   bigint      NOT NULL,
    instance_id  bigint      NOT NULL REFERENCES challenge_instances(id) ON DELETE RESTRICT,
    assigned_at  timestamptz NOT NULL DEFAULT now(),

    -- Assignment happens lazily on first access — the only place in the product where READING a
    -- challenge mutates state, and therefore the only read that races with itself. Both constraints
    -- are load-bearing:
    PRIMARY KEY (challenge_id, account_id),   -- an account cannot be issued two instances, even
                                              -- under concurrent first-views
    UNIQUE (instance_id)                      -- an instance cannot be issued to two accounts.
                                              -- THIS is the constraint that makes the anti-cheat
                                              -- property true.
);
```

### 2.6 Gameplay: submissions, solves, awards, unlocks

```sql
-- Append-only log of every attempt. `type` is a STATUS, not a class.
CREATE TABLE submissions (
    id           bigserial   PRIMARY KEY,
    challenge_id bigint      NOT NULL REFERENCES challenges(id) ON DELETE CASCADE,
    -- BOTH account columns are always written; user_mode selects which one you READ.
    user_id      bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    team_id      bigint      REFERENCES teams(id) ON DELETE CASCADE,
    type         text        NOT NULL CHECK (type IN ('correct','incorrect','partial','discard','ratelimited')),
    provided     text        NOT NULL,
    ip           inet,
    date         timestamptz NOT NULL DEFAULT now(),

    -- The account the matched flag was ISSUED to. Stamped inside the submit transaction, never
    -- joined at query time — so attribution survives flag rotation, regeneration and deletion, and
    -- sharing detection is a predicate on THIS TABLE ALONE.
    -- No FK: it holds users.id XOR teams.id per instance.user_mode, and it must outlive the
    -- deletion of the account it names (§5).
    attributed_account_id bigint
);
-- Append-only. Nothing may UPDATE `date` or rewrite a row. Admin grading changes `type` and creates
-- a solve; it does not rewrite history.

-- The scoring fact. Self-sufficient, so the standings query never joins submissions ⋈ solves.
CREATE TABLE solves (
    id            bigserial   PRIMARY KEY,
    submission_id bigint      UNIQUE REFERENCES submissions(id) ON DELETE SET NULL,  -- provenance; NULL for imports/admin grants
    -- RESTRICT, not CASCADE: deleting a challenge must not silently destroy its solves. The
    -- standings, the time-travel view and the gameplay audit trail are all sums over this table; a
    -- CASCADE would let one admin DELETE rewrite all three with no trace of what was lost. The API
    -- turns the violation into a 409 that names the problem. `submissions` RESTRICTs too: failed
    -- attempts are anticheat evidence (ip, attributed account), so a challenge with any recorded
    -- attempts can only be hidden, never deleted.
    challenge_id  bigint      NOT NULL REFERENCES challenges(id) ON DELETE RESTRICT,
    user_id       bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    team_id       bigint      REFERENCES teams(id) ON DELETE CASCADE,

    -- The per-solve snapshot. THE decision this whole schema turns on. Stamped under the challenge
    -- lock at solve time, so decay changes the asking price of FUTURE solves and never revalues a
    -- past one. It is what makes the scoreboard append-only (hence replayable to any point in time)
    -- and what makes the audit trail mean anything: an audit row saying "awarded N" is worthless if
    -- N is recomputed on read.
    value         int         NOT NULL,
    date          timestamptz NOT NULL DEFAULT now(),

    -- The idempotency primitive of the hot path. The submit path does
    -- `INSERT … ON CONFLICT DO NOTHING RETURNING id`; zero rows back means already solved. There is
    -- no SELECT, so there is no window to lose.
    UNIQUE (challenge_id, user_id),
    UNIQUE (challenge_id, team_id)
);
-- Append-only. `value` and `date` are set ONCE, at insert. Never rewritten.

CREATE TABLE awards (
    id           bigserial   PRIMARY KEY,
    user_id      bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    team_id      bigint      REFERENCES teams(id) ON DELETE CASCADE,
    -- A real category, so hint spends and first-blood bonuses are distinguishable in the ledger.
    type         text        NOT NULL DEFAULT 'standard'
                     CHECK (type IN ('standard','hint_unlock','first_blood')),
    -- challenge_id is what makes "at most one first-blood bonus per challenge" expressible at all.
    challenge_id bigint      REFERENCES challenges(id) ON DELETE CASCADE,
    name         text        NOT NULL,
    description  text,
    value        int         NOT NULL,          -- may be negative (hint penalty)
    category     text,
    icon         text,
    date         timestamptz NOT NULL DEFAULT now(),
    -- a hint spend and a first-blood bonus are ALWAYS about a challenge; a manual award need not be
    CONSTRAINT awards_typed_challenge CHECK (
        type = 'standard' OR challenge_id IS NOT NULL
    )
);
-- Prevents a double first-blood bonus even if the challenge lock is ever bypassed. A constraint,
-- not a check in Go.
CREATE UNIQUE INDEX awards_one_first_blood_per_challenge
    ON awards (challenge_id) WHERE type = 'first_blood';
-- Append-only.

CREATE TABLE hint_unlocks (
    id       bigserial   PRIMARY KEY,
    hint_id  bigint      NOT NULL REFERENCES hints(id) ON DELETE CASCADE,
    user_id  bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    team_id  bigint      REFERENCES teams(id) ON DELETE CASCADE,
    award_id bigint      NOT NULL UNIQUE REFERENCES awards(id) ON DELETE RESTRICT,  -- exactly one charge
    date     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (hint_id, user_id),
    UNIQUE (hint_id, team_id)
);
-- The uniques kill the double-INSERT. They do NOT kill the other half of the race — the balance
-- check — because affordability is a question about a SUM, and a SUM is not a constraint. That half
-- is a transaction contract, and it is not optional:
--   BEGIN
--     SELECT 1 FROM <teams|users> WHERE id = $account FOR UPDATE   -- serialize this account's spends
--     score := SUM(solves.value) + SUM(awards.value)               -- computed here, not cached
--     if score < hint.cost: ROLLBACK → 400
--     INSERT awards(type='hint_unlock', value=-cost, challenge_id=hint.challenge_id) RETURNING id
--     INSERT hint_unlocks(...) ON CONFLICT DO NOTHING RETURNING id -- no row ⇒ already unlocked
--     if no row: ROLLBACK → 400 (the award is discarded with the tx; no orphan)
--   COMMIT
-- Read the balance outside the lock — or from a cache — and N concurrent unlocks all see the
-- pre-spend balance, all pass the affordability check, and the account goes NEGATIVE.
```

**First blood is derived, not stored.** There is no `first_blood` table. First blood is *the solve
with `MIN(date, id)` for a challenge, excluding hidden and banned accounts* — a derived fact that
cannot be stale, cannot be double-announced, and self-corrects when an account is later banned
(consistent with how hidden and banned accounts are already excluded from the decay count). A
materialized row would be a second source of truth that goes wrong the moment an admin bans an
account, and the solves table is append-only, so there would be nothing to rewrite it from. The
**bonus award** *is* materialized — it is a fact about a payment, and payments are stamped — guarded
by `awards_one_first_blood_per_challenge`. See
[First blood](../TARGET-FEATURES.md#first-blood).

### 2.7 Content & platform

```sql
CREATE TABLE notifications (
    id      bigserial   PRIMARY KEY,
    title   text        NOT NULL,
    content text        NOT NULL,
    date    timestamptz NOT NULL DEFAULT now()
);
-- No user_id/team_id. Every notification is a broadcast; targeting columns that no create path sets
-- are not a feature, they are a lie in the schema.

-- `tasks` owns the STATE; River owns the EXECUTION. The state of a database restore belongs in the
-- database — put it in a cache and an eviction loses the status of a running restore.
CREATE TABLE tasks (
    id         bigserial   PRIMARY KEY,
    kind       text        NOT NULL CHECK (kind IN ('import','export')),
    state      text        NOT NULL CHECK (state IN ('queued','running','succeeded','failed','cancelled')),
    progress   int         NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
    detail     text,                          -- "restoring submissions"
    error      text,
    created_by bigint      REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
-- The singleton lives in the DATABASE, not in a worker-pool setting like `MaxWorkers: 1` — that is
-- per-client, so three replicas would give you three concurrent imports, i.e. three concurrent
-- database restores.
CREATE UNIQUE INDEX tasks_one_in_flight ON tasks (kind) WHERE state IN ('queued','running');
-- Implementation trap: progress must be written on a SECOND connection. The restore is one
-- transaction (so that it can really roll back), and progress UPDATEs inside it are invisible until
-- it commits.
```

### 2.8 Audit

The trail behind [Audit trail](../TARGET-FEATURES.md#audit-trail): every admin mutation, captured by
the database itself, so no write path can forget to log.

```sql
CREATE TABLE audit_log (
    id           bigserial   PRIMARY KEY,
    actor_id     bigint,                        -- NULL = system/migration/console. No FK: the row must
                                                -- survive the actor's deletion (that's the point).
    action       text        NOT NULL CHECK (action IN ('INSERT','UPDATE','DELETE')),
    target_table text        NOT NULL,
    target_id    bigint,
    before       jsonb,
    after        jsonb,
    at           timestamptz NOT NULL DEFAULT now(),
    ip           inet
);

-- Secrets are stripped from the diff. The log is admin-readable, and a password hash in a JSONB
-- `before` is an offline-cracking target that the audit trail has no use for.
CREATE FUNCTION audit_capture() RETURNS trigger AS $$
DECLARE
    masked constant text[] := ARRAY['password_hash', 'secret'];
BEGIN
    INSERT INTO audit_log (actor_id, action, target_table, target_id, before, after, ip)
    VALUES (
        nullif(current_setting('app.actor_id', true), '')::bigint,
        TG_OP, TG_TABLE_NAME,
        COALESCE(NEW.id, OLD.id),
        CASE WHEN TG_OP IN ('UPDATE','DELETE') THEN to_jsonb(OLD) - masked END,
        CASE WHEN TG_OP IN ('INSERT','UPDATE') THEN to_jsonb(NEW) - masked END,
        nullif(current_setting('app.ip', true), '')::inet
    );
    RETURN NULL;   -- AFTER trigger
END $$ LANGUAGE plpgsql;

-- Rule 1: admin-mutable tables ONLY.
-- Rule 2: NEVER `submissions` or `solves`. They are already immutable gameplay facts; a trigger
--         there would DOUBLE the write volume on the hottest path in the product for zero
--         information.
-- Rule 3: the importer runs under `session_replication_role = replica`, which disables triggers —
--         so a million-row restore emits zero audit rows. Deliberate, not an accident to "fix": the
--         audit trail records what an admin did, and what an admin did was "import this archive".
CREATE TRIGGER audit_challenges   AFTER INSERT OR UPDATE OR DELETE ON challenges
    FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_flags        AFTER INSERT OR UPDATE OR DELETE ON flags        FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_instances    AFTER INSERT OR UPDATE OR DELETE ON challenge_instances FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_hints        AFTER INSERT OR UPDATE OR DELETE ON hints        FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_tags         AFTER INSERT OR UPDATE OR DELETE ON tags         FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_users        AFTER INSERT OR UPDATE OR DELETE ON users        FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_teams        AFTER INSERT OR UPDATE OR DELETE ON teams        FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_config       AFTER INSERT OR UPDATE OR DELETE ON config       FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_awards       AFTER INSERT OR UPDATE OR DELETE ON awards       FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_notifications AFTER INSERT OR UPDATE OR DELETE ON notifications FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_files        AFTER INSERT OR UPDATE OR DELETE ON files        FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_brackets     AFTER INSERT OR UPDATE OR DELETE ON brackets     FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_fields       AFTER INSERT OR UPDATE OR DELETE ON fields       FOR EACH ROW EXECUTE FUNCTION audit_capture();
-- Non-goal (chosen, not overlooked): tamper-evidence. No hash chain, no revoked DELETE grant. The
-- threat model is "an admin changed something and nobody remembers who", not "an admin with
-- database access covered their tracks".
```

### 2.9 Jobs

River's `river_job`, `river_leader`, `river_queue` and `river_client*` tables are created by River's
own migrator, run behind the same advisory lock as ours. We define none of them and we read none of
them: `tasks` (§2.7) is the API surface. The one thing we *do* rely on is that River's enqueue takes
a transaction — an announcement enqueued in the same transaction as the solve cannot outlive a
rollback of that solve. See [the submit hot path](05-hotpath.md).

---

## 3. Every race, and the constraint that kills it

**Every `SELECT`-then-`INSERT` is a race.** Two requests both look, both find nothing, both write.
No amount of care in the application layer closes that window, because the window is *between* two
statements and the application does not own the interleaving. The database does.

So the rule is: if the database can express the invariant, the database enforces it — and the
application's job shrinks to *translating the violation into an HTTP status*. What follows is the
complete list of places in this product where that rule is load-bearing.

| Race | The invariant | The constraint that enforces it | Where |
|---|---|---|---|
| **Duplicate solve** — N concurrent correct submissions for one account | one solve per (challenge, account) | `UNIQUE (challenge_id, user_id)`, `UNIQUE (challenge_id, team_id)`; the submit path inserts with `ON CONFLICT DO NOTHING RETURNING id` and reads zero rows as *already solved*. Never a SELECT. | §2.6 |
| **Double-unlock / negative score** — N concurrent POSTs to buy one hint | a hint is bought once, charged once, and a balance never goes below zero | `UNIQUE (hint_id, user_id)`, `UNIQUE (hint_id, team_id)`, `award_id NOT NULL UNIQUE` kill the double-insert and the double-charge. The *negative balance* is not a constraint — it is a SUM — so it is a transaction contract: `FOR UPDATE` the account row, compute the score exactly, in-tx. Both halves are required. | §2.6 |
| **Double first-blood bonus** — two solvers land in the same instant | at most one first-blood bonus per challenge | `awards_one_first_blood_per_challenge` partial unique index. Belt and braces: the correct-path challenge lock already serializes this, and the index makes it true anyway. | §2.6 |
| **Unique-flag double-issue** — concurrent first-views of the same challenge | an account holds at most one instance; an instance is held by at most one account | `PRIMARY KEY (challenge_id, account_id)` + `UNIQUE (instance_id)` on `flag_issues`. The second constraint *is* the anti-cheat property; without it a leaked flag no longer names one account. | §2.5 |
| **Cap bypass under concurrent registration** — `num_users`, `num_teams`, `team_size` | the count of live accounts never exceeds the configured cap | The cap is a config value, so it cannot be a `CHECK`. `enforce_account_caps()` takes `pg_advisory_xact_lock(hashtext('registration'))` before counting, which makes count-then-insert atomic. | §2.2 |
| **Team-name collision** — two teams created, or joined, under one name | a team name identifies exactly one team | `teams_name_uniq`. Enrollment looks a team up *by name*; without the index the lookup is ambiguous the moment the create race is lost. | §2.2 |
| **Silent team switch** — a user moves from team A to team B | `team_id` goes NULL→set or set→NULL, never A→B | `users_team_switch_guard`. Solves stamp `team_id` at solve time, so a direct switch lets one user feed two teams' scores. | §2.2 |
| **File-location double-insert** — concurrent uploads resolving to the same path | one row per stored location | `files_location_uniq`. This is why files are one table: the uniqueness must hold across *all* files. | §2.3 |
| **Config duplicate key** — concurrent writers create the same setting | one row per key | `config_key_uniq`. Config is read by key; a duplicate makes the read nondeterministic, and nondeterministic config is a bug that reproduces on one replica out of three. | §2.1 |
| **Tracking double-insert** — concurrent requests from a new IP | one row per (user, ip) | `UNIQUE (user_id, ip)`, which turns the write into `ON CONFLICT (user_id, ip) DO UPDATE SET date = now()`. Highest-frequency check-then-insert in the product; without the constraint its failure surfaces as a broken session. | §2.2 |
| **Two concurrent imports** — two admins hit restore | at most one import (or export) in flight, cluster-wide | `tasks_one_in_flight`, a partial unique index on `kind` for the in-flight states. A per-process worker limit cannot say this; the database can. | §2.7 |
| **Decay last-writer-wins** — N concurrent solvers recompute the value | `challenges.value` = f(solve count), not f(whoever committed last) | The correct path holds `SELECT … FOR NO KEY UPDATE` on the challenge row, and the recalc is one statement (`UPDATE … FROM (SELECT COUNT(*))`), never a read-modify-write. | [05-hotpath](05-hotpath.md) |
| **Rate-limit counter** — concurrent submissions from one account | the per-minute count is exact | No external counter store, so nothing to be non-atomic: the limit is `COUNT(*) FROM submissions WHERE … AND date > now() - interval` over `submissions_ratelimit_idx`. Atomic by construction, because it is one statement. | §4 |

---

## 4. Indexes, each tied to a real query

PK/UNIQUE constraints already index `solves(challenge_id, user_id)`, `solves(challenge_id, team_id)`,
`flag_issues(challenge_id, account_id)`,
`challenge_instances(challenge_id, value_hash, generation)`, `files(location)`, `config(key)`,
`users(lower(email))`, `teams(name)`, `hint_unlocks(hint_id, user_id|team_id)`, `tracking(user_id, ip)`
and `api_tokens(token_hash)`. Nothing below duplicates those.

```sql
-- === Standings, and the replay of standings to a past instant ===
-- The standings query is a UNION ALL of two grouped selects: group solves by account with
-- `date < freeze`, group awards by account with `date < freeze`, re-group, order. Replaying the
-- board to an arbitrary instant substitutes `date < @as_of` — the same shape, the same indexes,
-- which is exactly why the append-only design pays off. These four are THE scoreboard.
CREATE INDEX solves_team_scoreboard_idx  ON solves (team_id, date) INCLUDE (value, id);
CREATE INDEX solves_user_scoreboard_idx  ON solves (user_id, date) INCLUDE (value, id);
CREATE INDEX awards_team_scoreboard_idx  ON awards (team_id, date) INCLUDE (value, id) WHERE value <> 0;
CREATE INDEX awards_user_scoreboard_idx  ON awards (user_id, date) INCLUDE (value, id) WHERE value <> 0;
-- `WHERE value <> 0` mirrors the standings query's own predicate on awards. It is NOT applied to
-- solves: a zero-value solve is still a solve (it counts for first blood and for the challenge's
-- solve count), so excluding it would be wrong for the queries that share the index.

-- === Submit hot path ===
-- The prior-solve count under the challenge lock and the decay recalc's count are both
-- `WHERE challenge_id = $1` joined to the account table for hidden/banned. Served by the leading
-- column of the solves uniques — no extra index.
-- First blood (derived): `ORDER BY date, id LIMIT 1` per challenge.
CREATE INDEX solves_challenge_firstblood_idx ON solves (challenge_id, date, id);

-- max_attempts and the per-minute rate limiter, both scoped to (challenge, account) over a recent
-- window.
CREATE INDEX submissions_ratelimit_idx ON submissions (user_id, challenge_id, date DESC);
CREATE INDEX submissions_team_attempts_idx ON submissions (team_id, challenge_id, date DESC);

-- A challenge's flags are loaded on every submit. This is the hottest read in the product.
CREATE INDEX flags_challenge_idx ON flags (challenge_id);

-- Unique-flag compare path: sha256(provided) → one indexed probe. O(1), not O(N_accounts), and no
-- per-byte timing channel. Served by the challenge_instances unique's (challenge_id, value_hash)
-- prefix — no extra index.

-- === Challenge board ===
CREATE INDEX challenges_board_idx ON challenges (state, category, position);   -- the visible board, ordered
CREATE INDEX hints_challenge_idx  ON hints (challenge_id, position);
CREATE INDEX tags_challenge_idx   ON tags (challenge_id);
CREATE INDEX files_challenge_idx  ON files (challenge_id) WHERE challenge_id IS NOT NULL;

-- Per-account solve state for the board ("which have I solved?") — the second-hottest read.
-- Served by the solves uniques' leading column? NO: those lead with challenge_id. Need the reverse.
CREATE INDEX solves_by_team_idx ON solves (team_id, challenge_id);
CREATE INDEX solves_by_user_idx ON solves (user_id, challenge_id);

-- === Anti-cheat ===
-- Flag sharing: a predicate on `submissions` alone, no join, because attribution is stamped there.
-- Partial: only correct, attributed submissions are ever scanned — a tiny fraction of the table.
CREATE INDEX submissions_sharing_idx ON submissions (attributed_account_id, date DESC)
    WHERE type = 'correct' AND attributed_account_id IS NOT NULL;

-- Shared IP across unrelated accounts. The IP is captured on the submission itself, so this is a
-- question about gameplay, not about sessions.
CREATE INDEX submissions_ip_idx ON submissions (ip, user_id) WHERE ip IS NOT NULL;
CREATE INDEX tracking_ip_idx    ON tracking (ip);

-- "Solved without ever fetching the artifact": a correct submission on a flag_mode='unique'
-- challenge with NO flag_issues row. Provable, not statistical. An anti-join over
-- solves_challenge_firstblood_idx × the flag_issues PK. No new index.

-- === Admin lists ===
CREATE INDEX submissions_admin_list_idx ON submissions (date DESC);            -- default admin ordering
CREATE INDEX submissions_admin_type_idx ON submissions (type, date DESC);      -- the `type` facet
CREATE INDEX awards_admin_list_idx      ON awards (date DESC);
CREATE INDEX users_team_idx             ON users (team_id) WHERE team_id IS NOT NULL;  -- team roster + team_size cap
CREATE INDEX users_bracket_idx          ON users (bracket_id) WHERE bracket_id IS NOT NULL;
CREATE INDEX teams_bracket_idx          ON teams (bracket_id) WHERE bracket_id IS NOT NULL;
CREATE INDEX notifications_date_idx     ON notifications (date DESC);
CREATE INDEX tracking_user_recent_idx   ON tracking (user_id, date DESC);      -- "recent IPs" on the user page
CREATE INDEX api_tokens_user_idx        ON api_tokens (user_id);
CREATE INDEX api_tokens_expiry_idx      ON api_tokens (expires_at);            -- expiry sweep
CREATE INDEX field_entries_user_idx     ON field_entries (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX field_entries_team_idx     ON field_entries (team_id) WHERE team_id IS NOT NULL;

-- === Audit ===
CREATE INDEX audit_log_target_idx ON audit_log (target_table, target_id, at DESC);  -- "history of this challenge"
CREATE INDEX audit_log_actor_idx  ON audit_log (actor_id, at DESC);                 -- "what did alice do"
CREATE INDEX audit_log_at_idx     ON audit_log (at DESC);                           -- the global feed

-- === Instance-pool gauge: issued/total per challenge ===
-- The one number that must be right BEFORE the event starts. Running out of instances mid-CTF is a
-- hard failure by design — we would rather refuse than issue a duplicate.
CREATE INDEX challenge_instances_challenge_idx ON challenge_instances (challenge_id);
```

**Deliberately absent.** No index on `submissions.provided`: it is never searched, because the
unique-flag path hashes and probes `challenge_instances` instead. No trigram or GIN index for the
admin free-text search: those are admin screens over a few thousand rows, and a sequential scan is
the right price for not carrying a second index structure through every write.

---

## 5. What the database cannot enforce, and what we do instead

Three invariants in this design are not expressible as constraints. Each is stated here so that
nobody has to rediscover it from a comment.

**`account_id` has no foreign key, and must not have one.**
`flag_issues.account_id` and `submissions.attributed_account_id` hold a `users.id` **XOR** a
`teams.id`, depending on `instance.user_mode`. A conditional FK is not expressible in Postgres, and
the alternative — two nullable columns plus a `CHECK` — buys referential integrity we do not want.
Attribution is a **stamped fact**: it records that, at that instant, the flag submitted was the one
issued to that account. A `CASCADE` that erased the evidence when an admin deleted a suspicious team
would be actively wrong, and so would a `RESTRICT` that made the team undeletable. The dangling id is
the correct outcome, and it is the same argument as `audit_log.actor_id`, which also outlives its
actor by design. The anti-cheat queries tolerate ids that no longer resolve.

**`flag_mode='unique'` with a regex flag is incoherent, and the predicate spans two tables.**
A pool instance is a concrete string baked into an artifact; a regex is not a string. The rule is
therefore *a challenge with `flag_mode='unique'` has no regex flags*, and it spans `challenges` and
`flags`, so it cannot be a `CHECK`. It is enforced in the service layer, at the write boundary — the
one invariant in this schema that lives in Go rather than in the database. It is called out rather
than hidden, because the whole point of this design is that the *other* invariants do not.

**Affordability is a SUM, and a SUM is not a constraint.**
"Score never goes negative" cannot be a `CHECK` — the score is an aggregate over two tables. It is a
transaction contract instead: lock the account row, compute the balance in-transaction, then charge.
It is written out in §2.6 and pinned by the concurrency suite, because it is the one invariant here
that a future refactor could quietly break without failing a constraint.

### The standings tiebreak

Standings order by `score DESC, last_event ASC, account_id ASC`.

- `score DESC` — the obvious one.
- `last_event ASC` — the account that reached its score *earlier* ranks higher. This is the fairness
  clause: getting there first is worth something, and it is what makes a frozen board and a live
  board agree.
- `account_id ASC` — determinism insurance. It only fires when two accounts have an identical score
  **and** an identical `last_event` down to the microsecond, which in practice means a tie between
  two accounts whose last scoring events landed in the same instant. There is no fair answer to that
  question; there is only a *stable* one. Without a total order, the same board can render in two
  different orders on two replicas, and a scoreboard that flickers is worse than one that is
  arbitrary in the last digit.

A first-blood bonus counts as a scoring event, so it moves the account's `last_event` forward — a
bonus can therefore cost a place in an exact tie. That is the correct reading: the bonus is points,
and points arrive at a time.
