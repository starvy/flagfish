-- Gameplay group: submissions, solves, awards, hint_unlocks.
-- These four tables are append-only and must never carry the audit trigger: they are the
-- gameplay audit trail, and a trigger would double the write volume on the hot path.

-- +goose Up

-- ── submissions: the append-only attempt log ─────────────────────────────────────
-- `type` is a status, not a subclass — the attempt statuses share one table.
CREATE TABLE submissions (
    id           bigserial   PRIMARY KEY,
    challenge_id bigint      NOT NULL REFERENCES challenges(id) ON DELETE CASCADE,
    -- Both are always written; instance.user_mode selects which one you read.
    -- user_id is NOT NULL: the importer must reject or repair rows missing it rather than relaxing this.
    user_id      bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    team_id      bigint      REFERENCES teams(id) ON DELETE CASCADE,
    type         text        NOT NULL CHECK (type IN ('correct','incorrect','partial','discard','ratelimited')),
    provided     text        NOT NULL,
    ip           inet,                        -- recorded but not yet used by any detector
    date         timestamptz NOT NULL DEFAULT now(),

    -- The account the matched flag was issued to. Stamped inside the submit transaction, never
    -- joined at query time — so attribution survives instance rotation or deletion and sharing
    -- detection is a predicate on this table alone.
    -- No FK: it holds users.id XOR teams.id per instance.user_mode, and attribution is a stamped
    -- fact that should outlive the account's deletion.
    attributed_account_id bigint
);
-- max_attempts and the kpm rate limiter, both scoped to (challenge, account) over a recent window.
-- No Redis: the counter is a COUNT(*) over this index, atomic by construction rather than a cache
-- increment that only works on one backend.
CREATE INDEX submissions_ratelimit_idx     ON submissions (user_id, challenge_id, date DESC);
CREATE INDEX submissions_team_attempts_idx ON submissions (team_id, challenge_id, date DESC);
CREATE INDEX submissions_admin_list_idx    ON submissions (date DESC);        -- default admin ordering
CREATE INDEX submissions_admin_type_idx    ON submissions (type, date DESC);  -- the `type` facet
-- Sharing signal: only correct, attributed submissions are ever scanned.
CREATE INDEX submissions_sharing_idx ON submissions (attributed_account_id, date DESC)
    WHERE type = 'correct' AND attributed_account_id IS NOT NULL;
-- Shared-IP signal across unrelated accounts.
CREATE INDEX submissions_ip_idx ON submissions (ip, user_id) WHERE ip IS NOT NULL;

-- ── solves: the self-sufficient scoring fact ─────────────────────────────────────
-- One self-sufficient table, not a subclass of submissions that makes every scoring query join.
CREATE TABLE solves (
    id            bigserial   PRIMARY KEY,
    -- provenance; NULL for imports/admin grants. ON DELETE SET NULL, never CASCADE: deleting an
    -- attempt must not silently retract points.
    submission_id bigint      UNIQUE REFERENCES submissions(id) ON DELETE SET NULL,
    challenge_id  bigint      NOT NULL REFERENCES challenges(id) ON DELETE CASCADE,
    user_id       bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    team_id       bigint      REFERENCES teams(id) ON DELETE CASCADE,

    -- The per-solve snapshot, the decision this schema turns on. Summing the live challenges.value
    -- for standings would let decay retroactively revalue every past solve, so the first solver
    -- keeps no advantage. Stamped under the challenge lock; makes the scoreboard append-only.
    value         int         NOT NULL,
    date          timestamptz NOT NULL DEFAULT now(),

    -- The idempotency primitive for the hot path: a unique constraint backing the check-then-insert.
    -- `INSERT … ON CONFLICT DO NOTHING RETURNING id` returns zero rows when already solved.
    UNIQUE (challenge_id, user_id),
    UNIQUE (challenge_id, team_id)
);
-- Append-only. `value` and `date` are set once, at insert. Never rewritten.

-- Standings and scoreboard time-travel: group by account with `date < @as_of`.
CREATE INDEX solves_team_scoreboard_idx ON solves (team_id, date) INCLUDE (value, id);
CREATE INDEX solves_user_scoreboard_idx ON solves (user_id, date) INCLUDE (value, id);
-- First blood (derived, never stored): ORDER BY date, id LIMIT 1 per challenge.
CREATE INDEX solves_challenge_firstblood_idx ON solves (challenge_id, date, id);
-- "Which challenges have I solved?" — the board's second-hottest read. The uniques above lead with
-- challenge_id; this is the reverse.
CREATE INDEX solves_by_team_idx ON solves (team_id, challenge_id);
CREATE INDEX solves_by_user_idx ON solves (user_id, challenge_id);

