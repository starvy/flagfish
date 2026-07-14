-- Hint unlocks. The transaction contract, in order:
--   BEGIN
--     LockUserForSpend / LockTeamForSpend     -- serialize this account's spends
--     GetAccountScore                          -- exact, read under the lock
--     if score < hint.cost: ROLLBACK -> 400
--     InsertAward(type='hint_unlock', value=-cost)
--     InsertHintUnlock ... ON CONFLICT DO NOTHING  -- no row => already unlocked
--     if no row: ROLLBACK -> 400                   -- the award dies with the tx; no orphan charge
--   COMMIT

-- name: LockUserForSpend :one
-- Serializes this account's spends so the balance can never go negative; the lock is on
-- the account because the balance is a property of the account. Two queries, not one
-- parameterised one: the account model is a table choice, and a table cannot be a bind
-- parameter.
--
-- FOR NO KEY UPDATE: the FK inserts on submissions/solves/awards take FOR KEY SHARE on
-- this row, which FOR UPDATE would block — a wrong answer must never wait on a lock.
-- It still conflicts with itself, so two concurrent spends by one account serialize.
SELECT id FROM users WHERE id = @user_id FOR NO KEY UPDATE;

-- name: LockTeamForSpend :one
-- The teams-mode counterpart. Same lock strength, same reasoning.
SELECT id FROM teams WHERE id = @team_id FOR NO KEY UPDATE;

-- name: GetAccountScore :one
-- The affordability read: an exact SUM over the ledger — a cached balance is an opinion
-- about the past — taken under the spend lock so no concurrent spend can land between
-- this read and the insert that follows. awards.value may be negative (that is what a
-- hint spend is). The account model comes from `instance`, never a caller-supplied flag.
WITH mode AS (
    SELECT user_mode FROM instance
)
SELECT (
    COALESCE((SELECT sum(s.value)
                FROM solves s
                CROSS JOIN mode m
               WHERE CASE WHEN m.user_mode = 'teams'
                          THEN s.team_id = sqlc.narg(team_id)::bigint
                          ELSE s.user_id = sqlc.arg(user_id)::bigint
                     END), 0)
  + COALESCE((SELECT sum(a.value)
                FROM awards a
                CROSS JOIN mode m
               WHERE CASE WHEN m.user_mode = 'teams'
                          THEN a.team_id = sqlc.narg(team_id)::bigint
                          ELSE a.user_id = sqlc.arg(user_id)::bigint
                     END), 0)
)::bigint AS score;

-- name: GetHint :one
-- The challenge id from the URL is part of the key: a hint reached through the wrong challenge —
-- or through a hidden one — is simply not found.
SELECT h.id, h.challenge_id, h.title, h.content, h.cost, h.requirements, h.position
  FROM hints h
  JOIN challenges c ON c.id = h.challenge_id
 WHERE h.id = @hint_id
   AND h.challenge_id = @challenge_id
   AND c.state = 'visible';

-- name: GetHintUnlock :one
-- Read under the account spend lock, which is what makes it safe to branch on. It runs
-- Before affordability: an account that owns a hint and has since spent down to zero
-- must be told it owns the hint — not that it cannot afford it.
WITH mode AS (
    SELECT user_mode FROM instance
)
SELECT hu.id
  FROM hint_unlocks hu
  CROSS JOIN mode m
 WHERE hu.hint_id = @hint_id
   AND CASE WHEN m.user_mode = 'teams'
            THEN hu.team_id = sqlc.narg(team_id)::bigint
            ELSE hu.user_id = sqlc.arg(user_id)::bigint
       END;

-- name: CountUnlockedHintPrerequisites :one
-- The prerequisite gate for hint unlocks. A hint can require other hints to be unlocked first;
-- this counts how many of them this account already owns, and the caller compares that against the
-- number required. Hint prerequisites carry no anonymize flag — a locked hint is simply not
-- purchasable until its prerequisites are unlocked.
WITH mode AS (
    SELECT user_mode FROM instance
)
SELECT count(DISTINCT hu.hint_id)::bigint
  FROM hint_unlocks hu
  CROSS JOIN mode m
 WHERE hu.hint_id = ANY(@prerequisites::bigint[])
   AND CASE WHEN m.user_mode = 'teams'
            THEN hu.team_id = sqlc.narg(team_id)::bigint
            ELSE hu.user_id = sqlc.arg(user_id)::bigint
       END;

-- name: InsertHintUnlock :one
-- The duplicate check IS the constraint, never a SELECT. Zero rows (pgx.ErrNoRows) means
-- already unlocked, and the caller must roll back: the award inserted a moment earlier is
-- a charge, and committing it without the unlock row it paid for is a double-charge.
-- `award_id` is NOT NULL UNIQUE REFERENCES awards(id) — exactly one charge per unlock.
INSERT INTO hint_unlocks (hint_id, user_id, team_id, award_id)
VALUES (@hint_id, @user_id, @team_id, @award_id)
ON CONFLICT DO NOTHING
RETURNING id, date;