-- ── awards ──────────────────────────────────────────────────────────────────────
CREATE TABLE awards (
    id           bigserial   PRIMARY KEY,
    -- Both user_id and team_id are always written; they are not either/or.
    user_id      bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    team_id      bigint      REFERENCES teams(id) ON DELETE CASCADE,
    -- A real category so hint spends and first-blood bonuses are distinguishable in the ledger.
    type         text        NOT NULL DEFAULT 'standard'
                     CHECK (type IN ('standard','hint_unlock','first_blood')),
    -- Without challenge_id, "at most one first-blood bonus per challenge" is not expressible.
    challenge_id bigint      REFERENCES challenges(id) ON DELETE CASCADE,
    name         text        NOT NULL,
    description  text,
    value        int         NOT NULL,   -- may be negative (hint penalty)
    category     text,
    icon         text,
    date         timestamptz NOT NULL DEFAULT now(),
    -- a hint spend and a first-blood bonus are always about a challenge; a manual award need not be
    CONSTRAINT awards_typed_challenge CHECK (type = 'standard' OR challenge_id IS NOT NULL)
);
-- Prevents a second bonus if the challenge lock is ever bypassed (admin grant, import, psql).
-- A constraint, not a check.
CREATE UNIQUE INDEX awards_one_first_blood_per_challenge
    ON awards (challenge_id) WHERE type = 'first_blood';
-- `WHERE value <> 0` mirrors the scoreboard predicate exactly. Not applied to solves: a zero-value
-- solve is still a solve (it counts for first blood and the solve count).
CREATE INDEX awards_team_scoreboard_idx ON awards (team_id, date) INCLUDE (value, id) WHERE value <> 0;
CREATE INDEX awards_user_scoreboard_idx ON awards (user_id, date) INCLUDE (value, id) WHERE value <> 0;
CREATE INDEX awards_admin_list_idx      ON awards (date DESC);

-- ── hint_unlocks ────────────────────────────────────────────────────────────────
-- Without a unique constraint, concurrent POSTs double-insert the unlock and the negative award,
-- and an affordability check against a memoized score lets N concurrent unlocks all see the
-- pre-spend balance, so the score can go negative.
--
-- The constraints below kill the double-insert. They do not fix the negative-score half — that is a
-- transaction contract:
--   BEGIN
--     SELECT 1 FROM <teams|users> WHERE id = $account FOR NO KEY UPDATE  -- serialize this account's
--                                                                        -- spends (see note below)
--     score := SUM(solves.value) + SUM(awards.value)               -- exact, not memoized
--     if score < hint.cost: ROLLBACK → 400
--     INSERT awards(type='hint_unlock', value=-cost, challenge_id=hint.challenge_id) RETURNING id
--     INSERT hint_unlocks(...) ON CONFLICT DO NOTHING RETURNING id -- no row ⇒ already unlocked
--     if no row: ROLLBACK → 400 (the award is discarded with the tx; no orphan)
--   COMMIT
--
-- FOR NO KEY UPDATE, corrected during implementation — this contract originally said FOR UPDATE,
-- which has the same defect already corrected on the challenge lock (see LockChallengeForSubmit in
-- queries/gameplay.sql), reintroduced one table over.
-- `submissions.user_id`, `solves.user_id` and `awards.user_id` are FKs to `users`, and an FK insert
-- takes FOR KEY SHARE on the parent row. FOR UPDATE conflicts with FOR KEY SHARE, so an account
-- holding the spend lock would block its own wrong-answer submissions — verified empirically:
--     ERROR:  canceling statement due to lock timeout
--     CONTEXT:  while locking tuple (0,1) in relation "users"
--     SQL statement "SELECT 1 FROM ONLY "public"."users" x WHERE "id" = $1 FOR KEY SHARE OF x"
-- FOR NO KEY UPDATE is compatible with FOR KEY SHARE (the submissions sail through) yet still
-- conflicts with itself (concurrent spends by one account still serialize, which is all this lock was
-- ever for).
CREATE TABLE hint_unlocks (
    id       bigserial   PRIMARY KEY,
    hint_id  bigint      NOT NULL REFERENCES hints(id) ON DELETE CASCADE,   -- a real FK, not a loose int
    user_id  bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    team_id  bigint      REFERENCES teams(id) ON DELETE CASCADE,
    award_id bigint      NOT NULL UNIQUE REFERENCES awards(id) ON DELETE RESTRICT,  -- exactly one charge
    date     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (hint_id, user_id),
    UNIQUE (hint_id, team_id)
);

-- +goose Down
DROP TABLE hint_unlocks;
DROP TABLE awards;
DROP TABLE solves;
DROP TABLE submissions;
